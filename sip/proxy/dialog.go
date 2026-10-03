package proxy

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
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

// Side names a party of a dialog. Requests generated toward Both go to each
// party.
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
	// Answered is a dialog confirmed by a 2xx that has not been ACKed yet.
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
	}

	return "EndCause(?)"
}

// DialogEvent reports a dialog's start, answer and end. Code is the final
// response to the initial INVITE, and By the party that ended the dialog,
// zero when the proxy did.
type DialogEvent struct {
	Kind   EventKind
	Dialog *Dialog
	Code   int
	By     Side
	End    EndCause
}

type DialogConfig struct {
	// Target gives the next hop of a request the tracker generates. By
	// default it is sent on the flows the initial INVITE used.
	Target func(toward Side, req *sip.Request) (Target, error)
}

// Release asks the proxy to end a dialog. While it is being set up, a CANCEL
// with Reason goes to the callee and, when Toward includes the caller, a Code
// response with ResponseReason to the caller. Once answered, a BYE with Reason
// goes to each party in Toward.
type Release struct {
	Toward Side
	Reason []sip.Reason

	Code           int
	ResponseReason []sip.Reason
}

// Body is a session description, offered or answered by From.
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
	prev          *negotiation
}

// Dialog is a proxy's record of an INVITE dialog it record-routes, matched by
// the dialog id in its Record-Route URI.
type Dialog struct {
	p   *Proxy
	id  string
	cfg DialogConfig

	mu sync.Mutex

	begun    bool
	state    DialogState
	released bool
	silent   bool
	closing  map[string]*sip.Request
	byeFrom  Side
	ended    bool
	failing  bool

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
		closing: make(map[string]*sip.Request),
	}
}

// Dialog finds the dialog whose id is in one of the URIs Preprocess removed.
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

func (p *Proxy) emit(events []DialogEvent) {
	if p.onDialog == nil {
		return
	}

	for _, e := range events {
		p.onDialog(e)
	}
}

func (d *Dialog) ID() string {
	return d.id
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

// Released reports whether the proxy released or discarded the dialog, after
// which requests on it are answered 481 (TS 24.229 §5.2.8.1.3, §5.4.5.1.3).
func (d *Dialog) Released() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.released
}

// Contact is the remote target of a party.
func (d *Dialog) Contact(s Side) sip.URI {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.party(s).contact.Clone()
}

// Seq is the highest CSeq seen in the requests of a party, including those the
// proxy generated on its behalf.
func (d *Dialog) Seq(s Side) (uint32, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	pt := d.party(s)

	return pt.seq, pt.haveSeq
}

// Routes is the route set from this proxy toward a party, without its own
// Record-Route entries.
func (d *Dialog) Routes(toward Side) []sip.Address {
	d.mu.Lock()
	defer d.mu.Unlock()

	return cloneAddresses(d.routes(toward))
}

// Session returns the latest offer and its answer; answered is false while the
// offer waits for one.
func (d *Dialog) Session() (offer, answer Body, answered bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.sdp.offer, d.sdp.answer, d.sdp.offer.Data != nil && !d.sdp.pending
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

// calleeRoute is the part of a Record-Route list above the proxy's own
// entries, in the order a request toward the callee visits it.
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
	d.offer(Caller, d.inviteTx, out.Envelope)
	d.arm(d.p.dialogLifetime)

	d.mu.Unlock()

	d.p.mu.Lock()
	d.p.dialogs[d.id] = d
	d.p.mu.Unlock()

	d.p.emit([]DialogEvent{{Kind: EventStarted, Dialog: d}})

	return nil
}

func (d *Dialog) abandon() {
	d.mu.Lock()
	d.stopTimers()
	d.mu.Unlock()

	d.p.forgetDialog(d)
}

// sender finds the party that sent an in-dialog request, by its tags.
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

// request records an in-dialog request before the proxy forwards it. It
// reports false for a request on another dialog with the same Call-ID and
// caller tag, such as one a forking proxy downstream created: the proxy relays
// it without tracking it.
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

	d.requestBody(side, key, out.Envelope)

	return true, nil
}

func (d *Dialog) ack(ack *sip.Request) error {
	cseq, err := ack.Header.CSeq()
	if err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.released {
		return ErrDialogEnded
	}

	side, _, err := d.sender(ack)
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

	d.requestBody(side, txKey{from: side, seq: cseq.Seq, method: "ACK"}, ack.Envelope)

	return nil
}

func (d *Dialog) cancelledByCaller() {
	d.mu.Lock()
	d.cancelled = true
	d.mu.Unlock()
}

// response records a response to a request forwarded on the dialog, before
// the proxy relays it.
func (d *Dialog) response(req *sip.Request, initial bool, r Reply) {
	res := r.Response
	if res == nil {
		res = sip.NewResponse(req, 408, "")
	}

	cseq, err := req.Header.CSeq()
	if err != nil {
		return
	}

	if initial {
		d.inviteResponse(res, r.Err == nil)
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

	var events []DialogEvent

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

	d.responseBody(side.other(), key, res)

	if req.Method == "BYE" && res.StatusCode >= 200 && !d.ended && (d.state == Ending || d.state == Confirmed || d.state == Answered) {
		events = d.end(EndBye, d.byeFrom, d.p.lingerBye)
	}

	d.mu.Unlock()

	d.p.emit(events)
}

func (d *Dialog) inviteResponse(res *sip.Response, downstream bool) {
	to, _ := res.Header.To()
	tag := to.Tag()

	d.mu.Lock()

	var events []DialogEvent

	switch {
	case res.StatusCode <= 100:
	case res.StatusCode < 200:
		if tag == "" || d.state != Early || d.released {
			break
		}

		pt := d.early[tag]
		if pt == nil {
			pt = &party{addr: to}
			d.early[tag] = pt
		}

		if c, ok := firstContact(res.Header); ok {
			pt.contact = c
		}

		if rr, err := res.Header.RecordRoutes(); err == nil && len(rr) > 0 {
			pt.route = rr
		}

		d.responseBody(Callee, d.inviteTx, res)
	case res.IsSuccess():
		switch {
		case d.silent:
			d.state = Ended
		case d.released:
			// The 2xx is relayed all the same (RFC 3261 §16.7 step 5). The
			// proxy completes and ends the callee's session itself; the
			// caller's ACK and BYE then meet a released dialog.
			ack, bye := d.close(res, tag)
			d.mu.Unlock()

			_ = d.p.layer.Go(func(ctx context.Context) {
				d.sendAck(ctx, ack)

				if bye != nil {
					d.p.log.Debug("ending a 2xx to a released INVITE", slog.String("dialog", d.id))
					d.send(bye, Callee)
				}
			})

			return
		}

		if d.ended || d.answerTag != "" {
			break
		}

		d.failing = false

		pt := d.early[tag]
		if pt == nil {
			pt = &party{addr: to}
		}

		if c, ok := firstContact(res.Header); ok {
			pt.contact = c
		}

		if rr, err := res.Header.RecordRoutes(); err == nil {
			pt.route = rr
		}

		d.callee = *pt
		d.answerTag = tag
		d.early = nil
		d.state = Answered
		d.code = res.StatusCode
		d.responseBody(Callee, d.inviteTx, res)
		d.arm(sessionExpires(res, d.p.dialogLifetime))
		d.noAck = d.p.clock.AfterFunc(2*64*d.p.layer.T1(), d.noAckFired)

		events = append(events, DialogEvent{Kind: EventAnswered, Dialog: d, Code: res.StatusCode})
	default:
		if d.state != Early {
			break
		}

		d.code = res.StatusCode
		d.rollback(d.inviteTx)

		if d.ended {
			d.state = Ended
			break
		}

		if !downstream {
			// A 2xx may still follow a response the proxy generated, after
			// Timer C or a timeout, and must then be relayed (RFC 3261
			// §16.7). The dialog ends if none comes.
			d.failing = true
			d.arm(64 * d.p.layer.T1())

			break
		}

		by := Callee
		if res.StatusCode == 487 && (d.cancelled || d.byeFrom == Caller) {
			by = Caller
		}

		events = d.end(EndFailed, by, d.p.lingerBye)
	}

	d.mu.Unlock()

	d.p.emit(events)
}

// close builds the ACK for a 2xx to a released INVITE, and the BYE that
// follows it, the first time it sees that To-tag. A retransmission only needs
// the stored ACK again.
func (d *Dialog) close(res *sip.Response, tag string) (ack, bye *sip.Request) {
	if ack, ok := d.closing[tag]; ok {
		return ack.Clone(), nil
	}

	to, _ := res.Header.To()
	pt := &party{addr: to}

	if c, ok := firstContact(res.Header); ok {
		pt.contact = c
	}

	if rr, err := res.Header.RecordRoutes(); err == nil {
		pt.route = rr
	}

	if d.answerTag == "" {
		d.callee = *pt
		d.answerTag = tag
	}

	ack = d.build("ACK", Callee, pt, d.inviteTx.seq)
	d.closing[tag] = ack

	return ack.Clone(), d.build("BYE", Callee, pt, 0)
}

// retransmitted sends the ACK again for a retransmission of a 2xx to a
// released INVITE.
func (d *Dialog) retransmitted(res *sip.Response) {
	to, _ := res.Header.To()

	d.mu.Lock()
	ack := d.closing[to.Tag()]
	d.mu.Unlock()

	if ack != nil {
		_ = d.p.layer.Go(func(ctx context.Context) { d.sendAck(ctx, ack.Clone()) })
	}
}

func (d *Dialog) sendAck(ctx context.Context, ack *sip.Request) {
	to, err := d.target(Callee, ack)
	if err != nil {
		d.p.log.Debug("no target for a generated ACK", slog.String("dialog", d.id), slog.Any("error", err))
		return
	}

	out, err := d.p.prepare(ack, to)
	if err != nil {
		return
	}

	if err := d.p.layer.SendAck(ctx, out); err != nil {
		d.p.log.Debug("sending a generated ACK failed", slog.String("dialog", d.id), slog.Any("error", err))
	}
}

// Release ends the dialog from the proxy (TS 24.229 §5.2.8.1, §5.4.5.1).
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

	early := d.state == Early
	c := d.ctx

	type generatedBye struct {
		req    *sip.Request
		toward Side
	}

	var byes []generatedBye

	if !early {
		for _, s := range []Side{Callee, Caller} {
			if r.Toward&s == 0 {
				continue
			}

			bye := d.build("BYE", s, &d.callee, 0)
			if len(r.Reason) > 0 {
				bye.Header.Add("Reason", reasons(r.Reason))
			}

			byes = append(byes, generatedBye{req: bye, toward: s})
		}
	}

	events := d.end(cause, 0, d.p.timerC)

	d.mu.Unlock()

	d.p.emit(events)

	for _, b := range byes {
		d.send(b.req, b.toward)
	}

	if !early || c == nil {
		return nil
	}

	c.cancel(reasonFields(r.Reason))

	if r.Toward&Caller != 0 {
		code := r.Code
		if code == 0 {
			code = 500
		}

		res := c.generate(code)
		if len(r.ResponseReason) > 0 {
			res.Header.Add("Reason", reasons(r.ResponseReason))
		}

		_ = c.relay(res)
	}

	return nil
}

// Discard drops the dialog without any SIP (TS 24.229 §5.2.8.1.4).
func (d *Dialog) Discard() {
	d.mu.Lock()

	if d.ended {
		d.mu.Unlock()
		return
	}

	d.released = true
	d.silent = true
	events := d.end(EndDiscarded, 0, d.p.timerC)

	d.mu.Unlock()

	d.p.emit(events)
}

// end marks the dialog ended and keeps it for linger, so that late requests
// still match it. A dialog released while being set up stays Early until the
// final response to its INVITE. It must be called with d.mu held.
func (d *Dialog) end(cause EndCause, by Side, linger time.Duration) []DialogEvent {
	if d.ended {
		return nil
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

	return []DialogEvent{{Kind: EventEnded, Dialog: d, Code: d.code, By: by, End: cause}}
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

	var events []DialogEvent

	if d.failing && !d.ended {
		events = d.end(EndFailed, 0, d.p.lingerBye)
		d.mu.Unlock()

		d.p.emit(events)

		return
	}

	if !d.ended {
		d.p.log.Debug("dialog expired", slog.String("dialog", d.id), slog.String("call-id", d.callID))

		d.ended = true
		d.state = Ended
		events = []DialogEvent{{Kind: EventEnded, Dialog: d, Code: d.code, End: EndExpired}}
	}

	d.stopTimers()

	d.mu.Unlock()

	d.p.forgetDialog(d)
	d.p.emit(events)
}

// noAckFired ends a dialog whose 2xx was never ACKed. The callee should have
// sent a BYE by then (RFC 3261 §13.3.1.4); the proxy does it for both parties
// when it did not.
func (d *Dialog) noAckFired() {
	d.p.log.Debug("2xx never ACKed", slog.String("dialog", d.id))

	d.mu.Lock()
	answered := d.state == Answered
	d.mu.Unlock()

	if !answered {
		return
	}

	reason, _ := sip.NewReason(sip.ReasonReleaseCause, sip.ReleaseNoACK, "")
	_ = d.release(Release{Toward: Both, Reason: []sip.Reason{reason}}, EndNoAck)
}

// build makes a request toward a party on behalf of the other one (TS 24.229
// §5.2.8.1.2), with callee as the callee's leg. A zero seq takes the sender's
// highest CSeq plus one. It must be called with d.mu held.
func (d *Dialog) build(method string, toward Side, callee *party, seq uint32) *sip.Request {
	sender, recipient := &d.caller, callee
	route := calleeRoute(callee.route, d.own)

	if toward == Caller {
		sender, recipient = callee, &d.caller
		route = d.routes(Caller)
	}

	if seq == 0 {
		seq = 1
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

	_ = sip.ApplyStrictRoute(req)

	return req
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

// The offer/answer model of RFC 3264, with the PRACK of RFC 3262 and the
// UPDATE of RFC 3311. A session description is an offer unless it answers
// the pending offer of the other party in the same transaction, or in the
// PRACK or ACK that completes it.

func (d *Dialog) requestBody(from Side, key txKey, e sip.Envelope) {
	body, ok := sessionBody(from, e)
	if !ok {
		return
	}

	if d.sdp.pending && d.sdp.offer.From != from && (key.method == "PRACK" || key.method == "ACK") {
		d.sdp.answer = body
		d.sdp.pending = false

		return
	}

	if key.method == "ACK" || key.method == "PRACK" && d.sdp.pending {
		return
	}

	d.offer(from, key, e)
}

func (d *Dialog) offer(from Side, key txKey, e sip.Envelope) {
	body, ok := sessionBody(from, e)
	if !ok {
		return
	}

	prev := d.sdp.prev
	if !d.sdp.pending {
		done := d.sdp
		done.prev = nil
		prev = &done
	}

	d.sdp = negotiation{offer: body, tx: key, pending: true, prev: prev}
}

func (d *Dialog) responseBody(from Side, key txKey, res *sip.Response) {
	body, ok := sessionBody(from, res.Envelope)

	switch {
	case !ok || res.StatusCode >= 300:
	case d.sdp.tx == key && d.sdp.offer.From != from:
		d.sdp.answer = body
		d.sdp.pending = false
	case d.sdp.tx == key:
		if d.sdp.pending {
			d.sdp.offer = body
		}
	case key.method == "INVITE" && !d.sdp.pending:
		d.offer(from, key, res.Envelope)
	}

	if res.StatusCode >= 300 {
		d.rollback(key)
	}
}

// rollback restores the previous negotiation when the transaction carrying an
// offer fails (RFC 3261 §14.1, RFC 3311 §5.2).
func (d *Dialog) rollback(key txKey) {
	if d.sdp.tx != key || !d.sdp.pending {
		return
	}

	if d.sdp.prev != nil {
		d.sdp = *d.sdp.prev
	} else {
		d.sdp = negotiation{}
	}
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

// sessionExpires is the session interval negotiated in a 2xx (RFC 4028 §9),
// or the default lifetime without one.
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
