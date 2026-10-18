package b2bua

import (
	"errors"
	"slices"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// Copy is which parts of a message cross from one leg to the other: header fields by name, and the body parts
// other than the session description, byte-exact (TS 24.379 §6.3.2.1.5: mcptt-info, resource-lists).
type Copy struct {
	// Request are the header fields copied from a request to the one it makes on the other leg.
	Request []string

	// Response are the header fields copied from a response to the one it makes on the other leg.
	Response []string

	// Part, when not nil, chooses the body parts other than the session description that cross; all do without it.
	Part func(p sip.BodyPart) bool
}

// DefaultCopy copies the header fields of the IETF extensions an INVITE session carries end to end: caller
// preferences (RFC 3841), Resource-Priority (RFC 4412), Answer-Mode and Priv-Answer-Mode (RFC 5373), P-Answer-State
// (RFC 4964), Reason (RFC 3326), Warning, Retry-After, Priority, Subject and Alert-Info (RFC 3261).
var DefaultCopy = Copy{
	Request: []string{
		"Accept-Contact", "Reject-Contact", "Resource-Priority", "Answer-Mode", "Priv-Answer-Mode", "Priority",
		"Subject", "Alert-Info", "Reason",
	},
	Response: []string{
		"P-Answer-State", "Answer-Mode", "Priv-Answer-Mode", "Warning", "Reason", "Retry-After", "Alert-Info",
	},
}

// Media makes the session descriptions one leg sends from those of the other: the same ones for a signalling-only
// relay, or ones with the media the B2BUA anchors. from and to are the legs the description comes from and goes to.
// An error rejects the request, or ends the call, with a 488.
type Media interface {
	Offer(from, to *Leg, sdp []byte) ([]byte, error)
	Answer(from, to *Leg, sdp []byte) ([]byte, error)
}

// RelayConfig configures a relay.
type RelayConfig struct {
	Copy Copy

	// Media, when set, makes the session descriptions of each leg from those of the other; they cross unchanged
	// without it.
	Media Media

	// AfterPrack holds the 2xx to the initial INVITE of the incoming leg until the PRACK of its last reliable 1xx
	// (ua.Reply.AfterPrack, TS 24.379 §6.3.2.2.5.2 step 2b).
	AfterPrack bool

	// Request, when set, completes each request the relay makes on to the other leg: from is the leg of req, the
	// request it relays, and c what the relay made of it. The initial INVITE of Bridge is completed by its caller.
	Request func(from *Leg, req *sip.Request, c *ua.Change)

	// Response, when set, completes each response the relay makes on the other leg: from is the leg of res.
	Response func(from *Leg, res *sip.Response, rp *ua.Reply)

	// RelayRefresh reports whether a session refresh of the peer of from (ua.Request.Refresh) goes on to the other
	// leg, which a re-INVITE without an offer then asks for one: by default, one carrying more than a session
	// description, as the mcptt-info of an emergency upgrade (TS 24.379 §6.3.3.1.6). The others are answered on their
	// own leg, each leg keeping its own session timer (RFC 4028).
	RelayRefresh func(from *Leg, r *ua.Request) bool

	// Final is the response of the incoming leg to the failure of the INVITE of the outgoing one, res or err; nil
	// for FinalFor.
	Final func(res *sip.Response, err error) ua.Reply
}

// relay relays two legs of a call to each other.
type relay struct {
	cfg  RelayConfig
	a, b *Leg

	// The initial INVITE, a to b: whether a offered; the mirror of each early dialog of b in a, by the session of
	// either; whether the INVITE of a was answered; the code to answer it with should b fail.
	offered  bool
	mirrors  map[*ua.Session]*mirror
	final    bool
	failCode int
	rel100   bool

	// op is the modification of one leg in progress on the other.
	op *operation
}

// mirror is an early dialog of b and the one of a that mirrors it: in answers the INVITE of a in it; answered marks
// the answer of b given to a reliably; offer is the offer of b waiting for the answer of a.
type mirror struct {
	in       *ua.Request
	b        *ua.Session
	answered bool
	offer    *ua.Offer
	relayed  map[*sip.Response]bool
}

// operation is a re-INVITE or an UPDATE of the peer of from, relayed to the other leg: r waits for its response;
// fetch marks one without an offer, whose offer the other leg makes in its 2xx, and offer that offer, waiting for
// the answer of from in its ACK.
type operation struct {
	from  *Leg
	r     *ua.Request
	fetch bool
	offer *ua.Offer
}

// Bridge relays r, the initial INVITE of the peer of an incoming leg, to a new outgoing leg: out is the INVITE of that
// leg as Agent.Invite takes it (Request-URI, From, To, Route and the header fields of the application), to which the
// header fields of cfg.Copy and the bodies of r are added, its offer made by cfg.Media. The responses of the outgoing
// leg answer r, each of its early dialogs mirrored in one of the incoming leg (Leg.Fork); then each leg's requests
// are relayed to the other (Relay). Config.OnInvite returns a.Handler().
func Bridge(c *Call, r *ua.Request, out *sip.Request, cfg RelayConfig, opts ...ua.Option) (a, b *Leg, err error) {
	a, err = c.Incoming(r, LegHandler{})
	if err != nil {
		return nil, nil, err
	}

	b, err = c.leg(LegHandler{})
	if err != nil {
		return a, nil, err
	}

	p := &relay{cfg: cfg, a: a, b: b, mirrors: make(map[*ua.Session]*mirror)}

	in := r.Message()
	p.rel100 = in.Header.HasOptionTag("Supported", sip.OptionTag100rel) ||
		in.Header.HasOptionTag("Require", sip.OptionTag100rel)

	copyHeader(&out.Header, in.Header, cfg.Copy.Request)

	sdp, offered := r.Offer()
	p.offered = offered

	if offered {
		if sdp, err = p.media(a, b, sdp, true); err != nil {
			_ = r.Reject(488)
			return a, b, err
		}
	}

	out.SetBodies(bodies(in.Envelope, cfg.Copy, sdp)...)

	a.h, b.h = p.handler(), p.handler()

	if err := b.dial(out, opts...); err != nil {
		_ = r.Reject(500)
		return a, b, err
	}

	return a, b, nil
}

// Relay relays the requests of two legs with confirmed sessions to each other: a leg joining another mid-call. The
// relay of an initial INVITE is Bridge.
func Relay(a, b *Leg, cfg RelayConfig) error {
	if a.call.st != b.call.st {
		return ErrStrand
	}

	if a.state == ua.Terminated || b.state == ua.Terminated {
		return ErrEnded
	}

	p := &relay{cfg: cfg, a: a, b: b, final: true}
	a.h, b.h = p.handler(), p.handler()

	return nil
}

func (p *relay) other(l *Leg) *Leg {
	if l == p.a {
		return p.b
	}

	return p.a
}

func (p *relay) handler() LegHandler {
	return LegHandler{
		Progress:    p.progress,
		Established: p.established,
		Answer:      p.answer,
		Offer:       p.offerOf,
		Request:     p.request,
		Bye:         p.bye,
		Cancelled:   p.cancelled,
		Failed:      p.failed,
		EarlyEnded:  p.earlyEnded,
		Ended:       p.ended,
	}
}

func (p *relay) media(from, to *Leg, sdp []byte, offer bool) ([]byte, error) {
	switch {
	case p.cfg.Media == nil:
		return sdp, nil
	case offer:
		return p.cfg.Media.Offer(from, to, sdp)
	}

	return p.cfg.Media.Answer(from, to, sdp)
}

// reply is the response relayed from res, with sdp as its session description.
func (p *relay) reply(from *Leg, res *sip.Response, code int, sdp []byte) ua.Reply {
	rp := ua.Reply{Code: code, Bodies: bodies(res.Envelope, p.cfg.Copy, sdp)}
	if code == res.StatusCode {
		rp.Reason = res.Reason
	}

	copyHeader(&rp.Header, res.Header, p.cfg.Copy.Response)

	if f := p.cfg.Response; f != nil {
		f(from, res, &rp)
	}

	return rp
}

// change is what a request relayed from req carries, sdp its session description.
func (p *relay) change(from *Leg, req *sip.Request, sdp []byte) ua.Change {
	c := ua.Change{Bodies: bodies(req.Envelope, p.cfg.Copy, sdp)}
	copyHeader(&c.Header, req.Header, p.cfg.Copy.Request)

	if f := p.cfg.Request; f != nil {
		f(from, req, &c)
	}

	return c
}

// The initial INVITE.

// mirrorOf is the mirror of the early dialog s of b: the first one answered in the first early dialog of a, the
// others in forks of it; nil when a can no longer fork.
func (p *relay) mirrorOf(s *ua.Session) *mirror {
	if m := p.mirrors[s]; m != nil {
		return m
	}

	in := p.a.in

	if len(p.mirrors) > 0 {
		f, err := p.a.Fork()
		if err != nil {
			return nil
		}

		in = f
	}

	m := &mirror{in: in, b: s, relayed: make(map[*sip.Response]bool)}
	p.mirrors[s] = m
	p.mirrors[in.Session()] = m

	return m
}

// progress relays a provisional response of b to the mirror of its early dialog: the answer in it, reliably when a
// supports 100rel.
func (p *relay) progress(l *Leg, s *ua.Session, res *sip.Response) {
	if l != p.b || p.final || s == nil || res.StatusCode <= 100 || res.StatusCode == 199 {
		return
	}

	m := p.mirrorOf(s)
	if m == nil || m.relayed[res] {
		return
	}

	reliable := p.rel100 && res.Header.HasOptionTag("Require", sip.OptionTag100rel)

	var sdp []byte

	if body := sessionSDP(res.Envelope); body != nil && p.offered && !m.answered {
		answer, err := p.media(p.b, p.a, body, false)
		if err != nil {
			p.abandon(488)
			return
		}

		sdp = answer
		m.answered = reliable
	}

	rp := p.reply(l, res, res.StatusCode, sdp)
	rp.Reliable = reliable

	_ = m.in.Progress(rp)
}

// offerOf takes an offer of l in a response: of b to the INVITE of a without one, carried on to the mirror in a
// reliable 1xx or its 2xx, for its answer in the PRACK or the ACK; or of the leg a re-INVITE without an offer was
// relayed to.
func (p *relay) offerOf(l *Leg, s *ua.Session, o *ua.Offer) {
	if op := p.op; op != nil && op.fetch && l != op.from {
		sdp, err := p.media(l, op.from, o.SDP(), true)
		if err != nil {
			o.Reject()
			_ = op.r.Reject(488)
			p.op = nil

			return
		}

		op.offer = o
		_ = op.r.Respond(p.reply(l, o.Response(), 200, sdp))

		return
	}

	m := p.mirrorOf(s)
	if l != p.b || p.final || p.offered || m == nil {
		o.Reject()
		return
	}

	res := o.Response()

	sdp, err := p.media(p.b, p.a, o.SDP(), true)
	if err != nil {
		o.Reject()

		p.failCode = 488

		return
	}

	m.offer = o

	if res.IsProvisional() {
		if !p.rel100 {
			// An offer in a reliable 1xx is answered in the PRACK, which a peer without 100rel cannot give.
			m.offer = nil
			p.failCode = 488

			o.Reject()

			return
		}

		m.relayed[res] = true

		rp := p.reply(l, res, res.StatusCode, sdp)
		rp.Reliable = true

		_ = m.in.Progress(rp)

		return
	}

	p.respond(m, p.reply(l, res, res.StatusCode, sdp))
}

// answer carries the answer of a to an offer of b on: in the PRACK or the ACK of b.
func (p *relay) answer(l *Leg, s *ua.Session, sdp []byte) {
	var o *ua.Offer

	switch m := p.mirrors[s]; {
	case p.op != nil && p.op.offer != nil && l == p.op.from:
		o, p.op = p.op.offer, nil
	case l == p.a && m != nil && m.offer != nil:
		o, m.offer = m.offer, nil
	default:
		return
	}

	answer, err := p.media(l, p.other(l), sdp, false)
	if err != nil {
		o.Reject()
		return
	}

	_ = o.Answer(ua.Change{Bodies: []sip.BodyPart{ua.SDP(answer)}})
}

// established relays the 2xx of b to the mirror of its dialog; a 2xx crossing the end of a is acknowledged, and ended
// with a BYE.
func (p *relay) established(l *Leg, msg sip.Message) {
	if l != p.b {
		return
	}

	res, _ := msg.(*sip.Response)
	if res == nil {
		return
	}

	m := p.mirrorOf(p.b.s)

	if p.final || p.a.state == ua.Terminated || m == nil {
		_ = p.b.End(ua.Reply{})
		return
	}

	// An offer of the 2xx goes on with Offer.
	if !p.offered && sessionSDP(res.Envelope) != nil {
		return
	}

	var sdp []byte

	if body := sessionSDP(res.Envelope); body != nil && p.offered && !m.answered {
		answer, err := p.media(p.b, p.a, body, false)
		if err != nil {
			p.abandon(488)
			return
		}

		sdp = answer
	}

	p.respond(m, p.reply(l, res, res.StatusCode, sdp))
}

// respond answers the INVITE of a, once: a 2xx in the dialog of m, another response in any.
func (p *relay) respond(m *mirror, rp ua.Reply) {
	if p.final {
		return
	}

	p.final = true

	if rp.Code >= 300 || m == nil {
		_ = p.a.Respond(rp)
		return
	}

	rp.AfterPrack = p.cfg.AfterPrack

	if err := m.in.Respond(rp); err != nil {
		_ = p.b.End(ua.Reply{})
	}
}

// failed answers the INVITE of a with the failure of the INVITE of b.
func (p *relay) failed(l *Leg, res *sip.Response, err error) {
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
func (p *relay) abandon(code int) {
	p.respond(nil, ua.Reply{Code: code})
	_ = p.b.End(ua.Reply{})
}

// earlyEnded ends the mirror of an early dialog that ended alone: an early dialog of b ended by its peer, or one of a
// with a BYE of its peer.
func (p *relay) earlyEnded(l *Leg, s *ua.Session, e ua.End) {
	m := p.mirrors[s]
	if m == nil || p.final {
		return
	}

	c := ua.Reasons(e.Reasons()...)

	if l == p.b {
		if as := m.in.Session(); as.State() == ua.Early {
			_ = as.Bye(c, nil)
		}

		return
	}

	if m.b.State() == ua.Early {
		_ = m.b.Bye(c, nil)
	}
}

// FinalFor is the final response to relay for the failure of an INVITE: its own, but for a challenge, which was the
// B2BUA's to answer, and a redirection, which the B2BUA does not follow (480); 408 for its timeout and 503 for the
// agent closing (RFC 3261 §16.7 step 6), 500 for another error.
func FinalFor(res *sip.Response, err error) ua.Reply {
	switch {
	case res != nil && (res.StatusCode == 401 || res.StatusCode == 407):
		return ua.Reply{Code: 500}
	case res != nil && res.StatusCode < 400:
		return ua.Reply{Code: 480}
	case res != nil && res.StatusCode == 503:
		// RFC 3261 §16.7 step 6: a 503 is not relayed as is, which would make the peer think this server overloaded.
		return ua.Reply{Code: 500}
	case res != nil:
		return ua.Reply{Code: res.StatusCode}
	case errors.Is(err, transaction.ErrTimeout):
		return ua.Reply{Code: 408}
	case errors.Is(err, ua.ErrClosed), errors.Is(err, transaction.ErrClosed):
		return ua.Reply{Code: 503}
	}

	return ua.Reply{Code: 500}
}

// The end of the legs.

// bye relays the BYE of the peer of l: the 200 waits for the response to the BYE of the other leg (TS 24.379
// §6.3.2.1.6).
func (p *relay) bye(l *Leg, r *ua.Request) {
	o := p.other(l)

	respond := func(*sip.Response, error) { _ = r.Respond(ua.Reply{Code: 200}) }

	switch {
	case o.state == ua.Terminated:
		respond(nil, nil)
	case o.s != nil && o.s.State() == ua.Confirmed:
		if err := o.s.Bye(p.change(l, r.Message(), nil), respond); err != nil {
			respond(nil, err)
		}
	default:
		respond(nil, nil)

		c := p.change(l, r.Message(), nil)
		_ = o.End(ua.Reply{Header: c.Header})
	}
}

// cancelled relays the CANCEL of a re-INVITE.
func (p *relay) cancelled(l *Leg, r *ua.Request, cancel *sip.Request) {
	op := p.op
	if op == nil || op.r != r {
		return
	}

	p.op = nil

	if s := p.other(l).s; s != nil {
		_ = s.CancelReinvite(p.change(l, cancel, nil))
	}
}

// ended ends the other leg: a CANCEL of the INVITE of b, or its BYE, with the Reason of the end of l (RFC 3326).
func (p *relay) ended(l *Leg, e ua.End) {
	if l == p.a && !p.final {
		p.final = true
	}

	o := p.other(l)
	if o.state == ua.Terminated {
		return
	}

	rp := ua.Reply{Header: ua.Reasons(e.Reasons()...).Header}

	if l == p.b && o.state == ua.Early {
		// A failed INVITE answered a already; any other end of b fails it.
		p.respond(nil, ua.Reply{Code: 480})
		return
	}

	_ = o.End(rp)
}

// Requests within the sessions.

// request relays a request of the peer of l to the other leg: refreshes are answered on each leg, the rest goes on.
func (p *relay) request(l *Leg, r *ua.Request) {
	req := r.Message()
	o := p.other(l)

	// Without the dialog of the other leg yet, or once it ended, the request cannot go on (RFC 3311 §5.2).
	if o.s == nil || o.state == ua.Terminated {
		_ = r.Reject(500, sip.Field{Name: "Retry-After", Value: "1"})
		return
	}

	switch req.Method {
	case "INVITE", "UPDATE":
		if r.Refresh() && !p.relayRefresh(l, r) {
			_ = r.AnswerUnchanged(ua.Change{})
			return
		}

		p.modify(l, r, o)
	case "PRACK", "REFER", "SUBSCRIBE", "NOTIFY":
		// A new offer in a PRACK, and the subscriptions of a dialog, are the application's.
		_ = r.Reject(501)
	default:
		err := o.s.Send(req.Method, p.change(l, req, nil), func(res *sip.Response, err error) {
			if res == nil {
				_ = r.Respond(FinalFor(nil, err))
				return
			}

			_ = r.Respond(p.reply(o, res, res.StatusCode, sessionSDP(res.Envelope)))
		})
		if err != nil {
			_ = r.Reject(500)
		}
	}
}

func (p *relay) relayRefresh(l *Leg, r *ua.Request) bool {
	if f := p.cfg.RelayRefresh; f != nil {
		return f(l, r)
	}

	parts, _ := r.Message().Content().Parts()

	return slices.ContainsFunc(parts, func(part sip.BodyPart) bool { return !part.IsSession() })
}

// modify relays a re-INVITE or an UPDATE of the peer of l to o: its offer made by Media, the answer of o back; one
// without an offer fetches one from o, whose answer the peer of l gives in its ACK. A 491 of o goes back as it is
// (RFC 3261 §14.1), for the peer to retry.
func (p *relay) modify(l *Leg, r *ua.Request, o *Leg) {
	req := r.Message()

	if p.op != nil {
		_ = r.Reject(491)
		return
	}

	offer, offered := r.Offer()

	var sdp []byte

	if offered {
		var err error
		if sdp, err = p.media(l, o, offer, true); err != nil {
			_ = r.Reject(488)
			return
		}
	}

	op := &operation{from: l, r: r, fetch: !offered && req.Method == "INVITE"}
	p.op = op

	done := func(res *sip.Response, err error) {
		if p.op == op && (res == nil || !res.IsSuccess() || !op.fetch) {
			p.op = nil
		}

		switch {
		case res == nil && errors.Is(err, ua.ErrPending):
			_ = r.Reject(491)
		case res == nil:
			_ = r.Respond(FinalFor(nil, err))
		case !res.IsSuccess():
			_ = r.Respond(p.reply(o, res, res.StatusCode, nil))
		case op.fetch && op.offer == nil:
			// A 2xx without the offer it owes.
			p.op = nil
			_ = r.Reject(500)
		case op.fetch:
			// The offer of the 2xx went on with Offer; the answer of the ACK follows it.
		default:
			var answer []byte

			if body := sessionSDP(res.Envelope); body != nil {
				a, err := p.media(o, l, body, false)
				if err != nil {
					_ = r.Reject(488)
					return
				}

				answer = a
			}

			_ = r.Respond(p.reply(o, res, res.StatusCode, answer))
		}
	}

	c := p.change(l, req, sdp)

	var err error
	if req.Method == "INVITE" {
		err = o.s.Reinvite(c, done)
	} else {
		err = o.s.Update(c, done)
	}

	if err != nil {
		p.op = nil

		if errors.Is(err, ua.ErrPending) {
			_ = r.Reject(491)
		} else {
			_ = r.Reject(500)
		}
	}
}

// Bodies and header fields.

// sessionSDP is the session description of a message.
func sessionSDP(e sip.Envelope) []byte {
	p, ok, err := e.Content().FindFunc(sip.BodyPart.IsSession)
	if !ok || err != nil {
		return nil
	}

	return p.Body
}

// bodies are the parts of e that cross, byte-exact, with sdp in place of its session description, or added.
func bodies(e sip.Envelope, c Copy, sdp []byte) []sip.BodyPart {
	parts, _ := e.Content().Parts()

	var out []sip.BodyPart

	placed := false

	for _, part := range parts {
		switch {
		case part.IsSession():
			if sdp != nil && !placed {
				out = append(out, ua.SDP(sdp))
				placed = true
			}
		case c.Part == nil || c.Part(part):
			out = append(out, part)
		}
	}

	if sdp != nil && !placed {
		out = append([]sip.BodyPart{ua.SDP(sdp)}, out...)
	}

	return out
}

func copyHeader(dst *sip.Header, src sip.Header, names []string) {
	for _, name := range names {
		for _, v := range src.Values(name) {
			dst.Add(name, v)
		}
	}
}
