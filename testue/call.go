package testue

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
)

const (
	DefaultSessionExpires = 1800 * time.Second

	mmtelService = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

	firstMediaPort = 40000

	// Each call takes four ports: audio RTP and RTCP, then video RTP and RTCP.
	portsPerCall = 4
	maxCalls     = 5000
)

// allowed are the methods the UE allows (TS 24.229 §5.1.3.1).
var allowed = []string{"INVITE", "ACK", "OPTIONS", "CANCEL", "BYE", "UPDATE", "PRACK", "NOTIFY", "MESSAGE"}

var (
	ErrCallEnded = errors.New("testue: call ended")

	ErrCallState = errors.New("testue: not allowed in this call state")

	ErrNoAck = errors.New("testue: no ACK to the 2xx")

	ErrNoPrack = errors.New("testue: no PRACK to the reliable provisional response")
)

type CallState int

const (
	CallInit CallState = iota
	CallEarly
	CallConfirmed
	CallTerminated
)

func (s CallState) String() string {
	switch s {
	case CallInit:
		return "init"
	case CallEarly:
		return "early"
	case CallConfirmed:
		return "confirmed"
	case CallTerminated:
		return "terminated"
	}

	return fmt.Sprintf("CallState(%d)", int(s))
}

type EndReason int

const (
	NotEnded EndReason = iota
	LocalBye
	RemoteBye
	Cancelled
	Rejected
	TimedOut
	Expired
	Closed
	// MediaLost ends a call whose voice bearer was lost or refused (TS 24.229 §6.1.1).
	MediaLost
)

func (r EndReason) String() string {
	switch r {
	case NotEnded:
		return "not ended"
	case LocalBye:
		return "local BYE"
	case RemoteBye:
		return "remote BYE"
	case Cancelled:
		return "cancelled"
	case Rejected:
		return "rejected"
	case TimedOut:
		return "timed out"
	case Expired:
		return "session expired"
	case Closed:
		return "closed"
	case MediaLost:
		return "media bearer lost"
	}

	return fmt.Sprintf("EndReason(%d)", int(r))
}

type CallOptions struct {
	Preconditions bool

	// Video makes the call a video call: the INVITE offers audio and video, and asks for a video capable callee
	// (IR.94 §2.2.2). It needs Config.Video.
	Video bool

	SessionExpires time.Duration

	NoSessionTimer bool

	Headers []sip.Field
}

// Call is a call of the UE, on the sessions of its agent: the one of an incoming call, one for each dialog of an
// outgoing call forked downstream (RFC 3261 §12.1.2, §13.2.2.4).
//
// The handlers of the agent, on its loop, take the lock of the call; the methods of the call never call the agent
// with it held.
type Call struct {
	u        *UE
	incoming bool
	callID   string
	events   chan Event
	done     chan struct{}

	mu      sync.Mutex
	changed chan struct{}
	state   CallState
	end     EndReason
	endErr  error
	reasons []sip.Reason
	invite  *sip.Request

	// leg is the call's dialog: the only one of an incoming call; for an outgoing call, the answered one, or before
	// the answer the earliest early dialog still alive. legs holds every dialog the call created.
	leg  *leg
	legs []*leg

	// initial is the media state just after the initial offer, from which each forked dialog's own offer/answer
	// exchange starts (RFC 3264 §4).
	initial *media

	saHeld bool

	// The outgoing call: its INVITE, its final response or error, and how a cancelled one ends.
	out       *ua.Outgoing
	final     *sip.Response
	finalErr  error
	cancelled bool
	cancelEnd EndReason

	// The incoming call: its INVITE and offer, whether the UE gave its SDP yet, the PRACKs of its reliable 1xx, the
	// ACK of its 2xx.
	in       *ua.Request
	offer    *sdp.Session
	answered bool
	rel100   bool
	pracks   int
	acked    bool

	// byeEnd and rejectEnd are how the call ends with a BYE or a final response of the UE.
	byeEnd, rejectEnd EndReason

	// refuse answers the offers of the remote end with a 491 (RefuseOffers).
	refuse bool
}

// leg is one dialog of a call, with its own offer/answer state.
type leg struct {
	s *ua.Session
	m *media

	// offering is set while an offer of the UE in a re-INVITE or an UPDATE waits for its answer; reporting while the
	// UPDATE reporting its resources (RFC 3312 §5) is on its way, which one trigger sends at a time.
	offering  bool
	reporting bool

	// released is set once the early dialog ended without the call: by a 199 (RFC 6228 §4), a BYE, or because
	// another dialog was answered (RFC 3261 §13.2.2.4).
	released bool
}

// newCall starts a call whose first offer, if the UE makes it, has video when video is set.
func (u *UE) newCall(incoming bool, invite *sip.Request, callID string, precondition, video bool) (*Call, error) {
	u.mu.Lock()
	port := firstMediaPort + portsPerCall*u.calls.next
	u.calls.next = (u.calls.next + 1) % maxCalls
	u.mu.Unlock()

	m := newMedia(u.cfg.Local, uint16(port), precondition, u.cfg.Video)

	if video {
		if err := m.addVideo(); err != nil {
			return nil, err
		}
	}

	return &Call{
		u:        u,
		incoming: incoming,
		callID:   callID,
		events:   make(chan Event, 256),
		done:     make(chan struct{}),
		changed:  make(chan struct{}),
		invite:   invite,
		leg:      &leg{m: m},
		byeEnd:   LocalBye,
	}, nil
}

func (u *UE) Calls() <-chan *Call { return u.calls.incoming }

func (c *Call) Incoming() bool { return c.incoming }

func (c *Call) Invite() *sip.Request {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.invite.Clone()
}

// Events are the requests and responses the UE received for the call, and its errors.
func (c *Call) Events() <-chan Event { return c.events }

func (c *Call) Done() <-chan struct{} { return c.done }

func (c *Call) State() CallState {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.state
}

func (c *Call) End() EndReason {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.end
}

// Reasons returns the Reason header field values (RFC 3326) of the CANCEL, BYE or final response that ended the
// call.
func (c *Call) Reasons() []sip.Reason {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.reasons)
}

func (c *Call) ID() dialog.ID {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.leg.s == nil {
		return dialog.ID{}
	}

	return c.leg.s.ID()
}

// EarlyDialogs returns the early dialogs of an outgoing call that are still alive: one per callee ringing when the
// INVITE was forked (RFC 3261 §13.2.2.4).
func (c *Call) EarlyDialogs() []dialog.ID {
	c.mu.Lock()

	var sessions []*ua.Session

	for _, l := range c.legs {
		if !l.released {
			sessions = append(sessions, l.s)
		}
	}

	c.mu.Unlock()

	var out []dialog.ID

	for _, s := range sessions {
		if s.State() == ua.Early {
			out = append(out, s.ID())
		}
	}

	return out
}

func (c *Call) LocalSDP() *sdp.Session {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.leg.m.local == nil {
		return nil
	}

	return c.leg.m.local.Clone()
}

func (c *Call) RemoteSDP() *sdp.Session {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.leg.m.remote == nil {
		return nil
	}

	return c.leg.m.remote.Clone()
}

// Stream is an m-line of the call's session (RFC 3264 §5).
type Stream struct {
	Index int
	// Kind is the media of the stream: sdp.Audio or sdp.Video.
	Kind string
	// Active is false once the stream is rejected or removed, with port 0 (RFC 3264 §8.2).
	Active bool
	// Direction is the direction the UE last gave the stream in its SDP.
	Direction sdp.Direction
	// Local and Remote are the RTP endpoints of the stream in the UE's last SDP and the remote end's; RTCP is on the
	// next port (RFC 3550 §11). Remote is zero until the remote end has sent its SDP.
	Local, Remote sdp.Endpoint
}

// Streams returns the streams of the call, one per m-line, in order.
func (c *Call) Streams() []Stream {
	c.mu.Lock()
	defer c.mu.Unlock()

	m := c.leg.m
	out := make([]Stream, len(m.streams))

	for i, s := range m.streams {
		st := Stream{Index: i, Kind: s.kind, Active: s.active()}

		if m.local != nil && i < len(m.local.Media) {
			st.Direction = m.local.MediaDirection(i)
			st.Local, _ = m.local.RTPEndpoint(i)
		}

		if m.remote != nil && i < len(m.remote.Media) {
			st.Remote, _ = m.remote.RTPEndpoint(i)
		}

		out[i] = st
	}

	return out
}

// Stream returns the active stream of the kind.
func (c *Call) Stream(kind string) (Stream, bool) {
	for _, s := range c.Streams() {
		if s.Active && s.Kind == kind {
			return s, true
		}
	}

	return Stream{}, false
}

func (c *Call) PreconditionsMet() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.leg.m.met()
}

// SessionTimer is the session timer of the call (RFC 4028): its interval, and whether the UE refreshes.
func (c *Call) SessionTimer() (time.Duration, bool) {
	c.mu.Lock()
	s := c.leg.s
	c.mu.Unlock()

	if s == nil {
		return 0, false
	}

	return s.SessionTimer()
}

func (c *Call) event(e Event) {
	select {
	case c.events <- e:
	default:
	}
}

func (c *Call) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Call) waitFor(ctx context.Context, cond func() bool) error {
	for {
		c.mu.Lock()

		if cond() {
			c.mu.Unlock()
			return nil
		}

		ch := c.changed
		c.mu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// holdSALocked keeps the SAs of the call's setup until it completes (TS 33.203 §7.4).
func (c *Call) holdSALocked() {
	if c.u.cfg.Plain || c.saHeld {
		return
	}

	c.saHeld = true

	c.u.mu.Lock()
	c.u.pending++
	c.u.mu.Unlock()
}

func (c *Call) releaseSALocked() {
	if !c.saHeld {
		return
	}

	c.saHeld = false

	c.u.mu.Lock()
	defer c.u.mu.Unlock()

	c.u.pending--
	c.u.dropOldLocked()
}

func (c *Call) terminateLocked(reason EndReason) {
	if c.state == CallTerminated {
		return
	}

	c.state, c.end = CallTerminated, reason

	for _, l := range c.legs {
		l.released = l.released || l != c.leg
	}

	c.releaseSALocked()

	close(c.done)
	c.notifyLocked()

}

// legLocked is the leg of a session of the call, made for the first response of its dialog: the call's own for the
// first one, a fork of the initial media for the others (RFC 3264 §4).
func (c *Call) legLocked(s *ua.Session) *leg {
	if s == nil {
		return nil
	}

	for _, l := range c.legs {
		if l.s == s {
			return l
		}
	}

	if c.state == CallTerminated {
		return nil
	}

	l := c.leg
	if l.s != nil {
		l = &leg{m: c.initial.fork()}
	}

	l.s = s
	c.legs = append(c.legs, l)

	if c.leg.released {
		c.leg = l
	}

	if c.state == CallInit {
		c.state = CallEarly
	}

	return l
}

// releaseLocked ends an early dialog that will not carry the call; the call goes on with another one.
func (c *Call) releaseLocked(l *leg) {
	l.released = true

	if c.leg != l {
		return
	}

	for _, other := range c.legs {
		if !other.released {
			c.leg = other
			return
		}
	}
}

// RFC 3311 §5.1, §5.2
func (c *Call) remoteOfferLocked() bool {
	return c.incoming && c.offer != nil && !c.answered
}

// TS 24.229 §5.1.2A.1.1
func (u *UE) callContact() string {
	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	return sip.Address{URI: uri, Params: append(sip.Params{{Name: "+g.3gpp.icsi-ref", Value: icsiMMTel}}, u.mediaTags()...)}.String()
}

// mediaTags are the media feature tags of the Contact the UE registers and uses in its dialogs (RFC 3840 §9,
// TS 24.229 §5.1.3.1): video when the UE takes video, whether or not the call has video (IR.94 §2.2.1, §2.2.2).
func (u *UE) mediaTags() sip.Params {
	if u.cfg.Video {
		return sip.Params{{Name: "audio"}, {Name: "video"}}
	}

	return sip.Params{{Name: "audio"}}
}

// IR.92 §2.2.4, IR.94 §2.2.2: a video call asks for a video capable callee, to guide forking (RFC 3841 §9.2).
func acceptContact(video bool) string {
	ac := "*;+g.3gpp.icsi-ref=" + icsiMMTel
	if video {
		ac += ";video"
	}

	return ac
}

// RFC 3261 §8.1.2, RFC 3263 §4.1
func (u *UE) requestTransport(req *sip.Request) sip.Transport {
	if route, err := req.Header.TopRoute(); err == nil {
		if v, ok := route.URI.Params.Get("transport"); ok && v != "" {
			return sip.Transport(strings.ToUpper(v))
		}
	}

	return u.cfg.Transport
}

// prepare completes a request of the UE for its flow (TS 24.229 §5.1.2A.1): the agent's requests and the UE's own.
func (u *UE) prepare(req *sip.Request) error {
	flow, verify, err := u.requestFlow(u.requestTransport(req))
	if err != nil {
		return err
	}

	req.Flow = flow

	via := sip.NewVia(flow.Transport, netip.AddrPortFrom(u.cfg.Local, u.port(!u.cfg.Plain)))
	via.Params.Set("rport", "")

	req.Header.Prepend("Via", via.String())

	if u.cfg.Plain {
		return nil
	}

	for _, v := range verify {
		req.Header.Add("Security-Verify", v)
	}

	req.Header.Add("Require", "sec-agree")
	req.Header.Add("Proxy-Require", "sec-agree")

	if req.Method != "ACK" {
		req.Header.Add("P-Access-Network-Info", u.cfg.AccessNetworkInfo)
	}

	return nil
}

// respondHeaders completes a response of the agent (TS 24.229 §5.1.2A.2).
func (u *UE) respondHeaders(_ *sip.Request, res *sip.Response) {
	if !u.cfg.Plain && !res.Header.Has("P-Access-Network-Info") {
		res.Header.Add("P-Access-Network-Info", u.cfg.AccessNetworkInfo)
	}
}

// response is a response of the UE outside the agent (TS 24.229 §5.1.2A.2).
func (u *UE) response(req *sip.Request, code int) *sip.Response {
	res := sip.NewResponse(req, code, "")
	u.respondHeaders(req, res)

	return res
}

// handler is the handler of the sessions of the call.
func (c *Call) handler() ua.Handler {
	return ua.Handler{
		Progress:    c.progress,
		Established: c.established,
		Answer:      c.answerReceived,
		Offer:       c.offerReceived,
		Pracked:     c.pracked,
		Request:     c.request,
		Cancelled:   func(*ua.Session, *ua.Request, *sip.Request) {},
		Received:    c.received,
		Ended:       c.ended,
		Failed:      c.failed,
	}
}

// received makes an Event of a message of the remote end the call took, a copy: the requests and responses of its
// dialogs, as the agent handled them, without retransmissions or messages it refused before the call saw them.
func (c *Call) received(_ *ua.Session, m sip.Message) {
	switch m := m.(type) {
	case *sip.Request:
		c.event(Event{Request: m.Clone()})
	case *sip.Response:
		c.event(Event{Response: m.Clone()})
	}
}

// TS 24.229 §5.1.3.1, IR.92 §2.4
func (u *UE) Invite(target string, opts CallOptions) (*Call, error) {
	if opts.Preconditions && u.cfg.NoPreconditions {
		return nil, fmt.Errorf("%w: preconditions on a UE without them", ErrCallState)
	}

	uri, err := sip.ParseURI(target)
	if err != nil {
		return nil, fmt.Errorf("testue: target: %w", err)
	}

	u.mu.Lock()
	closed, impu, route := u.closed, u.state.DefaultIMPU, slices.Clone(u.state.ServiceRoute)
	u.mu.Unlock()

	if closed {
		return nil, ErrClosed
	}

	if impu == "" {
		impu = u.id.impu
	}

	preloaded, err := u.outboundRoute()
	if err != nil {
		return nil, err
	}

	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("Route", "<"+preloaded.String()+">")

	for _, r := range route {
		req.Header.Add("Route", r)
	}

	callID := timeUUID() + "@" + sip.FormatHost(u.cfg.Local)

	c, err := u.newCall(false, req, callID, opts.Preconditions, opts.Video)
	if err != nil {
		return nil, err
	}

	req.Header.Add("From", "<"+impu+">")
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Call-ID", callID)
	req.Header.Add("Contact", u.callContact())
	req.Header.Add("Accept-Contact", acceptContact(opts.Video))
	req.Header.Add("P-Preferred-Identity", "<"+impu+">")
	req.Header.Add("P-Preferred-Service", mmtelService)
	req.Header.Add("P-Early-Media", sip.EarlyMediaSupported)
	req.Header.Add("Accept", sdp.ContentType+", application/3gpp-ims+xml")

	if !opts.NoSessionTimer {
		se := opts.SessionExpires
		if se <= 0 {
			se = DefaultSessionExpires
		}

		req.Header.Add("Session-Expires", seconds(se))
	}

	offer, err := c.leg.m.offer()
	if err != nil {
		return nil, err
	}

	c.initial = c.leg.m.fork()

	req.SetBody(sdp.ContentType, offer.Bytes())

	if opts.Preconditions {
		req.Header.Add("Supported", sip.OptionTagPrecondition)
	}

	for _, f := range opts.Headers {
		req.Header.Set(f.Name, f.Value)
	}

	c.mu.Lock()
	c.holdSALocked()
	c.mu.Unlock()

	o, err := u.agent.Invite(req, c.handler())
	if err != nil {
		c.mu.Lock()
		c.terminateLocked(TimedOut)
		c.mu.Unlock()

		return nil, fmt.Errorf("testue: send INVITE: %w", err)
	}

	sent := o.Request()

	c.mu.Lock()
	c.out, c.invite = o, sent
	c.mu.Unlock()

	return c, nil
}

func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

func reliable(res *sip.Response) bool {
	return res.Header.HasOptionTag("Require", sip.OptionTag100rel)
}

// progress handles a provisional response to an INVITE of the UE: of an outgoing call, an early dialog and its
// answer; then, of the call or of a re-INVITE, the UPDATE that reports the resources of the UE once the remote end
// has its preconditions (RFC 3312 §5, TS 24.229 §6.1.4.2).
func (c *Call) progress(s *ua.Session, res *sip.Response) {
	c.mu.Lock()

	l := c.legLocked(s)
	if l == nil || l.released || c.state != CallEarly && c.state != CallConfirmed {
		c.mu.Unlock()
		return
	}

	// An answer in an unreliable 1xx is a preview of the one to come (RFC 3261 §13.2.1); a reliable one came as one.
	if c.state == CallEarly && !reliable(res) && l.m.remote == nil {
		if err := answerIn(l.m, res.Header.ContentType(), res.Body); err != nil {
			c.event(Event{Err: err})
		}
	}

	update := reliable(res) && l.m.precondition && !l.m.met() && l.m.remote != nil && !l.offering && !l.reporting
	l.reporting = l.reporting || update

	c.notifyLocked()
	c.mu.Unlock()

	if update {
		go c.background(func(ctx context.Context) error { return c.reportResources(ctx, l) })
	}
}

// reportResources sends the UPDATE reporting the resources of the leg (RFC 3312 §5), the one trigger that set
// reporting.
func (c *Call) reportResources(ctx context.Context, l *leg) error {
	defer func() {
		c.mu.Lock()
		l.reporting = false
		c.mu.Unlock()
	}()

	return c.update(ctx, l, true, nil)
}

// established handles the 2xx of an outgoing call, or the ACK of an incoming one.
func (c *Call) established(s *ua.Session, m sip.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.incoming {
		c.acked = true
		c.releaseSALocked()
		c.notifyLocked()

		return
	}

	l := c.legLocked(s)
	if l == nil {
		return
	}

	res, _ := m.(*sip.Response)

	c.leg, c.final = l, res
	c.releaseSALocked()

	for _, other := range c.legs {
		if other != l {
			other.released = true
		}
	}

	if c.state != CallTerminated {
		c.state = CallConfirmed
	}

	c.notifyLocked()
}

// answerReceived applies the answer of the remote end to the last offer of the UE on the dialog.
func (c *Call) answerReceived(s *ua.Session, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	l := c.legLocked(s)
	if l == nil {
		return
	}

	if err := answerIn(l.m, sdp.ContentType, body); err != nil {
		c.event(Event{Err: err})
	}

	l.offering = false

	c.notifyLocked()
}

// offerReceived answers an offer of the remote end in a response (RFC 3262 §5, RFC 3261 §13.2.1).
func (c *Call) offerReceived(s *ua.Session, o *ua.Offer) {
	c.mu.Lock()

	l := c.legLocked(s)
	if l == nil {
		c.mu.Unlock()
		o.Reject()

		return
	}

	offer, err := sdp.Parse(o.SDP())

	var answer *sdp.Session

	if err == nil {
		answer, err = l.m.answer(offer)
	}

	c.notifyLocked()
	c.mu.Unlock()

	if err != nil {
		c.event(Event{Err: err})
		o.Reject()

		return
	}

	_ = o.Answer(ua.Change{Bodies: []sip.BodyPart{ua.SDP(answer.Bytes())}})
}

// SetAutoRefresh turns the session refreshes of the call on or off, those of the UE as refresher (RFC 4028 §7.4):
// off, a fault, the remote end ends the call before the session expires (§10).
func (c *Call) SetAutoRefresh(on bool) {
	c.mu.Lock()

	var sessions []*ua.Session

	for _, l := range c.legs {
		if l.s != nil {
			sessions = append(sessions, l.s)
		}
	}

	o := c.out

	c.mu.Unlock()

	p := ua.ClientPolicy
	p.SkipRefreshes = !on

	// The sessions of dialogs the INVITE creates later, its forks, too.
	if o != nil {
		o.SetPolicy(p)
	}

	for _, s := range sessions {
		s.SetPolicy(p)
	}
}

func (c *Call) pracked(*ua.Session, *sip.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pracks++
	c.notifyLocked()
}

// request answers a request of the remote end within a dialog of the call: an offer in a re-INVITE or an UPDATE gets
// the answer of the UE (RFC 3264 §8); other requests a 200. A released early dialog takes none; another live early
// dialog than the call's only an UPDATE.
func (c *Call) request(s *ua.Session, r *ua.Request) {
	req := r.Message()

	c.mu.Lock()

	l := c.legLocked(s)
	rp := ua.Reply{Code: 200}
	offerBytes, hasOffer := r.Offer()
	unchanged := false

	switch {
	case l == nil || l.released || c.state == CallTerminated:
		rp.Code = 481
	case !c.incoming && l != c.leg && req.Method != "UPDATE":
		rp.Code = 481
	case (hasOffer || req.Method == "INVITE") && c.refuse:
		rp.Code = 491
	case r.Refresh():
		// RFC 3264 §8: the session unchanged, as the offer a re-INVITE without one asks for.
		unchanged = true
	case hasOffer:
		rp = c.answerLocked(l, req, offerBytes)
	}

	c.notifyLocked()
	c.mu.Unlock()

	var err error
	if unchanged {
		err = r.AnswerUnchanged(ua.Change{})
	} else {
		err = r.Respond(rp)
	}

	// A request is always answered: one the UE could not answer as it meant to gets a 500.
	if err != nil {
		c.event(Event{Err: err})
		_ = r.Reject(500)
	}
}

// answerLocked is the answer of the UE to an offer of the remote end within the dialog of l.
func (c *Call) answerLocked(l *leg, req *sip.Request, body []byte) ua.Reply {
	offer, err := sdp.Parse(body)
	if err != nil {
		return ua.Reply{Code: 400}
	}

	answer, err := l.m.answer(offer)

	switch {
	case errors.Is(err, ErrNoCodec):
		return ua.Reply{Code: 488}
	case err != nil:
		return ua.Reply{Code: 500}
	}

	rp := ua.Reply{Code: 200, Bodies: []sip.BodyPart{ua.SDP(answer.Bytes())}}

	if l.m.precondition && (has(req.Header, "Supported", "precondition") || has(req.Header, "Require", "precondition")) {
		rp.Header = sip.Header{{Name: "Require", Value: "precondition"}}
	}

	return rp
}

// ended handles the end of a session of the call: the call's own ends the call; an early dialog of an outgoing call
// that ends alone is released (RFC 6228 §4, RFC 3261 §12.2.1.2).
func (c *Call) ended(s *ua.Session, e ua.End) {
	c.mu.Lock()
	defer c.mu.Unlock()

	l := c.legLocked(s)
	if l == nil {
		return
	}

	if !c.incoming && (l != c.leg || c.state == CallEarly && e.Cause != ua.EndCancelled && e.Cause != ua.EndRejected &&
		e.Cause != ua.EndTimeout && e.Cause != ua.EndClosed) {
		c.releaseLocked(l)
		c.notifyLocked()

		return
	}

	switch {
	case e.Request != nil:
		c.reasons, _ = e.Request.Header.Reasons()
	case e.Response != nil:
		c.reasons, _ = e.Response.Header.Reasons()
	}

	c.endErr = e.Err

	reason := TimedOut

	switch e.Cause {
	case ua.EndBye:
		reason = c.byeEnd
		if e.Remote {
			reason = RemoteBye
		}
	case ua.EndCancelled:
		// The user's cancel, with its reason; one of the agent, as it closes, is a cancel too.
		reason = Cancelled
		if !c.incoming && c.cancelled {
			reason = c.cancelEnd
		}
	case ua.EndRejected:
		reason = c.rejectEnd
		if !c.incoming {
			reason = Rejected
		}
	case ua.EndExpired:
		reason = Expired
	case ua.EndClosed:
		reason = Closed
	}

	c.terminateLocked(reason)
}

// failed handles the final response other than 2xx, or the error, of the INVITE of an outgoing call.
func (c *Call) failed(_ *ua.Outgoing, res *sip.Response, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// The early dialogs ended first, the call with them: the final response is still the call's.
	c.final, c.finalErr = res, err

	defer c.notifyLocked()

	reason := Rejected

	// A CANCEL that crossed a final response other than 487 cancelled nothing: the call was rejected.
	switch {
	case errors.Is(err, ua.ErrClosed):
		reason = Closed
	case err != nil:
		reason = TimedOut
	case c.cancelled && res.StatusCode == 487:
		reason = c.cancelEnd
	}

	if res != nil {
		c.reasons, _ = res.Header.Reasons()
	}

	c.terminateLocked(reason)
}

func answerIn(m *media, contentType string, body []byte) error {
	if mediaType(contentType) != sdp.ContentType || len(body) == 0 {
		return nil
	}

	s, err := sdp.Parse(body)
	if err != nil {
		return fmt.Errorf("testue: answer: %w", err)
	}

	return m.answered(s)
}

func (c *Call) background(f func(ctx context.Context) error) {
	if err := f(context.Background()); err != nil && !errors.Is(err, ErrCallEnded) && !errors.Is(err, ua.ErrClosed) {
		c.event(Event{Err: err})
	}
}

// outcome waits for the done of a request of the agent.
type outcome struct {
	res *sip.Response
	err error
}

func waitDone(ctx context.Context, done chan outcome) (*sip.Response, error) {
	select {
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func callback() (chan outcome, func(*sip.Response, error)) {
	done := make(chan outcome, 1)
	return done, func(res *sip.Response, err error) { done <- outcome{res, err} }
}

// asError is the error of a final response: none for a 2xx.
func asError(res *sip.Response, err error) error {
	switch {
	case err != nil:
		return err
	case res != nil && !res.IsSuccess():
		return &ResponseError{Response: res}
	}

	return nil
}

func agentError(err error) error {
	switch {
	case errors.Is(err, ua.ErrState):
		return ErrCallEnded
	case errors.Is(err, ua.ErrPending):
		return fmt.Errorf("%w: an offer is pending", ErrCallState)
	}

	return err
}

// update sends an UPDATE (RFC 3311), with an offer of the session as change, when not nil, leaves it, and restores
// it when the offer fails. The agent makes it once more after a 491 (RFC 3311 §5.1).
func (c *Call) update(ctx context.Context, l *leg, withOffer bool, change func(*media) error) error {
	var (
		previous mediaState
		ch       ua.Change
	)

	c.mu.Lock()

	if l.s == nil || c.state == CallTerminated && !l.released {
		c.mu.Unlock()
		return ErrCallEnded
	}

	if withOffer {
		if l.offering || c.remoteOfferLocked() {
			c.mu.Unlock()
			return fmt.Errorf("%w: an offer is pending", ErrCallState)
		}

		previous = l.m.save()

		offer, err := c.offerLocked(l, change)
		if err != nil {
			c.mu.Unlock()
			return err
		}

		l.offering = true
		ch.Bodies = []sip.BodyPart{ua.SDP(offer.Bytes())}

		if l.m.precondition {
			ch.Header = sip.Header{{Name: "Require", Value: "precondition"}}
		}
	}

	c.mu.Unlock()

	done, f := callback()

	err := l.s.Update(ch, f)
	if err == nil {
		var res *sip.Response

		res, err = waitDone(context.WithoutCancel(ctx), done)
		err = asError(res, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if withOffer {
		l.offering = false

		if err != nil {
			l.m.restore(previous)
		}
	}

	c.notifyLocked()

	return agentError(err)
}

// offerLocked is the offer of the session as change, when not nil, leaves it.
func (c *Call) offerLocked(l *leg, change func(*media) error) (*sdp.Session, error) {
	previous := l.m.save()

	if change != nil {
		if err := change(l.m); err != nil {
			l.m.restore(previous)
			return nil, err
		}
	}

	offer, err := l.m.offer()
	if err != nil {
		l.m.restore(previous)
		return nil, err
	}

	return offer, nil
}

func (c *Call) Wait(ctx context.Context) (*sip.Response, error) {
	if c.incoming {
		return nil, fmt.Errorf("%w: Wait on an incoming call", ErrCallState)
	}

	if err := c.waitFor(ctx, func() bool { return c.final != nil || c.finalErr != nil || c.state == CallTerminated }); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case c.final != nil && c.final.IsSuccess():
		return c.final, nil
	case c.final != nil:
		return c.final, &ResponseError{Response: c.final}
	case c.finalErr != nil:
		return nil, c.finalErr
	}

	return nil, ErrCallEnded
}

// Cancel cancels the call the user ends before it is answered, with RELEASE_CAUSE 1 (TS 24.229 §5.1.3.1), as phones
// do (RFC 3261 §9.1).
func (c *Call) Cancel(ctx context.Context) error {
	return c.cancel(ctx, Cancelled, nil, userEnds())
}

// cancel cancels the INVITE (RFC 3261 §9.1), the call ending with end and the CANCEL carrying body, when not nil,
// and extra.
func (c *Call) cancel(ctx context.Context, end EndReason, body *sdp.Session, extra ...sip.Field) error {
	c.mu.Lock()

	if c.incoming || c.out == nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: Cancel on an incoming call", ErrCallState)
	}

	if c.state == CallConfirmed || c.state == CallTerminated {
		c.mu.Unlock()
		return fmt.Errorf("%w: Cancel of a %s call", ErrCallState, c.state)
	}

	c.cancelled, c.cancelEnd = true, end
	o := c.out
	c.mu.Unlock()

	ch := ua.Change{Header: sip.Header(extra)}
	if body != nil {
		ch.Bodies = []sip.BodyPart{sip.NewBodyPart(sdp.ContentType, body.Bytes())}
	}

	if err := o.Cancel(ch); err != nil {
		return fmt.Errorf("testue: CANCEL: %w", err)
	}

	return c.waitFor(ctx, func() bool { return c.state == CallTerminated })
}

// Bye ends the call the user hangs up, with RELEASE_CAUSE 1 (TS 24.229 §5.1.5), as phones do.
func (c *Call) Bye(ctx context.Context) error {
	return c.bye(ctx, LocalBye, userEnds())
}

// userEnds is the Reason of a call the user ends (TS 24.229 §7.2A.18.11).
func userEnds() sip.Field {
	r, _ := sip.NewReason(sip.ReasonReleaseCause, sip.ReleaseUserEndsCall, sip.ReasonText(sip.ReasonReleaseCause, sip.ReleaseUserEndsCall))

	return sip.Field{Name: "Reason", Value: r.String()}
}

// bye ends the confirmed call with a BYE carrying extra Reasons (RFC 3261 §15.1.1, RFC 3326), and waits for its
// response: any, which ends the BYE, or the error of its transaction.
func (c *Call) bye(ctx context.Context, reason EndReason, extra ...sip.Field) error {
	c.mu.Lock()

	s := c.leg.s

	switch {
	case c.state == CallTerminated:
		c.mu.Unlock()
		return ErrCallEnded
	case c.state != CallConfirmed || s == nil:
		state := c.state
		c.mu.Unlock()

		return fmt.Errorf("%w: BYE in a %s call", ErrCallState, state)
	}

	c.byeEnd = reason
	c.terminateLocked(reason)
	c.mu.Unlock()

	done := make(chan error, 1)

	if err := s.Bye(ua.Change{Header: sip.Header(extra)}, func(_ *sip.Response, err error) { done <- err }); err != nil {
		if errors.Is(err, ua.ErrState) {
			return nil
		}

		return err
	}

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RFC 3264 §8.4, IR.94 §2.3.2: every stream of the call is held, and resumed, in one offer.
func (c *Call) Hold(ctx context.Context) error {
	return c.reinvite(ctx, func(m *media) error {
		m.setDirection(sdp.SendOnly)
		return nil
	})
}

func (c *Call) Resume(ctx context.Context) error {
	return c.reinvite(ctx, func(m *media) error {
		m.setDirection(sdp.SendRecv)
		return nil
	})
}

// Refresh refreshes the session with an UPDATE without an offer (RFC 4028 §7.4, IR.92 §2.2.8); the agent refreshes
// on its own before the session expires.
func (c *Call) Refresh(ctx context.Context) error {
	c.mu.Lock()
	l := c.leg
	c.mu.Unlock()

	return c.update(ctx, l, false, nil)
}

// reinvite offers the session as change leaves it (RFC 3261 §14.1), and restores it when the offer fails. A stream it
// added has its resources reported in an UPDATE once it completes (RFC 3312 §5). The agent makes it once more after
// a 491.
func (c *Call) reinvite(ctx context.Context, change func(*media) error) error {
	// RFC 3264 §4: the offer waits for the one in progress, such as an UPDATE of the call's setup still unanswered.
	if err := c.waitFor(ctx, func() bool {
		return c.state == CallTerminated || !c.leg.offering && !c.remoteOfferLocked()
	}); err != nil {
		return err
	}

	c.mu.Lock()

	l := c.leg
	if c.state != CallConfirmed || l.s == nil || l.offering {
		state := c.state
		c.mu.Unlock()

		return fmt.Errorf("%w: re-INVITE in a %s call, or during another offer", ErrCallState, state)
	}

	previous := l.m.save()

	offer, err := c.offerLocked(l, change)
	if err != nil {
		c.mu.Unlock()
		return err
	}

	l.offering = true

	ch := ua.Change{Bodies: []sip.BodyPart{ua.SDP(offer.Bytes())}}
	if l.m.precondition {
		ch.Header = sip.Header{{Name: "Require", Value: "precondition"}}
	}

	c.mu.Unlock()

	done, f := callback()

	err = l.s.Reinvite(ch, f)
	if err == nil {
		var res *sip.Response

		res, err = waitDone(context.WithoutCancel(ctx), done)
		err = asError(res, err)
	}

	c.mu.Lock()

	// The answer cleared the offer on success; an UPDATE reporting the resources may have made its own since.
	if err != nil {
		l.offering = false
		l.m.restore(previous)
	}

	update := err == nil && c.state == CallConfirmed && c.leg == l && l.m.precondition && !l.m.met() && !l.offering &&
		!l.reporting
	l.reporting = l.reporting || update

	c.notifyLocked()
	c.mu.Unlock()

	if err != nil {
		return agentError(err)
	}

	if update {
		return c.reportResources(ctx, l)
	}

	return nil
}

// AddVideo adds video to the call (IR.94 §2.2.2), in a re-INVITE.
func (c *Call) AddVideo(ctx context.Context) error {
	return c.reinvite(ctx, (*media).addVideo)
}

// RemoveVideo removes the video of the call, setting its port to zero in a re-INVITE (IR.94 §2.2.2).
func (c *Call) RemoveVideo(ctx context.Context) error {
	return c.reinvite(ctx, func(m *media) error { return m.remove(sdp.Video) })
}

// BearerLost is what the UE does when the bearer, or QoS flow, of the call's stream of the kind (sdp.Audio or
// sdp.Video) is released or refused (TS 24.229 §6.1.1, IR.94 §2.4.1, NG.114 §4.6.2): a lost video stream is removed
// and the call goes on, in an UPDATE before the call is answered and a re-INVITE after; the call ends when it has
// no voice left. A stream the call does not have has nothing to lose.
func (c *Call) BearerLost(ctx context.Context, kind string) error {
	c.mu.Lock()
	l := c.leg
	i := l.m.find(kind)
	voice := slices.ContainsFunc(l.m.streams, func(s *stream) bool { return s.active() && s.kind == sdp.Audio && s.kind != kind })
	terminated := c.state == CallTerminated
	c.mu.Unlock()

	switch {
	case i < 0 || terminated:
		return nil
	case !voice:
		return c.release(ctx, MediaLost, sip.ReasonReleaseCause, sip.ReleaseMediaBearerLoss)
	}

	// RFC 3264 §4, RFC 3311 §5.1: the offer removing the stream waits for the one in progress.
	var state CallState

	if err := c.waitFor(ctx, func() bool {
		state = c.state
		return state == CallTerminated || c.remoteOfferLocked() || !c.leg.offering
	}); err != nil {
		return err
	}

	remove := func(m *media) error { return m.remove(kind) }

	c.mu.Lock()
	pending := c.remoteOfferLocked()
	// The kept leg may have changed while waiting, as when another dialog of a forked INVITE was answered.
	l = c.leg
	c.mu.Unlock()

	switch {
	case state == CallTerminated:
		return nil
	case pending:
		return fmt.Errorf("%w: BearerLost before the offer of the call is answered", ErrCallState)
	case state == CallConfirmed:
		return c.reinvite(ctx, remove)
	}

	return c.update(ctx, l, true, remove)
}

// release ends the call in whatever state it is in, with the Reason (RFC 3326, TS 24.229 §5.1.3.1, §5.1.5,
// §7.2A.18.11): a BYE once confirmed, a CANCEL while the INVITE is pending, and a 580 (Precondition Failure) or, without
// preconditions, a 488 (Not Acceptable Here) to an INVITE not answered yet; a 503 would have the caller try elsewhere
// (RFC 3261 §21.5.4). The CANCEL and the 580 have the SDP of the precondition failure (RFC 3312 §8), as phones do.
func (c *Call) release(ctx context.Context, end EndReason, protocol string, cause int) error {
	reason, err := sip.NewReason(protocol, cause, sip.ReasonText(protocol, cause))
	if err != nil {
		return err
	}

	field := sip.Field{Name: "Reason", Value: reason.String()}

	c.mu.Lock()
	state, incoming, precondition := c.state, c.incoming, c.leg.m.precondition

	// RFC 3312 §8: the failure is described on the last SDP received.
	last := c.leg.m.remote
	if c.remoteOfferLocked() {
		last = c.offer
	}

	failure, err := c.leg.m.failure(last)
	c.mu.Unlock()

	if err != nil {
		return err
	}

	switch {
	case state == CallConfirmed:
		return c.bye(ctx, end, field)
	case incoming && precondition:
		return c.reject(580, end, failure, field)
	case incoming:
		return c.reject(488, end, nil, field)
	}

	return c.cancel(ctx, end, failure, field)
}

func has(h sip.Header, name, tag string) bool {
	return h.HasOptionTag(name, tag)
}

// incomingCall receives an initial INVITE (TS 24.229 §5.1.4.1): its offer has the voice of the UE, or the INVITE gets
// a 488; the call goes to Calls.
func (u *UE) incomingCall(r *ua.Request) ua.Handler {
	req := r.Message()
	u.event(Event{Request: req})

	if !u.cfg.AcceptCalls {
		_ = r.Reject(486)
		return ua.Handler{}
	}

	var offer *sdp.Session

	if body, ok := r.Offer(); ok {
		var err error

		if offer, err = sdp.Parse(body); err != nil {
			_ = r.Reject(400)
			return ua.Handler{}
		}

		if !offerAudio(offer) {
			var warning []sip.Field
			if w, err := sip.NewWarning(304, sip.FormatHost(u.cfg.Local)); err == nil {
				warning = append(warning, sip.Field{Name: "Warning", Value: w.String()})
			}

			_ = r.Reject(488, warning...)

			return ua.Handler{}
		}
	}

	precondition := !u.cfg.NoPreconditions &&
		(has(req.Header, "Supported", "precondition") || has(req.Header, "Require", "precondition")) && offerQoS(offer)

	// IR.94 §2.2.2: the offer the UE makes to an INVITE without one has every media it is able and willing to use.
	c, err := u.newCall(true, req, req.Header.CallID(), precondition, u.cfg.Video)
	if err != nil {
		_ = r.Reject(500)
		return ua.Handler{}
	}

	c.in, c.offer = r, offer
	c.rel100 = has(req.Header, "Supported", "100rel") || has(req.Header, "Require", "100rel")
	c.leg.s = r.Session()
	c.legs = []*leg{c.leg}
	c.rejectEnd = Rejected

	c.mu.Lock()
	c.holdSALocked()
	c.mu.Unlock()

	select {
	case u.calls.incoming <- c:
	default:
		c.mu.Lock()
		c.terminateLocked(Rejected)
		c.mu.Unlock()

		_ = r.Reject(486)

		return ua.Handler{}
	}

	return c.handler()
}

func offerAudio(offer *sdp.Session) bool {
	for _, m := range offer.Media {
		if m.Type() == sdp.Audio && m.Port() != 0 && choose(m) != nil {
			return true
		}
	}

	return false
}

func offerQoS(offer *sdp.Session) bool {
	if offer == nil {
		return false
	}

	for _, m := range offer.Media {
		pres, _ := m.Preconditions()
		if slices.ContainsFunc(pres, func(p sdp.Precondition) bool { return p.Type == sdp.QoS }) {
			return true
		}
	}

	return false
}

// responseHeaders are the header fields of a response of the UE to the INVITE of an incoming call.
func (c *Call) responseHeaders(code int) sip.Header {
	h := sip.Header{{Name: "Contact", Value: c.u.callContact()}}

	if code < 200 && c.u.cfg.EarlyMedia != "" {
		h.Add("P-Early-Media", c.u.cfg.EarlyMedia)
	}

	return h
}

// sdpLocked is the SDP of the UE for its first reliable response or its 2xx: the answer to the offer of the INVITE, or
// its own offer to an INVITE without one; none once given.
func (c *Call) sdpLocked() ([]sip.BodyPart, error) {
	if c.answered {
		return nil, nil
	}

	var (
		s   *sdp.Session
		err error
	)

	if c.offer != nil {
		s, err = c.leg.m.answer(c.offer)
	} else {
		s, err = c.leg.m.offer()
	}

	if err != nil {
		return nil, err
	}

	c.answered = true

	return []sip.BodyPart{ua.SDP(s.Bytes())}, nil
}

func (c *Call) checkIncoming(what string) error {
	switch {
	case !c.incoming:
		return fmt.Errorf("%w: %s on an outgoing call", ErrCallState, what)
	case c.state == CallTerminated:
		return ErrCallEnded
	case c.state == CallConfirmed:
		return fmt.Errorf("%w: %s on an answered call", ErrCallState, what)
	}

	return nil
}

// IR.92 §2.4.1, TS 24.229 §5.1.4.1
func (c *Call) Ring(ctx context.Context) error {
	c.mu.Lock()

	if err := c.checkIncoming("Ring"); err != nil {
		c.mu.Unlock()
		return err
	}

	rel100 := c.rel100
	c.mu.Unlock()

	if rel100 {
		if err := c.provisional(ctx, 183, true); err != nil {
			return err
		}

		c.mu.Lock()
		l := c.leg
		update := c.state == CallEarly && !l.m.met() && l.m.remoteMet() && !l.offering
		c.mu.Unlock()

		if update {
			if err := c.update(ctx, l, true, nil); err != nil {
				return err
			}
		}

		if err := c.waitFor(ctx, func() bool { return l.m.met() || c.state == CallTerminated }); err != nil {
			return err
		}
	}

	return c.provisional(ctx, 180, has(c.invite.Header, "Require", "100rel"))
}

// provisional sends a provisional response to the INVITE of an incoming call; a reliable one waits for its PRACK
// (RFC 3262 §3).
func (c *Call) provisional(ctx context.Context, code int, reliably bool) error {
	c.mu.Lock()

	if err := c.checkIncoming("provisional response"); err != nil {
		c.mu.Unlock()
		return err
	}

	rp := ua.Reply{Code: code, Header: c.responseHeaders(code), Reliable: reliably}

	if reliably {
		bodies, err := c.sdpLocked()
		if err != nil {
			c.mu.Unlock()
			return err
		}

		rp.Bodies = bodies

		if c.leg.m.precondition {
			rp.Header.Add("Require", "precondition")
		}
	}

	if c.state == CallInit {
		c.state = CallEarly
	}

	want := c.pracks + 1
	in := c.in
	c.notifyLocked()
	c.mu.Unlock()

	if err := in.Progress(rp); err != nil {
		return fmt.Errorf("testue: %d: %w", code, agentError(err))
	}

	if !reliably {
		return nil
	}

	if err := c.waitFor(ctx, func() bool { return c.pracks >= want || c.state == CallTerminated }); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case c.pracks >= want:
		return nil
	case errors.Is(c.endErr, ua.ErrNoPrack):
		return ErrNoPrack
	}

	return ErrCallEnded
}

// Answer answers an incoming call with a 200, and waits for its ACK.
func (c *Call) Answer(ctx context.Context) error {
	c.mu.Lock()

	if err := c.checkIncoming("Answer"); err != nil {
		c.mu.Unlock()
		return err
	}

	rp := ua.Reply{Code: 200, Header: c.responseHeaders(200)}

	bodies, err := c.sdpLocked()
	if err != nil {
		c.mu.Unlock()
		return err
	}

	rp.Bodies = bodies

	// RFC 4028 §9, TS 24.229 §5.1.4.1: the UE asks for a session timer the caller supports but did not ask for.
	if req := c.invite; !req.Header.Has("Session-Expires") && has(req.Header, "Supported", "timer") {
		interval := DefaultSessionExpires

		if v, ok, err := req.Header.MinSE(); ok && err == nil {
			interval = max(interval, time.Duration(v)*time.Second)
		}

		rp.Header.Add("Session-Expires", seconds(interval)+";refresher=uac")
	}

	c.state = CallConfirmed
	in := c.in
	c.notifyLocked()
	c.mu.Unlock()

	if err := in.Respond(rp); err != nil {
		return fmt.Errorf("testue: 200: %w", agentError(err))
	}

	if err := c.waitFor(ctx, func() bool { return c.acked || c.end != NotEnded }); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case c.acked:
		return nil
	case errors.Is(c.endErr, ua.ErrNoAck):
		return ErrNoAck
	}

	return ErrCallEnded
}

func (c *Call) Reject(code int) error {
	return c.reject(code, Rejected, nil)
}

// reject answers the INVITE with code, the call ending with end and the response carrying body, when not nil, and
// extra.
func (c *Call) reject(code int, end EndReason, body *sdp.Session, extra ...sip.Field) error {
	if code < 300 || code > 699 {
		return fmt.Errorf("testue: Reject with %d", code)
	}

	c.mu.Lock()

	if err := c.checkIncoming("Reject"); err != nil {
		c.mu.Unlock()
		return err
	}

	rp := ua.Reply{Code: code, Header: extra}
	if body != nil {
		rp.Bodies = []sip.BodyPart{ua.SDP(body.Bytes())}
	}

	c.rejectEnd = end
	c.terminateLocked(end)

	in := c.in
	c.mu.Unlock()

	return agentError(in.Respond(rp))
}

// RefuseOffers has the call answer the re-INVITEs and offers of the remote end with a 491, as when one of its own were
// pending (RFC 3261 §14.2): a fault, for tests of the remote end's glare handling.
func (c *Call) RefuseOffers(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.refuse = on
}
