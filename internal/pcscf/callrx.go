package pcscf

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	DefaultRxCallTimeout = 3 * time.Second

	DefaultMediaLossTimeout = 5 * time.Second
)

var (
	errCallEnded = errors.New("the call has ended")

	errRetryInterval = errors.New("the same service information was refused and its retry interval has not elapsed")
)

type callRx struct {
	key        regKey
	identities []string
	service    string

	call   *call
	dialog *proxy.Dialog

	mu       sync.Mutex
	session  *rxSession
	busy     bool
	queue    []func()
	done     map[int]bool
	early    map[string]bool
	forked   bool
	flows    map[int]flowNumbers
	active   map[uint32]bool
	charging string
	loss     transaction.Timer
	lost     []uint32
	ended    bool
}

func (p *PCSCF) newCallRx(k regKey, identities []string, service string) *callRx {
	if p.rx == nil {
		return nil
	}

	return &callRx{
		key: k, identities: identities, service: service,
		done: make(map[int]bool), early: make(map[string]bool), flows: make(map[int]flowNumbers), active: make(map[uint32]bool),
	}
}

func (cr *callRx) attach(c *call, d *proxy.Dialog) {
	if cr != nil {
		cr.call, cr.dialog = c, d
	}
}

type answerJob struct {
	x       *exchange
	seq     int
	tag     string
	early   bool
	initial bool
	final   bool
	headers sip.Header
}

// TS 29.214 Annex A.1, A.3, TS 29.213 Annex B.2
func (p *PCSCF) mediaReply(tx *transaction.ServerTransaction, d *proxy.Dialog, rep proxy.Reply, initial bool) proxy.Verdict {
	c := callOf(d)
	if c == nil || c.rx == nil || rep.Response == nil {
		return proxy.Relay
	}

	res := rep.Response

	var job *answerJob
	if rep.Err == nil {
		job = p.responseAnswer(c, d, res, initial)
	}

	run := func() {
		if job != nil && !p.authorize(c, d, *job, res) {
			return
		}

		if err := p.cfg.Proxy.Relay(tx, res); err != nil {
			p.log.Debug("relaying a held response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}
	}

	if p.enqueue(c.rx, job != nil, run) {
		return proxy.Hold
	}

	return proxy.Relay
}

// RFC 3262 §5, RFC 3264, TS 29.214 Annex A.1
func (p *PCSCF) mediaRequest(d *proxy.Dialog, out *sip.Request, forward, reject func()) bool {
	c := callOf(d)
	if c == nil || c.rx == nil {
		return false
	}

	_, answers := d.PendingOffer(out)

	return p.enqueue(c.rx, answers, func() {
		offer, ok := d.PendingOffer(out)
		if !ok {
			forward()
			return
		}

		x := p.parseExchange(c, d, proxy.Exchange{Offer: offer, Answer: proxy.Body{From: other(offer.From), Data: out.Body}})
		if x == nil {
			forward()
			return
		}

		state := d.State()
		job := answerJob{
			x: x, tag: calleeTag(d, out), early: state == proxy.Early, headers: out.Header,
			initial: state == proxy.Early || out.Method == "ACK" && state == proxy.Answered,
		}

		switch {
		case p.authorize(c, d, job, nil), out.Method == "ACK":
			forward()
		case reject != nil:
			reject()
		}

		if ex, ok := d.Exchange(out); ok {
			c.rx.mu.Lock()
			c.rx.done[ex.Seq] = true
			c.rx.mu.Unlock()
		}
	})
}

func other(s proxy.Side) proxy.Side {
	if s == proxy.Caller {
		return proxy.Callee
	}

	return proxy.Caller
}

func calleeTag(d *proxy.Dialog, m sip.Message) string {
	from, _ := m.Env().Header.From()
	to, _ := m.Env().Header.To()

	if from.Tag() == d.CallerTag() {
		return to.Tag()
	}

	return from.Tag()
}

func (p *PCSCF) enqueue(cr *callRx, needed bool, run func()) bool {
	cr.mu.Lock()

	if cr.busy {
		cr.queue = append(cr.queue, run)
		cr.mu.Unlock()

		return true
	}

	if !needed {
		cr.mu.Unlock()
		return false
	}

	cr.busy = true
	cr.mu.Unlock()

	if !p.rx.spawn(func() { p.drain(cr, run) }) {
		cr.mu.Lock()
		cr.busy = false
		cr.mu.Unlock()

		return false
	}

	return true
}

func (p *PCSCF) drain(cr *callRx, run func()) {
	for {
		run()

		cr.mu.Lock()

		if len(cr.queue) == 0 {
			cr.busy = false
			cr.mu.Unlock()

			return
		}

		run = cr.queue[0]
		cr.queue = cr.queue[1:]

		cr.mu.Unlock()
	}
}

// TS 29.214 Annex A.1, A.3.2
func (p *PCSCF) responseAnswer(c *call, d *proxy.Dialog, res *sip.Response, initial bool) *answerJob {
	if res.StatusCode <= 100 || res.StatusCode >= 300 {
		return nil
	}

	ex, ok := d.Exchange(res)
	if !ok {
		return nil
	}

	final := initial && res.IsSuccess()

	cr := c.rx

	cr.mu.Lock()
	fresh := !cr.done[ex.Seq] && len(res.Body) > 0 && bytes.Equal(ex.Answer.Data, res.Body)
	forked := cr.forked
	cr.mu.Unlock()

	if !fresh && (!final || !forked) {
		return nil
	}

	x := p.parseExchange(c, d, ex)
	if x == nil {
		return nil
	}

	return &answerJob{
		x: x, seq: ex.Seq, tag: calleeTag(d, res), early: initial && !final, initial: initial, final: final, headers: res.Header,
	}
}

func (p *PCSCF) parseExchange(c *call, d *proxy.Dialog, ex proxy.Exchange) *exchange {
	o, err := sdp.Parse(ex.Offer.Data)
	if err != nil {
		p.log.Warn("unusable SDP offer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	a, err := sdp.Parse(ex.Answer.Data)
	if err != nil {
		p.log.Warn("unusable SDP answer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	return &exchange{offer: o, answer: a, offerFromUE: ex.Offer.From == c.ue}
}

// TS 24.229 §5.2.7.2, TS 29.214 §4.4.1
func (p *PCSCF) authorize(c *call, d *proxy.Dialog, job answerJob, res *sip.Response) bool {
	err := p.callAAR(c, d, job)
	if err == nil {
		if res != nil && c.ue == proxy.Callee && reliable(res) {
			chargeResponse(c, res)
		}

		return true
	}

	if errors.Is(err, errCallEnded) {
		return true
	}

	attrs := []any{
		slog.String("dialog", d.ID()), slog.String("impi", c.rx.key.impi), slog.String("ue", c.rx.key.ue.String()),
		slog.Any("error", err),
	}

	if result, ok := tgpp.ResultOf(err); ok {
		attrs = append(attrs, slog.String("result", result.String()))
	}

	if !job.initial {
		p.log.Warn("media authorization refused for a session modification", attrs...)
		return true
	}

	p.log.Warn("media authorization refused: releasing the call", attrs...)

	if err := d.Release(proxy.Release{Toward: proxy.Both, Code: 500}); err != nil {
		p.log.Debug("releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
	}

	return false
}

// RFC 3262 §3
func reliable(res *sip.Response) bool {
	return res.IsSuccess() || res.Header.Has("RSeq")
}

// TS 29.214 §4.4.1, §4.4.2, Annex A.3
func (p *PCSCF) callAAR(c *call, d *proxy.Dialog, job answerJob) error {
	cr := c.rx

	cr.mu.Lock()

	if cr.ended {
		cr.mu.Unlock()
		return errCallEnded
	}

	flows := make(map[int]flowNumbers, len(cr.flows))
	for k, v := range cr.flows {
		flows[k] = v
	}

	s := cr.session
	if s == nil {
		s = p.rx.callSession(cr)
		cr.session = s
	}

	fork := rx.ForkingSingleDialogue

	if job.early && job.tag != "" {
		cr.early[job.tag] = true
		cr.forked = cr.forked || len(cr.early) > 1

		if cr.forked {
			fork = rx.ForkingSeveralDialogues
		}
	}

	if cr.service == "" && c.ue == proxy.Caller {
		cr.service = icsiRef(job.headers.Values("Feature-Caps"))
	}

	service := cr.service

	cr.mu.Unlock()

	components, err := mediaComponents(*job.x, flows)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended {
		return errCallEnded
	}

	initial := !s.opened
	kind := rx.RequestUpdate

	r := rx.AARequest{
		AFApplicationIdentifier: service,
		MediaComponents:         components,
		SubscriptionIDs:         subscriptionIDs(cr.identities),
		SIPForkingIndication:    fork,
		RequestType:             &kind,
	}

	if initial {
		kind = rx.RequestInitial
		r.SpecificActions = []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer}
	}

	if cr.key.ue.Is4() {
		r.FramedIPAddress = cr.key.ue
	} else {
		r.FramedIPv6Address = cr.key.ue
	}

	a, err := p.rx.callAAR(s, r)
	if err != nil {
		if initial {
			s.ended = true
			p.rx.forget(s)

			cr.mu.Lock()
			if cr.session == s {
				cr.session = nil
			}
			cr.mu.Unlock()
		}

		return err
	}

	s.opened = true
	if len(a.Class) > 0 {
		s.class = a.Class
	}

	cr.mu.Lock()

	if job.seq != 0 {
		cr.done[job.seq] = true
	}

	if job.final {
		cr.forked = false
	}

	cr.flows = flows
	cr.active = activeComponents(components)

	if info := chargingInfo(a, flows); info != "" {
		cr.charging = info
	}

	cr.mu.Unlock()

	p.log.Info("media authorized", slog.String("dialog", d.ID()), slog.String("impi", cr.key.impi),
		slog.String("ue", cr.key.ue.String()), slog.String("session", s.id), slog.String("request", kind.String()),
		slog.String("forking", fork.String()))

	return nil
}

func activeComponents(components []rx.MediaComponent) map[uint32]bool {
	out := make(map[uint32]bool, len(components))

	for _, c := range components {
		out[c.Number] = c.FlowStatus == nil || *c.FlowStatus != rx.FlowStatusRemoved
	}

	return out
}

// TS 29.213 Annex B.4.1
func (p *PCSCF) callEnded(c *call) {
	cr := c.rx
	if cr == nil {
		return
	}

	cr.mu.Lock()

	cr.ended = true
	s := cr.session

	if cr.loss != nil {
		cr.loss.Stop()
		cr.loss = nil
	}

	cr.mu.Unlock()

	if s != nil {
		p.rx.end(s, rx.TerminationLogout, 0)
	}
}

func (c *rxClient) callSession(cr *callRx) *rxSession {
	s := &rxSession{id: c.cfg.Diameter.NewSessionID(), key: cr.key, call: cr}

	c.mu.Lock()
	c.sessions[s.id] = s
	c.mu.Unlock()

	return s
}

type retryHold struct {
	until  time.Time
	digest string
}

// TS 29.214 §4.4.1
func (c *rxClient) callAAR(s *rxSession, r rx.AARequest) (rx.AAAnswer, error) {
	b, _ := json.Marshal(r.MediaComponents)
	digest := string(b)

	c.mu.Lock()
	hold, held := c.retry[s.key.ue]
	c.mu.Unlock()

	if held && hold.digest == digest && time.Now().Before(hold.until) {
		return rx.AAAnswer{}, errRetryInterval
	}

	req, err := rx.NewAARequest(c.envelope(s.id), r)
	if err != nil {
		return rx.AAAnswer{}, err
	}

	ctx, cancel := context.WithTimeout(c.ctx, c.cfg.CallTimeout)
	defer cancel()

	ans, err := c.cfg.Diameter.Do(ctx, c.cfg.PCRF.ID, req)
	if err != nil {
		return rx.AAAnswer{}, err
	}

	a, err := rx.ParseAAAnswer(ans)

	var refused *rx.AAError
	if errors.As(err, &refused) && refused.Code == tgpp.ResultRequestedServiceTemporarilyNotAuthorized &&
		refused.RetryInterval > 0 {
		c.mu.Lock()
		c.retry[s.key.ue] = retryHold{until: time.Now().Add(refused.RetryInterval), digest: digest}
		c.mu.Unlock()
	}

	return a, err
}

// TS 24.229 §7.2A.5.2.7, TS 29.214 §5.3.3, Annex B
func chargingInfo(a rx.AAAnswer, flows map[int]flowNumbers) string {
	if len(a.AccessNetworkChargingIdentifiers) == 0 || !a.AccessNetworkChargingAddress.IsValid() {
		return ""
	}

	if t := a.AccessNetwork.IPCANType; t != nil && *t != rx.IPCAN3GPPEPS {
		return ""
	}

	items := make([]string, 0, len(a.AccessNetworkChargingIdentifiers))

	for i, id := range a.AccessNetworkChargingIdentifiers {
		item := "eps-item=" + strconv.Itoa(i+1) + ";eps-sig=no;ecid=" + strings.ToUpper(hex.EncodeToString(id.Value))

		if ids := flowIDs(id.Flows, flows); ids != "" {
			item += ";flow-id=" + ids
		}

		items = append(items, item)
	}

	return "pdngw=" + sip.FormatHost(a.AccessNetworkChargingAddress.Unmap()) + `;eps-info="` + strings.Join(items, ",") + `"`
}

func flowIDs(fs []rx.Flows, numbers map[int]flowNumbers) string {
	var tuples []string

	for _, f := range fs {
		ns := f.FlowNumbers

		if len(ns) == 0 {
			n := numbers[int(f.MediaComponentNumber)-1]

			for _, v := range []uint32{n.rtp, n.rtcp} {
				if v != 0 {
					ns = append(ns, v)
				}
			}

			slices.Sort(ns)
		}

		for _, n := range ns {
			tuples = append(tuples, "{"+strconv.FormatUint(uint64(f.MediaComponentNumber), 10)+","+strconv.FormatUint(uint64(n), 10)+"}")
		}
	}

	if len(tuples) == 0 {
		return ""
	}

	return "(" + strings.Join(tuples, ",") + ")"
}

// TS 24.229 §5.2.7.2, §5.2.7.3
func (cr *callRx) takeCharging() string {
	if cr == nil {
		return ""
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	info := cr.charging
	cr.charging = ""

	return info
}

func chargeResponse(c *call, res *sip.Response) {
	info := c.rx.takeCharging()
	if info == "" {
		return
	}

	cv, ok := parseChargingVector(res.Header.Get("P-Charging-Vector"))
	if !ok {
		return
	}

	cv.access = info
	cv.setResponse(res)
}

// TS 29.214 §4.4.6.2, TS 24.229 §5.2.8.1
func (p *PCSCF) mediaLost(s *rxSession, r rx.ReAuthRequest) {
	cr := s.call

	lost := slices.ContainsFunc(r.SpecificActions, func(a rx.SpecificAction) bool {
		return a == rx.ActionIndicationOfLossOfBearer || a == rx.ActionIndicationOfReleaseOfBearer
	})
	if !lost {
		return
	}

	var components []uint32
	for _, f := range r.Flows {
		components = append(components, f.MediaComponentNumber)
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	if cr.ended || cr.loss != nil {
		return
	}

	cr.lost = components
	cr.loss = p.clock.AfterFunc(p.rx.cfg.MediaLossTimeout, func() { p.mediaLossExpired(cr) })
}

func (p *PCSCF) mediaLossExpired(cr *callRx) {
	cr.mu.Lock()

	cr.loss = nil
	still := false

	if len(cr.lost) == 0 {
		for _, on := range cr.active {
			still = still || on
		}
	}

	for _, n := range cr.lost {
		still = still || cr.active[n]
	}

	release := still && !cr.ended && cr.dialog != nil

	cr.mu.Unlock()

	if !release {
		return
	}

	p.log.Info("media bearer lost: releasing the call", slog.String("dialog", cr.dialog.ID()), slog.String("impi", cr.key.impi),
		slog.String("ue", cr.key.ue.String()))

	p.releaseCall(cr.call, cr.dialog, false)
}

// TS 24.229 §5.2.8.1.1, §5.2.8.1.2
func (p *PCSCF) releaseCall(c *call, d *proxy.Dialog, signalling bool) {
	cause, _ := sip.NewReason("SIP", 503, "")

	var r proxy.Release

	switch d.State() {
	case proxy.Early:
		switch {
		case !signalling:
			r = proxy.Release{Toward: proxy.Both, Code: 500, Reason: []sip.Reason{cause}}
		case c.ue == proxy.Caller:
			r = proxy.Release{Toward: proxy.Callee, Reason: []sip.Reason{cause}}
		default:
			r = proxy.Release{Toward: proxy.Caller, Code: 500}
		}
	case proxy.Answered, proxy.Confirmed:
		r = proxy.Release{Toward: other(c.ue), Reason: []sip.Reason{cause}}
	default:
		return
	}

	if err := d.Release(r); err != nil {
		p.log.Debug("releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
	}
}

// TS 29.214 §5.4, RFC 4006 §8.46
func subscriptionIDs(identities []string) []rx.SubscriptionID {
	var out []rx.SubscriptionID

	for _, id := range identities {
		u, err := sip.ParseURI(id)
		if err != nil {
			continue
		}

		s := rx.SubscriptionID{Type: rx.SubscriptionIDSIPURI, Data: id}
		if t := telForm(u); t.IsTel() {
			s = rx.SubscriptionID{Type: rx.SubscriptionIDE164, Data: strings.TrimPrefix(t.User, "+")}
		}

		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}

	return out
}

// TS 29.214 Annex A.1 NOTE 3, RFC 6809
func icsiRef(values []string) string {
	for _, v := range values {
		for _, e := range sip.SplitList(v) {
			for part := range strings.SplitSeq(e, ";") {
				name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "+g.3gpp.icsi-ref") {
					continue
				}

				value = strings.Trim(strings.TrimSpace(value), `"`)
				if s, err := url.PathUnescape(value); err == nil {
					value = s
				}

				if first, _, _ := strings.Cut(value, ","); first != "" {
					return first
				}
			}
		}
	}

	return ""
}
