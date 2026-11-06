package b2bua

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type RelayConfig struct {
	Headers Headers

	Media Media

	AfterPrack bool

	TimerC time.Duration

	FailoverBudget time.Duration

	Request func(from, to *Leg, req *sip.Request, c *ua.Change)

	Response func(from, to *Leg, res *sip.Response, rp *ua.Reply)

	RelayRefresh func(from *Leg, r *ua.Request) bool

	Final func(res *sip.Response, err error) ua.Reply

	Failover func(ctx context.Context, failed *Leg, f Failure) *sip.Request
}

// RFC 7092 section 3.1
type Relay struct {
	cfg  RelayConfig
	a, b *Leg

	pairs map[*ua.Session]*pair

	open []*Exchange

	initial    *Exchange
	sdp        []byte
	opts       []ua.Option
	rel100     bool
	final      bool
	progressed bool
	failCode   int
	failures   []Failure
	sticky     sip.Header
	timerC     *ua.Timer
	budget     *ua.Timer
	exhausted  bool
}

type pair struct {
	in       *ua.Request
	a, b     *ua.Session
	answered bool
	answer   []byte
	offer    *ua.Offer
	x        *Exchange
	relayed  map[*sip.Response]bool
	op       *operation
	refresh  *refresh
}

type refresh struct {
	leg  *Leg
	prev []byte
}

func (pr *pair) other(p *Relay, from *Leg) *ua.Session {
	if from == p.a {
		return pr.b
	}

	return pr.a
}

type operation struct {
	from      *Leg
	r         *ua.Request
	x         *Exchange
	fetch     bool
	offer     *ua.Offer
	cancelled bool
	join      func(error)
}

func Bridge(ctx context.Context, c *Call, r *ua.Request, out *sip.Request, cfg RelayConfig, opts ...ua.Option) (*Relay, error) {
	c.check()

	a, err := c.Incoming(r, LegHandler{})
	if err != nil {
		_ = r.Reject(500)
		return nil, err
	}

	b, err := c.leg(LegHandler{})
	if err != nil {
		_ = r.Reject(500)
		return nil, err
	}

	p := &Relay{cfg: cfg, a: a, b: b, pairs: make(map[*ua.Session]*pair)}
	a.relay, b.relay = p, p

	in := r.Message()
	p.rel100 = in.Header.HasOptionTag("Supported", sip.OptionTag100rel) ||
		in.Header.HasOptionTag("Require", sip.OptionTag100rel)

	for _, name := range cfg.Headers.sticky() {
		for _, v := range in.Header.Values(name) {
			p.sticky.Add(name, v)
		}
	}

	sdp, offered := r.Offer()

	if offered {
		p.initial = &Exchange{From: a, To: b, Method: "INVITE", Initial: true, Offer: sdp}

		if p.sdp, err = p.begin(p.initial); err != nil {
			b.removed(ctx)

			_ = r.Respond(refusal(err))

			return p, err
		}
	}

	var without []string

	if !p.rel100 {
		without = append(without, sip.OptionTag100rel)
	}

	if !in.Header.HasOptionTag("Supported", sip.OptionTag199) && !in.Header.HasOptionTag("Require", sip.OptionTag199) {
		without = append(without, sip.OptionTag199)
	}

	if len(without) > 0 {
		opts = append([]ua.Option{ua.WithoutOptionTags(without...)}, opts...)
	}

	p.opts = opts

	if cfg.FailoverBudget > 0 {
		p.budget = c.st.AfterFunc(cfg.FailoverBudget, func() { p.exhausted = true })
	}

	if err := p.dial(ctx, b, out); err != nil {
		if p.failover(ctx, b, Failure{Err: err}) {
			return p, nil
		}

		p.giveUp(ctx)

		return p, p.failures[len(p.failures)-1].Err
	}

	p.armTimerC(ctx)

	return p, nil
}

func (p *Relay) dial(ctx context.Context, l *Leg, out *sip.Request) error {
	in := p.a.in.Message()

	if p.initial != nil && p.initial.To != l {
		p.end(p.initial, nil)
		p.initial = &Exchange{From: p.a, To: l, Method: "INVITE", Initial: true, Offer: p.initial.Offer}

		sdp, err := p.begin(p.initial)
		if err != nil {
			l.state = ua.Terminated
			l.removed(ctx)

			return &mediaError{err}
		}

		p.sdp = sdp
	}

	p.cfg.Headers.copyHeader(&out.Header, p.a, l, in)
	p.mapDialogs(in, &out.Header)

	own, _ := out.Content().Parts()
	out.SetBodies(p.cfg.Headers.bodies(p.a, l, in, own, p.sdp)...)

	return l.dial(ctx, out, p.opts...)
}

// RFC 3261 section 16.6
// RFC 3263 section 4.3
func (p *Relay) failover(ctx context.Context, l *Leg, f Failure) bool {
	p.failures = append(p.failures, f)

	for p.retryable() && (f.Response == nil || f.Response.StatusCode < 600) {
		out := p.cfg.Failover(ctx, l, f)
		if out == nil || !p.retryable() {
			return false
		}

		next, err := p.a.call.leg(l.h)
		if err != nil {
			return false
		}

		next.value, next.relay = l.value, p
		l.relay, p.b = nil, next

		if err := p.dial(ctx, next, out); err != nil {
			if me := (*mediaError)(nil); errors.As(err, &me) {
				p.respond(ctx, nil, refusal(me.err))
				return true
			}

			l, f = next, Failure{Err: err}
			p.failures = append(p.failures, f)

			continue
		}

		p.armTimerC(ctx)

		return true
	}

	return false
}

func (p *Relay) retryable() bool {
	return p.cfg.Failover != nil && !p.final && !p.progressed && !p.exhausted && p.failCode == 0 && p.a.state == ua.Early
}

// RFC 3261 section 16.7
func (p *Relay) giveUp(ctx context.Context) {
	f := chosen(p.failures)

	final := p.cfg.Final
	if final == nil {
		final = FinalFor
	}

	rp := final(f.Response, f.Err)
	if f.Response != nil && rp.Code == f.Response.StatusCode {
		rp = p.reply(p.b, f.Response, rp.Code, nil)
	}

	p.respond(ctx, nil, rp)
}

// TS 24.379 clause 10.1.1.3.1.2
func NewRelay(a, b *Leg, cfg RelayConfig) (*Relay, error) {
	a.call.check()

	switch {
	case a.call.st != b.call.st:
		return nil, ErrStrand
	case a.state != ua.Confirmed || b.state != ua.Confirmed:
		return nil, ErrState
	}

	p := &Relay{cfg: cfg, a: a, b: b, final: true, pairs: make(map[*ua.Session]*pair)}
	pr := &pair{a: a.s, b: b.s}
	p.pairs[a.s], p.pairs[b.s] = pr, pr

	for _, l := range []*Leg{a, b} {
		if l.relay != nil {
			l.relay.Stop()
		}

		l.relay = p
	}

	return p, nil
}

// RFC 3725 section 4.2
func Join(ctx context.Context, a, b *Leg, cfg RelayConfig, done func(error)) (*Relay, error) {
	p, err := NewRelay(a, b, cfg)
	if err != nil {
		return nil, err
	}

	if done == nil {
		done = func(error) {}
	}

	pr := p.pairs[a.s]
	op := &operation{from: b, fetch: true, join: done}
	pr.op = op

	err = a.s.Reinvite(ctx, ua.Change{}, func(res *sip.Response, err error) {
		if res != nil && res.IsSuccess() && op.offer != nil {
			return
		}

		if pr.op == op {
			pr.op = nil
		}

		if err == nil {
			err = &joinError{res}
		}

		done(err)
	})
	if err != nil {
		pr.op = nil
		return p, err
	}

	return p, nil
}

type joinError struct{ res *sip.Response }

func (e *joinError) Error() string {
	if e.res == nil {
		return "sip/b2bua: join failed"
	}

	return "sip/b2bua: join failed: " + e.res.StartLine()
}

func (p *Relay) A() *Leg { return p.a }
func (p *Relay) B() *Leg { return p.b }

func (p *Relay) Paired(s *ua.Session) *ua.Session {
	p.a.call.check()

	pr := p.pairs[s]
	if pr == nil {
		return nil
	}

	if s == pr.a {
		return pr.b
	}

	return pr.a
}

func (p *Relay) Stop() {
	p.a.call.check()

	for _, l := range []*Leg{p.a, p.b} {
		if l.relay == p {
			l.relay = nil
		}
	}

	p.stopTimers()

	p.endAll()
}

func (p *Relay) stopTimers() {
	if p.timerC != nil {
		p.timerC.Stop()
	}

	if p.budget != nil {
		p.budget.Stop()
	}
}

func (p *Relay) other(l *Leg) *Leg {
	if l == p.a {
		return p.b
	}

	return p.a
}

func (p *Relay) report(err error) {
	p.a.call.report(err)
}

func (p *Relay) reply(from *Leg, res *sip.Response, code int, sdp []byte) ua.Reply {
	to := p.other(from)
	rp := ua.Reply{Code: code, Bodies: p.cfg.Headers.bodies(from, to, res, nil, sdp)}

	if code == res.StatusCode {
		rp.Reason = res.Reason
	}

	p.cfg.Headers.copyHeader(&rp.Header, from, to, res)

	if f := p.cfg.Response; f != nil {
		f(from, to, res, &rp)
	}

	return rp
}

func (p *Relay) change(from *Leg, req *sip.Request, sdp []byte) ua.Change {
	to := p.other(from)
	c := ua.Change{Bodies: p.cfg.Headers.bodies(from, to, req, nil, sdp)}
	p.cfg.Headers.copyHeader(&c.Header, from, to, req)

	if f := p.cfg.Request; f != nil {
		f(from, to, req, &c)
	}

	return c
}

func (p *Relay) decorate(l *Leg, m sip.Message) {
	req, ok := m.(*sip.Request)
	if !ok || l != p.b {
		return
	}

	for _, f := range p.sticky {
		if !req.Header.Has(f.Name) {
			req.Header.Add(f.Name, f.Value)
		}
	}
}

// RFC 3891 section 3
// RFC 4538 section 3
func (p *Relay) mapDialogs(in *sip.Request, h *sip.Header) {
	a := p.a.call.a

	paired := func(id dialog.ID) (dialog.ID, bool) {
		s := a.Session(id)
		if s == nil {
			return dialog.ID{}, false
		}

		l := LegOf(s)
		if l == nil || l.relay == nil || l.call.st != p.a.call.st {
			return dialog.ID{}, false
		}

		o := l.relay.Paired(s)
		if o == nil {
			return dialog.ID{}, false
		}

		return o.ID(), true
	}

	if rep, ok, err := in.Header.Replaces(); ok && err == nil {
		if id, ok := paired(dialog.ReplacesID(rep)); ok {
			h.Set("Replaces", sip.Replaces{CallID: id.CallID, ToTag: id.RemoteTag, FromTag: id.LocalTag, EarlyOnly: rep.EarlyOnly}.String())

			if in.Header.HasOptionTag("Require", "replaces") {
				h.Add("Require", "replaces")
			}
		}
	}

	if td, ok, err := in.Header.TargetDialog(); ok && err == nil {
		if id, ok := paired(dialog.TargetID(td)); ok {
			h.Set("Target-Dialog", sip.TargetDialog{CallID: id.CallID, LocalTag: id.RemoteTag, RemoteTag: id.LocalTag}.String())

			if in.Header.HasOptionTag("Require", "tdialog") {
				h.Add("Require", "tdialog")
			}
		}
	}
}

func (p *Relay) armTimerC(ctx context.Context) {
	if p.cfg.TimerC <= 0 || p.final {
		return
	}

	if p.timerC != nil {
		p.timerC.Stop()
	}

	p.timerC = p.a.call.st.AfterFunc(p.cfg.TimerC, func() {
		if p.final {
			return
		}

		p.report(p.b.End(ctx, ua.Reply{}))
		p.respond(ctx, nil, ua.Reply{Code: 408})
	})
}

func (p *Relay) mirrorOf(s *ua.Session) *pair {
	if pr := p.pairs[s]; pr != nil {
		return pr
	}

	in := p.a.in

	if len(p.pairs) > 0 {
		f, err := p.a.Fork()
		if err != nil {
			return nil
		}

		in = f
	}

	pr := &pair{in: in, a: in.Session(), b: s, relayed: make(map[*sip.Response]bool)}
	p.pairs[s], p.pairs[pr.a] = pr, pr

	return pr
}

func (p *Relay) progress(ctx context.Context, l *Leg, s *ua.Session, res *sip.Response) {
	if l != p.b || p.final || s == nil || res.StatusCode <= 100 || res.StatusCode == 199 {
		return
	}

	p.armTimerC(ctx)

	pr := p.mirrorOf(s)
	if pr == nil || pr.relayed[res] {
		return
	}

	reliable := p.rel100 && res.Header.HasOptionTag("Require", sip.OptionTag100rel)

	var sdp []byte

	if body := sessionSDP(res.Envelope); body != nil && p.initial != nil && !pr.answered {
		answer, err := p.answerOf(p.initial, pr, body)
		if err != nil {
			p.abandon(ctx, refusal(err))
			return
		}

		sdp = answer
		pr.answer = answer
		pr.answered = reliable
	}

	rp := p.reply(l, res, res.StatusCode, sdp)
	rp.Reliable = reliable

	p.progressed = true
	p.report(pr.in.Progress(rp))
}

func (p *Relay) offer(ctx context.Context, l *Leg, s *ua.Session, o *ua.Offer) bool {
	if pr := p.pairs[s]; pr != nil && pr.op != nil && pr.op.fetch && l != pr.op.from {
		p.fetched(ctx, l, pr, o)
		return true
	}

	if l != p.b || p.final || p.initial != nil {
		return false
	}

	pr := p.mirrorOf(s)
	if pr == nil {
		o.Reject(ctx)
		return true
	}

	res := o.Response()

	if res.IsProvisional() && !p.rel100 {
		o.Reject(ctx)

		p.failCode = 488

		return true
	}

	x := &Exchange{From: p.b, To: p.a, Method: "INVITE", Initial: true, Offer: o.SDP(), Dialog: pr.b}

	sdp, err := p.begin(x)
	if err != nil {
		o.Reject(ctx)
		p.abandon(ctx, refusal(err))

		return true
	}

	pr.offer, pr.x = o, x

	if res.IsProvisional() {
		pr.relayed[res] = true

		rp := p.reply(l, res, res.StatusCode, sdp)
		rp.Reliable = true

		p.progressed = true
		p.report(pr.in.Progress(rp))

		return true
	}

	p.respond(ctx, pr, p.reply(l, res, res.StatusCode, sdp))

	return true
}

func (p *Relay) fetched(ctx context.Context, l *Leg, pr *pair, o *ua.Offer) {
	op := pr.op
	to := p.other(l)
	op.x = &Exchange{From: l, To: to, Method: "INVITE", Offer: o.SDP(), Dialog: pr.b}

	sdp, err := p.begin(op.x)
	if err != nil {
		o.Reject(ctx)

		pr.op = nil

		if op.join != nil {
			op.join(err)
		} else {
			p.report(op.r.Respond(refusal(err)))
		}

		return
	}

	op.offer = o

	if op.join == nil {
		p.report(op.r.Respond(p.reply(l, o.Response(), 200, sdp)))
		return
	}

	target := pr.other(p, l)

	err = target.Reinvite(ctx, ua.Change{Bodies: []sip.BodyPart{ua.SDP(sdp)}}, func(res *sip.Response, err error) {
		pr.op = nil

		var body []byte
		if res != nil && res.IsSuccess() {
			body = sessionSDP(res.Envelope)
		}

		if body == nil {
			o.Reject(ctx)
			p.end(op.x, nil)

			if err == nil {
				err = &joinError{res}
			}

			op.join(err)

			return
		}

		answer, err := p.answerOf(op.x, pr, body)
		if err == nil {
			err = o.Answer(ctx, ua.Change{Bodies: []sip.BodyPart{ua.SDP(answer)}})
		}

		if err != nil {
			o.Reject(ctx)
			p.end(op.x, nil)
		} else {
			p.end(op.x, pr)
		}

		op.join(err)
	})
	if err != nil {
		pr.op = nil

		o.Reject(ctx)
		p.end(op.x, nil)
		op.join(err)
	}
}

func (p *Relay) answer(ctx context.Context, l *Leg, s *ua.Session, sdp []byte) {
	pr := p.pairs[s]
	if pr == nil {
		return
	}

	var (
		o *ua.Offer
		x *Exchange
	)

	switch {
	case pr.op != nil && pr.op.offer != nil && l == pr.op.from && pr.op.join == nil:
		o, x, pr.op = pr.op.offer, pr.op.x, nil
	case l == p.a && pr.offer != nil:
		o, x, pr.offer = pr.offer, pr.x, nil
	case pr.refresh != nil && pr.refresh.leg == l:
		prev := pr.refresh.prev
		pr.refresh = nil

		if !bytes.Equal(prev, sdp) {
			p.propagate(ctx, l, pr, sdp)
		}

		return
	default:
		return
	}

	answer, err := p.answerOf(x, pr, sdp)
	if err == nil {
		err = o.Answer(ctx, ua.Change{Bodies: []sip.BodyPart{ua.SDP(answer)}})
	}

	if err != nil {
		p.report(err)
		o.Reject(ctx)
		p.end(x, nil)

		return
	}

	p.end(x, pr)
}

// RFC 3264 section 8
func (p *Relay) propagate(ctx context.Context, l *Leg, pr *pair, sdp []byte) {
	to := pr.other(p, l)
	x := &Exchange{From: l, To: p.other(l), Method: "INVITE", Offer: sdp, Dialog: pr.b}

	p.report(to.Modify(ctx, ua.Modification{Method: "INVITE", Build: func() (ua.Change, error) {
		offer, err := p.begin(x)
		if err != nil {
			return ua.Change{}, err
		}

		return ua.Change{Bodies: []sip.BodyPart{ua.SDP(offer)}}, nil
	}}, func(res *sip.Response, err error) {
		if res == nil || !res.IsSuccess() {
			p.end(x, nil)
			return
		}

		if body := sessionSDP(res.Envelope); body != nil {
			_, _ = p.answerOf(x, pr, body)
		}

		p.end(x, pr)
	}))
}

func (p *Relay) established(ctx context.Context, l *Leg, msg sip.Message) {
	if l != p.b {
		return
	}

	res, _ := msg.(*sip.Response)
	if res == nil {
		return
	}

	pr := p.mirrorOf(p.b.s)

	if p.final || p.a.state == ua.Terminated || pr == nil {
		p.report(p.b.End(ctx, ua.Reply{}))
		return
	}

	if p.initial == nil && sessionSDP(res.Envelope) != nil {
		return
	}

	var sdp []byte

	if p.initial != nil && !pr.answered {
		sdp = pr.answer

		if body := sessionSDP(res.Envelope); body != nil {
			answer, err := p.answerOf(p.initial, pr, body)
			if err != nil {
				p.abandon(ctx, refusal(err))
				return
			}

			sdp = answer
		}
	}

	p.respond(ctx, pr, p.reply(l, res, res.StatusCode, sdp))
}

func (p *Relay) respond(ctx context.Context, pr *pair, rp ua.Reply) {
	if p.final {
		return
	}

	p.final = true

	p.stopTimers()

	if rp.Code >= 300 || pr == nil {
		p.end(p.initial, nil)
		p.report(p.a.Respond(rp))

		return
	}

	rp.AfterPrack = p.cfg.AfterPrack

	if err := pr.in.Respond(rp); err != nil {
		p.report(err)
		p.end(p.initial, nil)
		p.report(p.a.Respond(ua.Reply{Code: 500}))
		p.report(p.b.End(ctx, ua.Reply{}))

		return
	}

	p.end(p.initial, pr)
	p.dialog(pr, ua.Confirmed)
}

func (p *Relay) failed(ctx context.Context, l *Leg, res *sip.Response, err error) {
	if l != p.b || p.final {
		return
	}

	if p.failCode != 0 {
		p.respond(ctx, nil, ua.Reply{Code: p.failCode})
		return
	}

	if !p.failover(ctx, l, Failure{Response: res, Err: err}) {
		p.giveUp(ctx)
	}
}

func (p *Relay) abandon(ctx context.Context, rp ua.Reply) {
	p.respond(ctx, nil, rp)
	p.report(p.b.End(ctx, ua.Reply{}))
}

// RFC 3261 section 15
func (p *Relay) earlyEnded(ctx context.Context, l *Leg, s *ua.Session, e ua.End) {
	pr := p.pairs[s]
	if pr == nil || p.final {
		return
	}

	delete(p.pairs, pr.a)
	delete(p.pairs, pr.b)

	if pr.x != nil {
		pr.offer = nil
		p.end(pr.x, nil)
	}

	p.dialog(pr, ua.Terminated)

	c := ua.Reasons(e.Reasons()...)

	if other := pr.other(p, l); other != nil && other.State() == ua.Early {
		p.report(other.Bye(ctx, c, nil))
	}
}

// RFC 3261 section 16.7
func FinalFor(res *sip.Response, err error) ua.Reply {
	switch {
	case res != nil:
		return ua.Reply{Code: relayable(res.StatusCode)}
	case errors.Is(err, transaction.ErrTimeout):
		return ua.Reply{Code: 408}
	case errors.Is(err, ua.ErrClosed), errors.Is(err, transaction.ErrClosed):
		return ua.Reply{Code: 503}
	}

	return ua.Reply{Code: 500}
}

func (p *Relay) ended(ctx context.Context, l *Leg, e ua.End) {
	o := p.other(l)

	p.stopTimers()

	p.endAll()

	if !p.final {
		p.final = true

		if l == p.b {
			p.report(p.a.Respond(ua.Reply{Code: 480}))
			return
		}
	}

	if o.state != ua.Terminated {
		p.report(o.End(ctx, ua.Reply{Header: ua.Reasons(e.Reasons()...).Header}))
	}
}

// TS 24.379 clause 6.3.2.1.6
func (p *Relay) Forward(ctx context.Context, from *Leg, r *ua.Request) {
	p.a.call.check()

	req := r.Message()

	if req.Method == "BYE" {
		p.bye(ctx, from, r)
		return
	}

	var to *ua.Session

	pr := p.pairs[r.Session()]
	if pr != nil {
		to = pr.other(p, from)
	}

	if to == nil || to.State() == ua.Terminated {
		p.report(r.Reject(500, sip.Field{Name: "Retry-After", Value: "1"}))
		return
	}

	switch req.Method {
	case "INVITE", "UPDATE", "PRACK":
		if req.Method != "PRACK" && r.Refresh() && !p.relayRefresh(from, r) {
			p.answerRefresh(from, r, pr)
			return
		}

		if _, offered := r.Offer(); req.Method == "PRACK" && !offered {
			p.report(r.Respond(ua.Reply{Code: 200}))
			return
		}

		p.modify(ctx, from, r, pr, to)
	case "REFER", "SUBSCRIBE", "NOTIFY":
		p.report(r.Reject(501))
	default:
		err := to.Send(ctx, req.Method, p.change(from, req, nil), func(res *sip.Response, err error) {
			if res == nil {
				p.report(r.Respond(FinalFor(nil, err)))
				return
			}

			p.report(r.Respond(p.reply(p.other(from), res, res.StatusCode, sessionSDP(res.Envelope))))
		})
		if err != nil {
			p.report(r.Reject(500))
		}
	}
}

func (p *Relay) answerRefresh(from *Leg, r *ua.Request, pr *pair) {
	if _, offered := r.Offer(); !offered && r.Message().Method == "INVITE" {
		if _, remote, ok := r.Session().Negotiated(); ok {
			pr.refresh = &refresh{leg: from, prev: remote}
		}
	}

	p.report(r.AnswerUnchanged(ua.Change{}))
}

func (p *Relay) relayRefresh(l *Leg, r *ua.Request) bool {
	if f := p.cfg.RelayRefresh; f != nil {
		return f(l, r)
	}

	parts, _ := r.Message().Content().Split()

	return slices.ContainsFunc(parts, func(part sip.BodyPart) bool { return !part.IsSession() })
}

// TS 24.379 clause 6.3.2.1.6
func (p *Relay) bye(ctx context.Context, l *Leg, r *ua.Request) {
	respond := func(*sip.Response, error) { p.report(r.Respond(ua.Reply{Code: 200})) }

	if l.state == ua.Early {
		respond(nil, nil)
		return
	}

	o := p.other(l)
	c := p.change(l, r.Message(), nil)

	switch s := o.Session(); {
	case o.state == ua.Terminated:
		respond(nil, nil)
	case s != nil && s.State() == ua.Confirmed:
		if err := s.Bye(ctx, c, respond); err != nil {
			respond(nil, err)
		}
	default:
		respond(nil, nil)
		p.report(o.End(ctx, ua.Reply{Header: c.Header}))
	}
}

func (p *Relay) cancelled(ctx context.Context, l *Leg, r *ua.Request, cancel *sip.Request) {
	pr := p.pairs[r.Session()]
	if pr == nil || pr.op == nil || pr.op.r != r {
		return
	}

	pr.op.cancelled = true

	if s := pr.other(p, l); s != nil {
		p.report(s.CancelReinvite(ctx, p.change(l, cancel, nil)))
	}
}

// RFC 3261 section 14.1
func (p *Relay) modify(ctx context.Context, from *Leg, r *ua.Request, pr *pair, to *ua.Session) {
	req := r.Message()
	offer, offered := r.Offer()

	if pr.op != nil && (offered || req.Method == "INVITE") {
		p.report(r.Reject(491))
		return
	}

	o := p.other(from)
	op := &operation{from: from, r: r, fetch: !offered && req.Method == "INVITE"}

	var sdp []byte

	if offered {
		op.x = &Exchange{From: from, To: o, Method: req.Method, Offer: offer, Dialog: pr.b}

		var err error
		if sdp, err = p.begin(op.x); err != nil {
			p.report(r.Respond(refusal(err)))
			return
		}
	}

	if offered || op.fetch {
		pr.op = op
	}

	done := func(res *sip.Response, err error) {
		if pr.op == op && (res == nil || !res.IsSuccess() || !op.fetch || op.cancelled) {
			pr.op = nil
		}

		switch {
		case op.cancelled:
			p.end(op.x, nil)

			if res != nil && res.IsSuccess() && offered {
				p.restore(ctx, from, o, to, pr)
			}
		case res == nil && (errors.Is(err, ua.ErrPending) || errors.Is(err, ua.ErrStale) || errors.Is(err, ua.ErrCancelled)):
			p.end(op.x, nil)
			p.report(r.Reject(491))
		case res == nil:
			p.end(op.x, nil)
			p.report(r.Respond(FinalFor(nil, err)))
		case !res.IsSuccess():
			p.end(op.x, nil)
			p.report(r.Respond(p.reply(o, res, res.StatusCode, nil)))
		case op.fetch && op.offer == nil:
			pr.op = nil

			p.report(r.Reject(500))
		case op.fetch:
		default:
			var answer []byte

			if body := sessionSDP(res.Envelope); body != nil && op.x != nil {
				a, err := p.answerOf(op.x, pr, body)
				if err != nil {
					p.end(op.x, nil)
					p.report(r.Respond(refusal(err)))
					p.restore(ctx, from, o, to, pr)

					return
				}

				answer = a
			}

			if err := r.Respond(p.reply(o, res, res.StatusCode, answer)); err != nil {
				p.report(err)
				p.end(op.x, nil)
				p.restore(ctx, from, o, to, pr)

				return
			}

			p.end(op.x, pr)
		}
	}

	c := p.change(from, req, sdp)

	var err error
	if req.Method == "INVITE" {
		err = to.Reinvite(ctx, c, done)
	} else {
		err = to.Update(ctx, c, done)
	}

	if err != nil {
		if pr.op == op {
			pr.op = nil
		}

		p.end(op.x, nil)

		if errors.Is(err, ua.ErrPending) || errors.Is(err, ua.ErrState) {
			p.report(r.Reject(491))
		} else {
			p.report(r.Reject(500))
		}
	}
}

// RFC 3264 section 8
func (p *Relay) restore(ctx context.Context, from, o *Leg, to *ua.Session, pr *pair) {
	s := from.Session()
	if s == nil {
		return
	}

	_, remote, ok := s.Negotiated()
	if !ok {
		return
	}

	x := &Exchange{From: from, To: o, Method: "INVITE", Offer: remote, Dialog: pr.b}

	err := to.Modify(ctx, ua.Modification{Method: "INVITE", Build: func() (ua.Change, error) {
		sdp, err := p.begin(x)
		if err != nil {
			return ua.Change{}, err
		}

		return ua.Change{Bodies: []sip.BodyPart{ua.SDP(sdp)}}, nil
	}}, func(res *sip.Response, err error) {
		if res == nil || !res.IsSuccess() {
			p.end(x, nil)
			p.report(err)
			p.a.call.End(ctx, ua.Reply{})

			return
		}

		if body := sessionSDP(res.Envelope); body != nil {
			_, _ = p.answerOf(x, pr, body)
		}

		p.end(x, pr)
	})
	p.report(err)
}
