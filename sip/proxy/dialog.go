package proxy

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	DefaultDialogLifetime = 12 * time.Hour

	dialogParam = "did"
)

var errOutside = errors.New("sip/proxy: request outside the dialog")

var (
	ErrDialogStarted = errors.New("sip/proxy: dialog already started")

	ErrDialogEnded = errors.New("sip/proxy: dialog ended")
)

type Side int

const (
	Caller Side = 1 << iota
	Callee

	Both = Caller | Callee
)

func (s Side) String() string {
	switch s {
	case Caller:
		return "caller"
	case Callee:
		return "callee"
	case Both:
		return "both"
	}

	return "proxy"
}

func (s Side) other() Side {
	return Both &^ s
}

type DialogState int

const (
	Early DialogState = iota
	Answered
	Confirmed
	Ending
	Ended
)

func (s DialogState) String() string {
	switch s {
	case Early:
		return "early"
	case Answered:
		return "answered"
	case Confirmed:
		return "confirmed"
	case Ending:
		return "ending"
	case Ended:
		return "ended"
	}

	return "DialogState(?)"
}

type EventKind int

const (
	EventStarted EventKind = iota
	EventAnswered
	EventEnded
)

type EndCause int

const (
	EndFailed EndCause = iota + 1
	EndBye
	EndReleased
	EndNoAck
	EndExpired
	EndDiscarded
	EndLost
)

func (c EndCause) String() string {
	switch c {
	case EndFailed:
		return "failed"
	case EndBye:
		return "bye"
	case EndReleased:
		return "released"
	case EndNoAck:
		return "no-ack"
	case EndExpired:
		return "expired"
	case EndDiscarded:
		return "discarded"
	case EndLost:
		return "lost"
	}

	return "EndCause(?)"
}

type DialogEvent struct {
	Kind   EventKind
	Dialog *Dialog
	Code   int
	By     Side
	End    EndCause
}

type DialogConfig struct {
	Target func(toward Side, req *sip.Request) (Target, error)

	Value any

	OnEvent func(DialogEvent)
}

type Release struct {
	Toward Side
	Reason []sip.Reason

	Code           int
	ResponseReason []sip.Reason
}

type Body struct {
	From Side
	Type string
	Data []byte
}

type party struct {
	addr    sip.Address
	contact sip.URI
	seq     uint32
	haveSeq bool
	route   []sip.Address
	sdp     negotiation
}

type closingLeg struct {
	callee party
	ack    *sip.Request
	sent   *sip.Request
	bye    bool
}

type txKey struct {
	from   Side
	seq    uint32
	method string
}

type negotiation struct {
	offer, answer Body
	tx            txKey
	pending       bool
	seq           int
	prev          *negotiation
}

type Exchange struct {
	Offer, Answer Body

	Seq int
}

type Dialog struct {
	p   *Proxy
	id  string
	cfg DialogConfig

	mu sync.Mutex

	begun    bool
	state    DialogState
	released bool
	silent   bool
	reasons  []sip.Reason
	closing  map[string]*closingLeg
	byeFrom  Side
	ended    bool

	outbox   []DialogEvent
	flushing bool

	ctx      *responseContext
	inviteTx txKey
	in       sip.Flow
	upstream Target
	out      Target

	callID    string
	caller    party
	callee    party
	early     map[string]*party
	answerTag string
	code      int
	cancelled bool

	offered   bool
	exchanges int
	lastEarly string

	invites  map[Side]uint32
	refresh  map[txKey]sip.URI
	sdp      negotiation
	timer    transaction.Timer
	timerGen int
	noAck    transaction.Timer
}

func (p *Proxy) NewDialog(cfg DialogConfig) *Dialog {
	return &Dialog{
		p:       p,
		id:      rand.Text()[:16],
		cfg:     cfg,
		early:   make(map[string]*party),
		invites: make(map[Side]uint32),
		refresh: make(map[txKey]sip.URI),
		closing: make(map[string]*closingLeg),
	}
}

func (p *Proxy) Dialog(removed []sip.URI) *Dialog {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, u := range removed {
		if id, ok := u.Params.Get(dialogParam); ok {
			if d := p.dialogs[id]; d != nil {
				return d
			}
		}
	}

	return nil
}

func (p *Proxy) forgetDialog(d *Dialog) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.dialogs[d.id] == d {
		delete(p.dialogs, d.id)
	}
}

func (d *Dialog) publish(e DialogEvent) {
	if d.p.onDialog != nil || d.cfg.OnEvent != nil {
		e.Dialog = d
		d.outbox = append(d.outbox, e)
	}
}

func (d *Dialog) flush() {
	d.mu.Lock()

	if d.flushing {
		d.mu.Unlock()
		return
	}

	d.flushing = true

	for len(d.outbox) > 0 {
		e := d.outbox[0]
		d.outbox = d.outbox[1:]

		d.mu.Unlock()

		if d.p.onDialog != nil {
			d.p.onDialog(e)
		}

		if d.cfg.OnEvent != nil {
			d.cfg.OnEvent(e)
		}

		d.mu.Lock()
	}

	d.flushing = false
	d.mu.Unlock()
}

func (d *Dialog) ID() string {
	return d.id
}

func (d *Dialog) Value() any {
	return d.cfg.Value
}

func (d *Dialog) CallID() string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.callID
}

func (d *Dialog) State() DialogState {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.state
}

// TS 24.229 §5.2.8.1.3, §5.4.5.1.3
func (d *Dialog) Released() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.released
}

func (d *Dialog) Contact(s Side) sip.URI {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.party(s).contact.Clone()
}

func (d *Dialog) Seq(s Side) (uint32, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	pt := d.party(s)

	return pt.seq, pt.haveSeq
}

func (d *Dialog) Routes(toward Side) []sip.Address {
	d.mu.Lock()
	defer d.mu.Unlock()

	return cloneAddresses(d.routes(toward))
}

func (d *Dialog) RouteSet(req *sip.Request) ([]sip.Address, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	side, leg, err := d.sender(req)
	if err != nil {
		return nil, false
	}

	if side == Caller {
		return calleeRoute(leg.route, d.own), len(leg.route) > 0
	}

	return cloneAddresses(d.routes(Caller)), true
}

func (d *Dialog) CallerTag() string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.caller.addr.Tag()
}

func (d *Dialog) Session() (offer, answer Body, answered bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	n := &d.sdp
	if pt := d.early[d.lastEarly]; d.answerTag == "" && pt != nil {
		n = &pt.sdp
	}

	return n.offer, n.answer, n.offer.Data != nil && !n.pending
}

// RFC 3264, TS 29.214 Annex A.3.2
func (d *Dialog) Exchange(m sip.Message) (Exchange, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	n := d.legNegotiation(m)
	if n == nil || n.pending || n.offer.Data == nil || n.answer.Data == nil {
		return Exchange{}, false
	}

	return Exchange{Offer: n.offer, Answer: n.answer, Seq: n.seq}, true
}

// RFC 3262 §5, RFC 3264
func (d *Dialog) PendingOffer(req *sip.Request) (Body, bool) {
	if req.Method != "PRACK" && req.Method != "ACK" {
		return Body{}, false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	side, callee, err := d.sender(req)
	if err != nil {
		return Body{}, false
	}

	n := d.negotiation(callee)
	if !n.pending || n.offer.From == side {
		return Body{}, false
	}

	if _, ok := sessionBody(side, req.Envelope); !ok {
		return Body{}, false
	}

	return n.offer, true
}

func (d *Dialog) legNegotiation(m sip.Message) *negotiation {
	from, err := m.Env().Header.From()
	if err != nil {
		return nil
	}

	to, err := m.Env().Header.To()
	if err != nil {
		return nil
	}

	tag := to.Tag()
	if from.Tag() != d.caller.addr.Tag() {
		tag = from.Tag()
	}

	if d.answerTag != "" {
		if tag != d.answerTag {
			return nil
		}

		return &d.sdp
	}

	if pt := d.early[tag]; pt != nil {
		return &pt.sdp
	}

	return nil
}

func (d *Dialog) negotiation(callee *party) *negotiation {
	if callee == nil || d.answerTag != "" {
		return &d.sdp
	}

	return &callee.sdp
}

func (d *Dialog) party(s Side) *party {
	if s == Caller {
		return &d.caller
	}

	return &d.callee
}

func (d *Dialog) own(a sip.Address) bool {
	id, _ := a.URI.Params.Get(dialogParam)
	return id == d.id
}

func (d *Dialog) routes(toward Side) []sip.Address {
	if toward == Callee {
		return calleeRoute(d.callee.route, d.own)
	}

	rr := d.caller.route

	i := 0
	for i < len(rr) && d.own(rr[i]) {
		i++
	}

	return rr[i:]
}

func calleeRoute(rr []sip.Address, own func(sip.Address) bool) []sip.Address {
	end := slices.IndexFunc(rr, own)
	if end < 0 {
		return nil
	}

	out := slices.Clone(rr[:end])
	slices.Reverse(out)

	return out
}

func (d *Dialog) begin(tx *transaction.ServerTransaction, c *responseContext, out *sip.Request, to Target, rr *RecordRoute) error {
	from, err := out.Header.From()
	if err != nil {
		return &sip.StatusError{StatusCode: 400, Err: err}
	}

	cseq, err := out.Header.CSeq()
	if err != nil {
		return &sip.StatusError{StatusCode: 400, Err: err}
	}

	contact, _ := firstContact(out.Header)
	route, _ := out.Header.RecordRoutes()

	d.mu.Lock()

	if d.begun {
		d.mu.Unlock()
		return ErrDialogStarted
	}

	d.begun = true
	d.ctx = c
	d.inviteTx = txKey{from: Caller, seq: cseq.Seq, method: "INVITE"}
	d.in = tx.Request().Flow
	d.upstream = Target{Flow: d.in, SentBy: rr.Upstream}
	d.out = to
	d.callID = out.Header.CallID()
	d.caller = party{addr: from, contact: contact, seq: cseq.Seq, haveSeq: true, route: route}
	d.invites[Caller] = cseq.Seq
	_, d.offered = sessionBody(Caller, out.Envelope)
	d.offer(&d.sdp, Caller, d.inviteTx, out.Envelope)
	d.arm(d.p.dialogLifetime)

	d.mu.Unlock()

	d.p.mu.Lock()
	d.p.dialogs[d.id] = d
	d.p.mu.Unlock()

	return nil
}

func (d *Dialog) started() {
	d.mu.Lock()
	d.publish(DialogEvent{Kind: EventStarted})
	d.mu.Unlock()

	d.flush()
}

func (d *Dialog) abandon() {
	d.mu.Lock()
	d.ended = true
	d.state = Ended
	d.stopTimers()
	d.mu.Unlock()

	d.p.forgetDialog(d)
}

func (d *Dialog) Party(req *sip.Request) (Side, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	side, _, err := d.sender(req)

	return side, err == nil
}

func (d *Dialog) sender(m *sip.Request) (Side, *party, error) {
	from, err := m.Header.From()
	if err != nil {
		return 0, nil, &sip.StatusError{StatusCode: 400, Err: err}
	}

	to, err := m.Header.To()
	if err != nil {
		return 0, nil, &sip.StatusError{StatusCode: 400, Err: err}
	}

	if from.Tag() == d.caller.addr.Tag() {
		if pt := d.calleeLeg(to.Tag()); pt != nil {
			return Caller, pt, nil
		}
	} else if to.Tag() == d.caller.addr.Tag() {
		if pt := d.calleeLeg(from.Tag()); pt != nil {
			return Callee, pt, nil
		}
	}

	return 0, nil, errOutside
}

func (d *Dialog) calleeLeg(tag string) *party {
	if d.answerTag != "" {
		if tag == d.answerTag {
			return &d.callee
		}

		return nil
	}

	return d.early[tag]
}

func (d *Dialog) request(out *sip.Request) (bool, error) {
	cseq, err := out.Header.CSeq()
	if err != nil {
		return false, &sip.StatusError{StatusCode: 400, Err: err}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.released {
		return false, &sip.StatusError{StatusCode: 481, Err: ErrDialogEnded}
	}

	side, callee, err := d.sender(out)
	if err != nil {
		d.p.log.Debug("request on an untracked dialog", slog.String("dialog", d.id), slog.String("request", out.StartLine()))
		return false, nil
	}

	pt := &d.caller
	if side == Callee {
		pt = callee
	}

	if pt.haveSeq && cseq.Seq <= pt.seq {
		d.p.log.Debug("in-dialog CSeq does not increase", slog.String("dialog", d.id), slog.String("from", side.String()),
			slog.Uint64("cseq", uint64(cseq.Seq)), slog.Uint64("highest", uint64(pt.seq)))
	} else {
		pt.seq, pt.haveSeq = cseq.Seq, true
	}

	key := txKey{from: side, seq: cseq.Seq, method: out.Method}

	switch out.Method {
	case "INVITE":
		d.invites[side] = cseq.Seq
		fallthrough
	case "UPDATE":
		if c, ok := firstContact(out.Header); ok {
			d.refresh[key] = c
		}
	case "BYE":
		d.byeFrom = side

		if d.state == Answered || d.state == Confirmed {
			d.state = Ending
		}
	}

	d.requestBody(d.negotiation(callee), side, key, out.Envelope)

	return true, nil
}

func (d *Dialog) ack(ack *sip.Request) error {
	cseq, err := ack.Header.CSeq()
	if err != nil {
		return err
	}

	d.mu.Lock()

	if d.released {
		var bye *sip.Request

		to, _ := ack.Header.To()

		if leg := d.closing[to.Tag()]; leg != nil && leg.bye && cseq.Seq == d.inviteTx.seq {
			leg.bye = false
			bye = d.byeWithReason(Caller, &leg.callee)
		}

		d.mu.Unlock()

		if bye != nil {
			d.send(bye, Caller)
		}

		return ErrDialogEnded
	}

	defer d.mu.Unlock()

	side, callee, err := d.sender(ack)
	if err != nil {
		return nil
	}

	if seq, ok := d.invites[side]; !ok || seq != cseq.Seq {
		d.p.log.Debug("ACK CSeq does not match the INVITE", slog.String("dialog", d.id),
			slog.Uint64("cseq", uint64(cseq.Seq)), slog.Uint64("invite", uint64(seq)))

		return nil
	}

	if side == Caller && cseq.Seq == d.inviteTx.seq && d.state == Answered {
		d.state = Confirmed

		if d.noAck != nil {
			d.noAck.Stop()
			d.noAck = nil
		}
	}

	d.requestBody(d.negotiation(callee), side, txKey{from: side, seq: cseq.Seq, method: "ACK"}, ack.Envelope)

	return nil
}

func (d *Dialog) cancelledByCaller() {
	d.mu.Lock()
	d.cancelled = true
	d.mu.Unlock()
}

func (d *Dialog) response(req *sip.Request, initial bool, r Reply) {
	res := r.Response
	if res == nil {
		res = sip.NewResponse(req, 408, "")
	}

	cseq, err := req.Header.CSeq()
	if err != nil {
		return
	}

	downstream := r.Err == nil

	if initial {
		d.inviteResponse(res, downstream)
		return
	}

	d.mu.Lock()

	side, callee, err := d.sender(req)
	if err != nil {
		d.mu.Unlock()
		return
	}

	sender, responder := &d.caller, callee
	if side == Callee {
		sender, responder = callee, &d.caller
	}

	key := txKey{from: side, seq: cseq.Seq, method: req.Method}

	switch {
	case res.StatusCode <= 100:
	case res.StatusCode < 300:
		if c, ok := d.refresh[key]; ok {
			sender.contact = c
		}

		if c, ok := firstContact(res.Header); ok && (req.Method == "INVITE" || req.Method == "UPDATE") {
			responder.contact = c
		}

		if res.IsSuccess() && (req.Method == "INVITE" || req.Method == "UPDATE") && !d.ended && d.state != Early {
			d.arm(sessionExpires(res, d.p.dialogLifetime))
		}
	}

	if res.StatusCode >= 200 {
		delete(d.refresh, key)
	}

	d.responseBody(d.negotiation(callee), side.other(), key, res)

	confirmed := d.state == Ending || d.state == Confirmed || d.state == Answered

	switch {
	case d.ended || !confirmed || res.StatusCode < 200:
	case req.Method == "BYE":
		d.end(EndBye, d.byeFrom, d.p.lingerBye)
	case res.StatusCode == 481 || res.StatusCode == 408:
		by := side.other()
		if !downstream {
			by = 0
		}

		d.end(EndLost, by, d.p.lingerBye)
	}

	d.mu.Unlock()

	d.flush()
}

func (d *Dialog) inviteResponse(res *sip.Response, downstream bool) {
	to, _ := res.Header.To()
	tag := to.Tag()

	d.mu.Lock()

	switch {
	case res.StatusCode <= 100:
	case res.StatusCode < 200:
		if tag == "" || d.state != Early || d.released {
			break
		}

		pt := d.early[tag]
		if pt == nil {
			pt = &party{addr: to, sdp: d.sdp}
			d.early[tag] = pt
		}

		d.lastEarly = tag

		if c, ok := firstContact(res.Header); ok {
			pt.contact = c
		}

		if rr, err := res.Header.RecordRoutes(); err == nil && len(rr) > 0 {
			pt.route = rr
		}

		if _, ok := sessionBody(Callee, res.Envelope); ok {
			d.offered = true
		}

		d.responseBody(&pt.sdp, Callee, d.inviteTx, res)
	case res.IsSuccess():
		switch {
		case d.silent:
			d.state = Ended
		case d.released:
			leg, fresh := d.close(res, tag)
			d.mu.Unlock()

			if fresh {
				d.p.log.Debug("ending a 2xx to a released INVITE", slog.String("dialog", d.id))
			}

			d.closeCallee(leg, fresh)

			return
		}

		if d.ended || d.answerTag != "" {
			break
		}

		pt := d.early[tag]
		if pt == nil {
			pt = &party{addr: to, sdp: d.sdp}
		}

		if c, ok := firstContact(res.Header); ok {
			pt.contact = c
		}

		if rr, err := res.Header.RecordRoutes(); err == nil {
			pt.route = rr
		}

		d.callee = *pt
		d.sdp = pt.sdp
		d.callee.sdp = negotiation{}
		d.answerTag = tag
		d.early = nil
		d.state = Answered
		d.code = res.StatusCode
		d.responseBody(&d.sdp, Callee, d.inviteTx, res)
		d.arm(sessionExpires(res, d.p.dialogLifetime))
		d.noAck = d.p.clock.AfterFunc(2*64*d.p.layer.T1(), d.noAckFired)

		d.publish(DialogEvent{Kind: EventAnswered, Code: res.StatusCode})
	default:
		if d.state != Early {
			break
		}

		d.code = res.StatusCode
		d.rollback(&d.sdp, d.inviteTx)

		if d.ended {
			d.state = Ended
			break
		}

		if !downstream {
			d.released = true
			d.end(EndFailed, 0, d.p.timerC)

			break
		}

		by := Callee
		if res.StatusCode == 487 && (d.cancelled || d.byeFrom == Caller) {
			by = Caller
		}

		d.end(EndFailed, by, d.p.lingerBye)
	}

	d.mu.Unlock()

	d.flush()
}

// RFC 3261 §13.2.2.4
func (d *Dialog) close(res *sip.Response, tag string) (*closingLeg, bool) {
	if leg, ok := d.closing[tag]; ok {
		return leg, false
	}

	to, _ := res.Header.To()
	leg := &closingLeg{callee: party{addr: to}, bye: true}

	if c, ok := firstContact(res.Header); ok {
		leg.callee.contact = c
	}

	if rr, err := res.Header.RecordRoutes(); err == nil {
		leg.callee.route = rr
	}

	if d.answerTag == "" {
		d.callee = leg.callee
		d.answerTag = tag
	}

	leg.ack = d.build("ACK", Callee, &leg.callee, d.inviteTx.seq)

	if offer, ok := sessionBody(Callee, res.Envelope); ok && !d.offered {
		if answer, err := rejectOffer(offer.Data); err == nil {
			leg.ack.SetBody("application/sdp", answer)
		}
	}

	d.closing[tag] = leg

	return leg, true
}

func (d *Dialog) closeCallee(leg *closingLeg, fresh bool) {
	var bye *sip.Request

	if fresh {
		d.mu.Lock()
		bye = d.byeWithReason(Callee, &leg.callee)
		d.mu.Unlock()
	}

	_ = d.p.layer.Go(func(ctx context.Context) {
		d.sendAck(ctx, leg)

		if bye != nil {
			d.send(bye, Callee)
		}
	})
}

func (d *Dialog) retransmitted(res *sip.Response) {
	to, _ := res.Header.To()

	d.mu.Lock()
	leg := d.closing[to.Tag()]
	d.mu.Unlock()

	if leg != nil {
		d.closeCallee(leg, false)
	}
}

func (d *Dialog) sendAck(ctx context.Context, leg *closingLeg) {
	d.mu.Lock()
	out := leg.sent
	d.mu.Unlock()

	if out == nil {
		ack := leg.ack.Clone()

		to, err := d.target(Callee, ack)
		if err != nil {
			d.p.log.Debug("no target for a generated ACK", slog.String("dialog", d.id), slog.Any("error", err))
			return
		}

		if out, err = d.p.prepare(ack, to); err != nil {
			return
		}

		d.mu.Lock()
		if leg.sent == nil {
			leg.sent = out
		}

		out = leg.sent
		d.mu.Unlock()
	}

	if err := d.p.layer.SendAck(ctx, out.Clone()); err != nil {
		d.p.log.Debug("sending a generated ACK failed", slog.String("dialog", d.id), slog.Any("error", err))
	}
}

func (d *Dialog) byeWithReason(toward Side, callee *party) *sip.Request {
	bye := d.build("BYE", toward, callee, 0)
	if len(d.reasons) > 0 {
		bye.Header.Add("Reason", reasons(d.reasons))
	}

	return bye
}

// TS 24.229 §5.2.8.1, §5.4.5.1
func (d *Dialog) Release(r Release) error {
	return d.release(r, EndReleased)
}

func (d *Dialog) release(r Release, cause EndCause) error {
	d.mu.Lock()

	if !d.begun || d.ended || d.released {
		d.mu.Unlock()
		return ErrDialogEnded
	}

	d.released = true
	d.reasons = slices.Clone(r.Reason)

	state := d.state
	c := d.ctx

	var (
		byes []*sip.Request
		leg  *closingLeg
	)

	switch state {
	case Early:
	case Answered:
		leg = &closingLeg{callee: d.callee, ack: d.build("ACK", Callee, &d.callee, d.inviteTx.seq), bye: r.Toward&Caller != 0}
		d.closing[d.answerTag] = leg

		if r.Toward&Callee != 0 {
			byes = append(byes, d.byeWithReason(Callee, &d.callee))
		}
	default:
		for _, s := range []Side{Callee, Caller} {
			if r.Toward&s != 0 {
				byes = append(byes, d.byeWithReason(s, &d.callee))
			}
		}
	}

	d.end(cause, 0, d.p.timerC)

	d.mu.Unlock()

	d.flush()

	switch {
	case state == Early:
		if c == nil {
			return nil
		}

		c.cancel(reasonFields(r.Reason))

		if r.Toward&Caller != 0 {
			_ = c.relay(releaseResponse(c, r))
		}
	case leg != nil:
		if r.Toward&Caller != 0 && c != nil && c.relay(releaseResponse(c, r)) == nil {
			d.mu.Lock()
			leg.bye = false
			d.mu.Unlock()
		}

		_ = d.p.layer.Go(func(ctx context.Context) {
			d.sendAck(ctx, leg)

			for _, bye := range byes {
				d.send(bye, Callee)
			}
		})

		return nil
	}

	for _, bye := range byes {
		toward := Callee
		if to, _ := bye.Header.To(); to.Tag() == d.caller.addr.Tag() {
			toward = Caller
		}

		d.send(bye, toward)
	}

	return nil
}

func releaseResponse(c *responseContext, r Release) *sip.Response {
	code := r.Code
	if code == 0 {
		code = 500
	}

	res := c.generate(code)
	if len(r.ResponseReason) > 0 {
		res.Header.Add("Reason", reasons(r.ResponseReason))
	}

	return res
}

// TS 24.229 §5.2.8.1.4
func (d *Dialog) Discard() {
	d.mu.Lock()

	if !d.begun || d.ended {
		d.mu.Unlock()
		return
	}

	d.released = true
	d.silent = true
	d.end(EndDiscarded, 0, d.p.timerC)

	d.mu.Unlock()

	d.flush()
}

func (d *Dialog) end(cause EndCause, by Side, linger time.Duration) {
	if d.ended {
		return
	}

	d.ended = true

	if d.state != Early || cause == EndFailed {
		d.state = Ended
	}

	if d.noAck != nil {
		d.noAck.Stop()
		d.noAck = nil
	}

	d.arm(linger)
	d.publish(DialogEvent{Kind: EventEnded, Code: d.code, By: by, End: cause})
}

func (d *Dialog) arm(after time.Duration) {
	if d.timer != nil {
		d.timer.Stop()
	}

	d.timerGen++
	gen := d.timerGen
	d.timer = d.p.clock.AfterFunc(after, func() { d.expire(gen) })
}

func (d *Dialog) stopTimers() {
	for _, t := range []transaction.Timer{d.timer, d.noAck} {
		if t != nil {
			t.Stop()
		}
	}

	d.timerGen++
}

func (d *Dialog) expire(gen int) {
	d.mu.Lock()

	if gen != d.timerGen {
		d.mu.Unlock()
		return
	}

	if !d.ended {
		d.p.log.Debug("dialog expired", slog.String("dialog", d.id), slog.String("call-id", d.callID))

		d.ended = true
		d.state = Ended
		d.publish(DialogEvent{Kind: EventEnded, Code: d.code, End: EndExpired})
	}

	d.stopTimers()

	d.mu.Unlock()

	d.p.forgetDialog(d)
	d.flush()
}

// RFC 3261 §13.3.1.4
func (d *Dialog) noAckFired() {
	d.mu.Lock()

	if d.state == Answered && !d.ended {
		d.p.log.Debug("2xx never ACKed", slog.String("dialog", d.id))
		d.end(EndNoAck, 0, d.p.lingerBye)
	}

	d.mu.Unlock()

	d.flush()
}

// TS 24.229 §5.2.8.1.2
func (d *Dialog) build(method string, toward Side, callee *party, seq uint32) *sip.Request {
	sender, recipient := &d.caller, callee
	route := calleeRoute(callee.route, d.own)

	if toward == Caller {
		sender, recipient = callee, &d.caller
		route = d.routes(Caller)
	}

	if seq == 0 {
		seq = randomSeq()
		if sender.haveSeq {
			seq = sender.seq + 1
		}

		sender.seq, sender.haveSeq = seq, true
	}

	req := sip.NewRequest(method, recipient.contact.Clone())
	req.Header = nil

	if len(route) > 0 {
		vs := make([]string, len(route))
		for i, a := range route {
			vs[i] = a.String()
		}

		req.Header.Add("Route", strings.Join(vs, ", "))
	}

	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", sender.addr.String())
	req.Header.Add("To", recipient.addr.String())
	req.Header.Add("Call-ID", d.callID)
	req.Header.Add("CSeq", sip.CSeq{Seq: seq, Method: method}.String())
	req.Header.Add("Content-Length", "0")

	return req
}

func randomSeq() uint32 {
	var b [4]byte

	_, _ = rand.Read(b[:])

	return binary.BigEndian.Uint32(b[:])%(1<<31-1) + 1
}

func (d *Dialog) target(toward Side, req *sip.Request) (Target, error) {
	if d.cfg.Target != nil {
		return d.cfg.Target(toward, req)
	}

	tr, dest, err := sip.NextHop(req)
	if err != nil {
		return Target{}, err
	}

	base := d.out
	if toward == Caller {
		base = d.upstream
	}

	return Target{Flow: sip.Flow{Transport: tr, Local: base.Flow.Local, Remote: dest}, SentBy: base.SentBy}, nil
}

func (d *Dialog) send(req *sip.Request, toward Side) {
	to, err := d.target(toward, req)
	if err != nil {
		d.p.log.Debug("no target for a generated request", slog.String("dialog", d.id), slog.String("request", req.StartLine()),
			slog.Any("error", err))

		return
	}

	out, err := d.p.prepare(req, to)
	if err != nil {
		return
	}

	if _, err := d.p.layer.Request(out, generated{d: d, req: out}); err != nil {
		d.p.log.Debug("sending a generated request failed", slog.String("dialog", d.id), slog.String("request", out.StartLine()),
			slog.Any("error", err))
	}
}

type generated struct {
	d   *Dialog
	req *sip.Request
}

func (g generated) HandleResponse(res *sip.Response) {
	if res.StatusCode >= 200 {
		g.d.p.log.Debug("generated request answered", slog.String("dialog", g.d.id), slog.String("request", g.req.StartLine()),
			slog.Int("code", res.StatusCode))
	}
}

func (g generated) HandleError(err error) {
	g.d.p.log.Debug("generated request failed", slog.String("dialog", g.d.id), slog.String("request", g.req.StartLine()),
		slog.Any("error", err))
}

func (d *Dialog) requestBody(n *negotiation, from Side, key txKey, e sip.Envelope) {
	body, ok := sessionBody(from, e)
	if !ok {
		return
	}

	if n.pending && n.offer.From != from && (key.method == "PRACK" || key.method == "ACK") {
		d.answered(n, body)
		return
	}

	if key.method == "ACK" || key.method == "PRACK" && n.pending {
		return
	}

	d.offer(n, from, key, e)
}

func (d *Dialog) offer(n *negotiation, from Side, key txKey, e sip.Envelope) {
	body, ok := sessionBody(from, e)
	if !ok {
		return
	}

	prev := n.prev
	if !n.pending {
		done := *n
		done.prev = nil
		prev = &done
	}

	*n = negotiation{offer: body, tx: key, pending: true, prev: prev}
}

func (d *Dialog) answered(n *negotiation, body Body) {
	n.answer = body

	if n.pending {
		d.exchanges++
		n.seq = d.exchanges
	}

	n.pending = false
}

func (d *Dialog) responseBody(n *negotiation, from Side, key txKey, res *sip.Response) {
	body, ok := sessionBody(from, res.Envelope)

	switch {
	case !ok || res.StatusCode >= 300:
	case n.tx == key && n.offer.From != from:
		d.answered(n, body)
	case n.tx == key:
		if n.pending {
			n.offer = body
		}
	case key.method == "INVITE" && !n.pending:
		d.offer(n, from, key, res.Envelope)
	}

	if res.StatusCode >= 300 {
		d.rollback(n, key)
	}
}

// RFC 3261 §14.1, RFC 3311 §5.2
func (d *Dialog) rollback(n *negotiation, key txKey) {
	if n.tx != key || !n.pending {
		return
	}

	if n.prev != nil {
		*n = *n.prev
	} else {
		*n = negotiation{}
	}
}

// RFC 3264
func rejectOffer(offer []byte) ([]byte, error) {
	s, err := sdp.Parse(offer)
	if err != nil {
		return nil, err
	}

	if o, err := s.Origin(); err == nil {
		o.Username, o.SessionID, o.SessionVersion = "-", strconv.FormatUint(uint64(randomSeq()), 10), "1"
		s.SetOrigin(o)
	}

	for _, m := range s.Media {
		m.Disable()
	}

	return s.Bytes(), nil
}

func sessionBody(from Side, e sip.Envelope) (Body, bool) {
	if len(e.Body) == 0 {
		return Body{}, false
	}

	mt, _, _ := strings.Cut(e.Header.ContentType(), ";")
	if !strings.EqualFold(strings.TrimSpace(mt), "application/sdp") {
		return Body{}, false
	}

	return Body{From: from, Type: "application/sdp", Data: slices.Clone(e.Body)}, true
}

// RFC 4028
func sessionExpires(res *sip.Response, lifetime time.Duration) time.Duration {
	v, _, err := sip.ParseTokenParams(res.Header.Get("Session-Expires"))
	if err != nil || v == "" {
		return lifetime
	}

	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 {
		return lifetime
	}

	return time.Duration(n) * time.Second
}

func firstContact(h sip.Header) (sip.URI, bool) {
	cs, err := h.Contacts()
	if err != nil || len(cs) == 0 || cs[0].Star {
		return sip.URI{}, false
	}

	return cs[0].URI, true
}

func reasons(rs []sip.Reason) string {
	vs := make([]string, len(rs))
	for i, r := range rs {
		vs[i] = r.String()
	}

	return strings.Join(vs, ", ")
}

func reasonFields(rs []sip.Reason) []sip.Field {
	fs := make([]sip.Field, len(rs))
	for i, r := range rs {
		fs[i] = sip.Field{Name: "Reason", Value: r.String()}
	}

	return fs
}

func cloneAddresses(as []sip.Address) []sip.Address {
	out := make([]sip.Address, len(as))
	for i, a := range as {
		out[i] = a.Clone()
	}

	return out
}
