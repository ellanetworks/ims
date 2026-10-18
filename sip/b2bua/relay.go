package b2bua

import (
	"bytes"
	"errors"
	"slices"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// RelayConfig configures a relay.
type RelayConfig struct {
	// Headers is what crosses from one leg to the other: all but what belongs to one leg without it.
	Headers Headers

	// Media, when set, makes the session descriptions of each leg from those of the other, exchange by exchange;
	// they cross unchanged without it.
	Media Media

	// AfterPrack holds the 2xx to the initial INVITE of the incoming leg until the PRACK of its last reliable 1xx
	// (ua.Reply.AfterPrack, TS 24.379 §6.3.2.2.5.2 step 2b).
	AfterPrack bool

	// TimerC, when not zero, bounds the wait for the final response to the INVITE of the outgoing leg, from each of
	// its provisional responses (RFC 3261 §16.6 step 11, which wants more than 3 minutes): past it, the INVITE is
	// cancelled, and the incoming one answered with a 408. Without it, the deadline of the incoming INVITE
	// (ua.Policy.AnswerTimeout) bounds both.
	TimerC time.Duration

	// Request, when set, completes each request the relay makes from req, a request of the peer of from, on the leg
	// to: what it carries is c. The initial INVITE of Bridge is the caller's.
	Request func(from, to *Leg, req *sip.Request, c *ua.Change)

	// Response, when set, completes each response the relay makes from res, a response of the peer of from, on the
	// leg to.
	Response func(from, to *Leg, res *sip.Response, rp *ua.Reply)

	// RelayRefresh reports whether a session refresh of the peer of from (ua.Request.Refresh) goes on to the other
	// leg, where a re-INVITE without an offer then asks for one: by default, one carrying more than a session
	// description, as the mcptt-info of an emergency upgrade (TS 24.379 §6.3.3.1.6). The others are answered on their
	// own leg, each leg keeping its own session timer (RFC 4028); an answer that changes the session there goes on.
	RelayRefresh func(from *Leg, r *ua.Request) bool

	// Final is the response of the incoming leg to the failure of the INVITE of the outgoing one, res or err; nil
	// for FinalFor.
	Final func(res *sip.Response, err error) ua.Reply
}

// Relay relays two legs of a call to each other (RFC 7092 §3.1): the INVITE of the incoming leg answered with the
// responses of the outgoing one (Bridge), each early dialog of the outgoing leg mirrored in one of the incoming leg,
// then each leg's requests relayed in the dialog paired with theirs, each session refreshed on its own leg, and the
// end of one leg ending the other.
//
// The relay sits under the application's handlers of the legs (Leg.SetHandler): it acts on each event first, then
// the application's function runs. A request, or a BYE, goes to the application's Request or Bye when it has one,
// which takes it, to respond to itself or to Forward; it is relayed otherwise. Its methods are called on the strand
// of the call.
type Relay struct {
	cfg  RelayConfig
	a, b *Leg

	// pairs are the dialogs of a and b relayed to each other, by the session of either: during the initial INVITE,
	// each early dialog of b and its mirror in a; then the two sessions.
	pairs map[*ua.Session]*pair

	// open are the exchanges in progress.
	open []*Exchange

	// The initial INVITE, a to b: its exchange when a offered; whether a supports 100rel; whether its INVITE was
	// answered; the code to answer it with should b fail; the header fields of its INVITE that b's requests carry;
	// Timer C.
	initial  *Exchange
	rel100   bool
	final    bool
	failCode int
	sticky   sip.Header
	timerC   *ua.Timer
}

// pair is a dialog of a and one of b relayed to each other. During the initial INVITE: in answers the INVITE of a in
// its dialog; answer is the answer of b given to a, reliably when answered; offer is an offer of b waiting for the
// answer of a, in exchange x; relayed are the responses of b carried on. op is the modification of either dialog in
// progress on the other; refresh is a refresh one leg answered on its own, whose answer may change the session.
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

// refresh is a re-INVITE without an offer of the peer of leg, answered on its leg with the session as it was: prev
// is the description of that peer then.
type refresh struct {
	leg  *Leg
	prev []byte
}

// other is the dialog of the pair on the other leg than the one from is on.
func (pr *pair) other(p *Relay, from *Leg) *ua.Session {
	if from == p.a {
		return pr.b
	}

	return pr.a
}

// operation is a re-INVITE, an UPDATE or a PRACK with an offer of the peer of from, relayed to the other leg as a
// re-INVITE or an UPDATE: r waits for its response, x is its exchange. fetch marks a re-INVITE without an offer,
// whose offer the other leg makes in its 2xx, and offer that offer, waiting for the answer of from in its ACK;
// cancelled a re-INVITE cancelled, whose 2xx on the other leg is undone; join the exchange of Join, r nil.
type operation struct {
	from      *Leg
	r         *ua.Request
	x         *Exchange
	fetch     bool
	offer     *ua.Offer
	cancelled bool
	join      func(error)
}

// Bridge relays r, the initial INVITE of the peer of an incoming leg, to a new outgoing leg (Relay). out is the INVITE
// of that leg as Agent.Invite takes it: the Request-URI, From, To, Route, and the header fields and bodies of the
// application; the header fields of r that cross and its bodies of other media types are added, its offer made by
// cfg.Media, and a Replaces or Target-Dialog of r naming a relayed dialog of the agent names the dialog paired with
// it (RFC 3891, RFC 4538), which the agent's Config.Extensions put on the strand of r. The extensions the outgoing leg advertises are those the incoming one supports (100rel,
// 199). The legs are Relay.A and Relay.B; Config.OnInvite returns what it likes.
func Bridge(c *Call, r *ua.Request, out *sip.Request, cfg RelayConfig, opts ...ua.Option) (*Relay, error) {
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

	cfg.Headers.copyHeader(&out.Header, a, b, in)
	p.mapDialogs(in, &out.Header)

	sdp, offered := r.Offer()

	if offered {
		p.initial = &Exchange{From: a, To: b, Method: "INVITE", Initial: true, Offer: sdp}

		if sdp, err = p.begin(p.initial); err != nil {
			b.removed()

			_ = r.Reject(488)

			return p, err
		}
	}

	own, _ := out.Content().Parts()
	out.SetBodies(cfg.Headers.bodies(a, b, in, own, sdp)...)

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

	if err := b.dial(out, opts...); err != nil {
		p.endAll()

		_ = r.Reject(500)

		return p, err
	}

	p.armTimerC()

	return p, nil
}

// NewRelay relays two legs with confirmed sessions to each other, as they stand: a leg joining another whose session
// already agrees with it, as the pre-established session of TS 24.379 §10.1.1.3.1.2 whose media the B2BUA anchors.
// Join first makes the sessions agree. The relay of an initial INVITE is Bridge.
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

// Join relays two legs with confirmed sessions to each other, making their sessions agree first (RFC 3725 §4.2):
// a re-INVITE without an offer to a, its offer in the 2xx made into one for b in a re-INVITE, the answer of b back to
// a in the ACK. done, when not nil, gets the outcome; the relay is in place meanwhile.
func Join(a, b *Leg, cfg RelayConfig, done func(error)) (*Relay, error) {
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

	err = a.s.Reinvite(ua.Change{}, func(res *sip.Response, err error) {
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

// joinError is the failure of the re-INVITE of Join.
type joinError struct{ res *sip.Response }

func (e *joinError) Error() string {
	if e.res == nil {
		return "sip/b2bua: join failed"
	}

	return "sip/b2bua: join failed: " + e.res.StartLine()
}

// A is the incoming leg of the relay, B the outgoing one (Bridge); the legs given, in order (NewRelay, Join).
func (p *Relay) A() *Leg { return p.a }
func (p *Relay) B() *Leg { return p.b }

// Paired is the dialog relayed to s, a dialog of either leg: its mirror, or the other session.
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

// Stop ends the relay: the events of the legs go to the application's handlers alone. The exchanges in progress end
// without effect for the Media.
func (p *Relay) Stop() {
	p.a.call.check()

	for _, l := range []*Leg{p.a, p.b} {
		if l.relay == p {
			l.relay = nil
		}
	}

	if p.timerC != nil {
		p.timerC.Stop()
	}

	p.endAll()
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

// reply is the response relayed from res, a response on from, with sdp as its session description.
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

// change is what a request relayed from req, a request on from, carries, sdp its session description.
func (p *Relay) change(from *Leg, req *sip.Request, sdp []byte) ua.Change {
	to := p.other(from)
	c := ua.Change{Bodies: p.cfg.Headers.bodies(from, to, req, nil, sdp)}
	p.cfg.Headers.copyHeader(&c.Header, from, to, req)

	if f := p.cfg.Request; f != nil {
		f(from, to, req, &c)
	}

	return c
}

// decorate adds the sticky header fields of the INVITE of a to the requests of b.
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

// mapDialogs sets in h the Replaces and Target-Dialog of in, which name a dialog of the agent, to name the dialog
// relayed to it, as its peer knows it (RFC 3891 §3, RFC 4538 §3): its local tag that of the peer.
func (p *Relay) mapDialogs(in *sip.Request, h *sip.Header) {
	a := p.a.call.a

	paired := func(id dialog.ID) (dialog.ID, bool) {
		s := a.Session(id)
		if s == nil {
			return dialog.ID{}, false
		}

		// A relay on the strand of this call alone: the agent puts a request naming a dialog on its strand
		// (Config.Extensions).
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

// armTimerC arms Timer C again, from a provisional response of b.
func (p *Relay) armTimerC() {
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

		p.report(p.b.End(ua.Reply{}))
		p.respond(nil, ua.Reply{Code: 408})
	})
}

// The initial INVITE.

// mirrorOf is the pair of the early dialog s of b: the first one answered in the first early dialog of a, the others
// in forks of it; nil when a can no longer fork.
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

// progress relays a provisional response of b to the mirror of its early dialog: the answer in it, reliably when a
// supports 100rel, kept for the 2xx otherwise.
func (p *Relay) progress(l *Leg, s *ua.Session, res *sip.Response) {
	if l != p.b || p.final || s == nil || res.StatusCode <= 100 || res.StatusCode == 199 {
		return
	}

	p.armTimerC()

	pr := p.mirrorOf(s)
	if pr == nil || pr.relayed[res] {
		return
	}

	reliable := p.rel100 && res.Header.HasOptionTag("Require", sip.OptionTag100rel)

	var sdp []byte

	if body := sessionSDP(res.Envelope); body != nil && p.initial != nil && !pr.answered {
		answer, err := p.answerOf(p.initial, s, body)
		if err != nil {
			p.abandon(488)
			return
		}

		sdp = answer
		pr.answer = answer
		pr.answered = reliable
	}

	rp := p.reply(l, res, res.StatusCode, sdp)
	rp.Reliable = reliable

	p.report(pr.in.Progress(rp))
}

// offer takes an offer of l in a response, reporting whether it is the relay's: of b to the INVITE of a without one,
// carried on to the mirror in a reliable 1xx or its 2xx, for its answer in the PRACK or the ACK; of the leg a
// re-INVITE without an offer was relayed to; or of a, to Join. The application takes the others.
func (p *Relay) offer(l *Leg, s *ua.Session, o *ua.Offer) bool {
	if pr := p.pairs[s]; pr != nil && pr.op != nil && pr.op.fetch && l != pr.op.from {
		p.fetched(l, pr, o)
		return true
	}

	if l != p.b || p.final || p.initial != nil {
		return false
	}

	pr := p.mirrorOf(s)
	if pr == nil {
		o.Reject()
		return true
	}

	res := o.Response()

	if res.IsProvisional() && !p.rel100 {
		// An offer in a reliable 1xx is answered in the PRACK, which a peer without 100rel cannot give.
		o.Reject()

		p.failCode = 488

		return true
	}

	x := &Exchange{From: p.b, To: p.a, Method: "INVITE", Initial: true, Offer: o.SDP()}

	sdp, err := p.begin(x)
	if err != nil {
		o.Reject()
		p.abandon(488)

		return true
	}

	pr.offer, pr.x = o, x

	if res.IsProvisional() {
		pr.relayed[res] = true

		rp := p.reply(l, res, res.StatusCode, sdp)
		rp.Reliable = true

		p.report(pr.in.Progress(rp))

		return true
	}

	p.respond(pr, p.reply(l, res, res.StatusCode, sdp))

	return true
}

// fetched carries on the offer o of l, made in the 2xx of a re-INVITE without an offer: to the peer of the other leg
// in the 2xx of its own re-INVITE without one, or, to Join, in a re-INVITE.
func (p *Relay) fetched(l *Leg, pr *pair, o *ua.Offer) {
	op := pr.op
	to := p.other(l)
	op.x = &Exchange{From: l, To: to, Method: "INVITE", Offer: o.SDP()}

	sdp, err := p.begin(op.x)
	if err != nil {
		o.Reject()

		pr.op = nil

		if op.join != nil {
			op.join(err)
		} else {
			p.report(op.r.Reject(488))
		}

		return
	}

	op.offer = o

	if op.join == nil {
		p.report(op.r.Respond(p.reply(l, o.Response(), 200, sdp)))
		return
	}

	target := pr.other(p, l)

	err = target.Reinvite(ua.Change{Bodies: []sip.BodyPart{ua.SDP(sdp)}}, func(res *sip.Response, err error) {
		pr.op = nil

		var body []byte
		if res != nil && res.IsSuccess() {
			body = sessionSDP(res.Envelope)
		}

		if body == nil {
			o.Reject()
			p.end(op.x, nil)

			if err == nil {
				err = &joinError{res}
			}

			op.join(err)

			return
		}

		answer, err := p.answerOf(op.x, target, body)
		if err == nil {
			err = o.Answer(ua.Change{Bodies: []sip.BodyPart{ua.SDP(answer)}})
		}

		if err != nil {
			o.Reject()
			p.end(op.x, nil)
		} else {
			p.end(op.x, target)
		}

		op.join(err)
	})
	if err != nil {
		pr.op = nil

		o.Reject()
		p.end(op.x, nil)
		op.join(err)
	}
}

// answer carries an answer of l on to the offer of the other leg it answers: of the peer of a to an offer of b, in
// the PRACK or the ACK of b; or of the peer of from to the offer a re-INVITE without one fetched. An answer to the
// offer of a refresh answered on its leg that changes the session goes on to the other leg.
func (p *Relay) answer(l *Leg, s *ua.Session, sdp []byte) {
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
			p.propagate(l, pr, sdp)
		}

		return
	default:
		return
	}

	answer, err := p.answerOf(x, s, sdp)
	if err == nil {
		err = o.Answer(ua.Change{Bodies: []sip.BodyPart{ua.SDP(answer)}})
	}

	if err != nil {
		p.report(err)
		o.Reject()
		p.end(x, nil)

		return
	}

	p.end(x, s)
}

// propagate carries a change of the session of the peer of l, made in its answer to the offer of a refresh answered
// on its leg, to the other leg as an offer: their sessions agree again (RFC 3264 §8).
func (p *Relay) propagate(l *Leg, pr *pair, sdp []byte) {
	to := pr.other(p, l)
	x := &Exchange{From: l, To: p.other(l), Method: "INVITE", Offer: sdp}

	p.report(to.Modify(ua.Modification{Method: "INVITE", Build: func() (ua.Change, error) {
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
			_, _ = p.answerOf(x, to, body)
		}

		p.end(x, to)
	}))
}

// established relays the 2xx of b to the mirror of its dialog, with the answer a has not had reliably; a 2xx
// crossing the end of a is acknowledged, and ended with a BYE.
func (p *Relay) established(l *Leg, msg sip.Message) {
	if l != p.b {
		return
	}

	res, _ := msg.(*sip.Response)
	if res == nil {
		return
	}

	pr := p.mirrorOf(p.b.s)

	if p.final || p.a.state == ua.Terminated || pr == nil {
		p.report(p.b.End(ua.Reply{}))
		return
	}

	// An offer of the 2xx goes on with Offer.
	if p.initial == nil && sessionSDP(res.Envelope) != nil {
		return
	}

	var sdp []byte

	if p.initial != nil && !pr.answered {
		sdp = pr.answer

		if body := sessionSDP(res.Envelope); body != nil {
			answer, err := p.answerOf(p.initial, pr.b, body)
			if err != nil {
				p.abandon(488)
				return
			}

			sdp = answer
		}
	}

	p.respond(pr, p.reply(l, res, res.StatusCode, sdp))
}

// respond answers the INVITE of a, once: a 2xx in the dialog of pr, which the exchange of the offer of a took effect
// in, another response in any. A 2xx the agent refuses fails the call.
func (p *Relay) respond(pr *pair, rp ua.Reply) {
	if p.final {
		return
	}

	p.final = true

	if p.timerC != nil {
		p.timerC.Stop()
	}

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
		p.report(p.b.End(ua.Reply{}))

		return
	}

	p.end(p.initial, pr.b)
}

// failed answers the INVITE of a with the failure of the INVITE of b.
func (p *Relay) failed(l *Leg, res *sip.Response, err error) {
	if l != p.b || p.final {
		return
	}

	if p.failCode != 0 {
		p.respond(nil, ua.Reply{Code: p.failCode})
		return
	}

	final := p.cfg.Final
	if final == nil {
		final = FinalFor
	}

	rp := final(res, err)
	if res != nil && rp.Code == res.StatusCode {
		rp = p.reply(l, res, rp.Code, nil)
	}

	p.respond(nil, rp)
}

// abandon ends both legs of an initial INVITE that cannot go on: a with code.
func (p *Relay) abandon(code int) {
	p.respond(nil, ua.Reply{Code: code})
	p.report(p.b.End(ua.Reply{}))
}

// earlyEnded ends the mirror of an early dialog that ended alone: one of b ended by its peer (a 199), one of a with a
// BYE of its peer (RFC 3261 §15).
func (p *Relay) earlyEnded(l *Leg, s *ua.Session, e ua.End) {
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

	c := ua.Reasons(e.Reasons()...)

	if other := pr.other(p, l); other != nil && other.State() == ua.Early {
		p.report(other.Bye(c, nil))
	}
}

// FinalFor is the final response to relay for the failure of an INVITE: its own, but for a challenge, which was the
// B2BUA's to answer (500), and a redirection, which the B2BUA does not follow (480); a 503 as a 500 (RFC 3261 §16.7
// step 6); 408 for its timeout and 503 for the agent closing, 500 for another error.
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

// The end of the legs.

// ended ends the other leg however it stands, with the Reason of the end of l (RFC 3326): a CANCEL of the INVITE of
// b, a failure or a BYE for a, answered or not. The exchanges in progress end without effect.
func (p *Relay) ended(l *Leg, e ua.End) {
	o := p.other(l)

	if p.timerC != nil {
		p.timerC.Stop()
	}

	p.endAll()

	if !p.final {
		p.final = true

		if l == p.b {
			// A failure answered a already (failed); any other end of b fails it.
			p.report(p.a.Respond(ua.Reply{Code: 480}))
			return
		}
	}

	if o.state != ua.Terminated {
		p.report(o.End(ua.Reply{Header: ua.Reasons(e.Reasons()...).Header}))
	}
}

// Forward relays a request of the peer of from, one the application's Request or Bye took, to the other leg, as the
// relay does with those it takes: a BYE ends the other leg, its 200 waiting for the response there (TS 24.379
// §6.3.2.1.6); a session refresh is answered on its own leg (RelayRefresh); another request goes in the dialog
// paired with the one it came in.
func (p *Relay) Forward(from *Leg, r *ua.Request) {
	p.a.call.check()

	req := r.Message()

	if req.Method == "BYE" {
		p.bye(from, r)
		return
	}

	var to *ua.Session

	pr := p.pairs[r.Session()]
	if pr != nil {
		to = pr.other(p, from)
	}

	// Without the dialog of the other leg yet, or once it ended, the request cannot go on (RFC 3311 §5.2).
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

		p.modify(from, r, pr, to)
	case "REFER", "SUBSCRIBE", "NOTIFY":
		// The subscriptions of a dialog, and the dialogs a REFER names, are the application's.
		p.report(r.Reject(501))
	default:
		err := to.Send(req.Method, p.change(from, req, nil), func(res *sip.Response, err error) {
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

// answerRefresh answers a refresh on its own leg; a re-INVITE without an offer gets the session as it was in its
// 2xx, whose answer in the ACK goes on should it change the session.
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

	parts, _ := r.Message().Content().Parts()

	return slices.ContainsFunc(parts, func(part sip.BodyPart) bool { return !part.IsSession() })
}

// bye relays the BYE of the peer of l: in an early dialog, it ends that one alone, the relay ending its mirror;
// otherwise the 200 waits for the response to the BYE of the other leg (TS 24.379 §6.3.2.1.6).
func (p *Relay) bye(l *Leg, r *ua.Request) {
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
		if err := s.Bye(c, respond); err != nil {
			respond(nil, err)
		}
	default:
		respond(nil, nil)
		p.report(o.End(ua.Reply{Header: c.Header}))
	}
}

// cancelled relays the CANCEL of a re-INVITE: its relayed re-INVITE is cancelled, and undone should its 2xx cross the
// CANCEL.
func (p *Relay) cancelled(l *Leg, r *ua.Request, cancel *sip.Request) {
	pr := p.pairs[r.Session()]
	if pr == nil || pr.op == nil || pr.op.r != r {
		return
	}

	pr.op.cancelled = true

	if s := pr.other(p, l); s != nil {
		p.report(s.CancelReinvite(p.change(l, cancel, nil)))
	}
}

// modify relays a re-INVITE, an UPDATE or a PRACK with an offer of the peer of from to the dialog to: its offer
// made by Media, the answer back; a re-INVITE without an offer fetches one from to, whose answer the peer of from
// gives in its ACK. A 491 goes back as it is, and so does a conflict on to, for the peer to retry (RFC 3261 §14.1).
func (p *Relay) modify(from *Leg, r *ua.Request, pr *pair, to *ua.Session) {
	req := r.Message()
	offer, offered := r.Offer()

	// One offer at a time across the pair; an UPDATE without one, as for preconditions, goes during a re-INVITE
	// (RFC 3311 §5.2).
	if pr.op != nil && (offered || req.Method == "INVITE") {
		p.report(r.Reject(491))
		return
	}

	o := p.other(from)
	op := &operation{from: from, r: r, fetch: !offered && req.Method == "INVITE"}

	var sdp []byte

	if offered {
		op.x = &Exchange{From: from, To: o, Method: req.Method, Offer: offer}

		var err error
		if sdp, err = p.begin(op.x); err != nil {
			p.report(r.Reject(488))
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
			// r was answered with a 487: a 2xx crossing the CANCEL is undone.
			p.end(op.x, nil)

			if res != nil && res.IsSuccess() && offered {
				p.restore(from, o, to)
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
			// A 2xx without the offer it owes.
			pr.op = nil

			p.report(r.Reject(500))
		case op.fetch:
			// The offer of the 2xx went on with Offer; the answer of the ACK follows it.
		default:
			var answer []byte

			if body := sessionSDP(res.Envelope); body != nil && op.x != nil {
				a, err := p.answerOf(op.x, to, body)
				if err != nil {
					p.end(op.x, nil)
					p.report(r.Reject(488))
					p.restore(from, o, to)

					return
				}

				answer = a
			}

			if err := r.Respond(p.reply(o, res, res.StatusCode, answer)); err != nil {
				p.report(err)
				p.end(op.x, nil)
				p.restore(from, o, to)

				return
			}

			p.end(op.x, to)
		}
	}

	c := p.change(from, req, sdp)

	var err error
	if req.Method == "INVITE" {
		err = to.Reinvite(c, done)
	} else {
		err = to.Update(c, done)
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

// restore offers to again the session description of the peer of from, which an exchange changed on to alone: a
// re-INVITE cancelled after its 2xx, an answer Media could not carry back (RFC 3264 §8). Should that fail, the legs
// cannot agree, and the call ends.
func (p *Relay) restore(from, o *Leg, to *ua.Session) {
	s := from.Session()
	if s == nil {
		return
	}

	_, remote, ok := s.Negotiated()
	if !ok {
		return
	}

	x := &Exchange{From: from, To: o, Method: "INVITE", Offer: remote}

	err := to.Modify(ua.Modification{Method: "INVITE", Build: func() (ua.Change, error) {
		sdp, err := p.begin(x)
		if err != nil {
			return ua.Change{}, err
		}

		return ua.Change{Bodies: []sip.BodyPart{ua.SDP(sdp)}}, nil
	}}, func(res *sip.Response, err error) {
		if res == nil || !res.IsSuccess() {
			p.end(x, nil)
			p.report(err)
			p.a.call.End(ua.Reply{})

			return
		}

		if body := sessionSDP(res.Envelope); body != nil {
			_, _ = p.answerOf(x, to, body)
		}

		p.end(x, to)
	})
	p.report(err)
}
