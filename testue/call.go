package testue

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	DefaultSessionExpires = 1800 * time.Second

	mmtelService = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

	allow = "INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE"

	firstMediaPort = 40000

	// Each call takes four ports: audio RTP and RTCP, then video RTP and RTCP.
	portsPerCall = 4
	maxCalls     = 5000
)

var supportedTags = []string{"100rel", "timer", "precondition", "sec-agree"}

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

type callKey struct {
	callID, tag string
}

type Call struct {
	u        *UE
	incoming bool
	key      callKey
	events   chan Event
	done     chan struct{}

	out sync.Mutex

	mu      sync.Mutex
	changed chan struct{}
	state   CallState
	end     EndReason
	reasons []sip.Reason
	invite  *sip.Request

	// leg is the call's dialog: the only one of an incoming call; for an outgoing call, the
	// answered one, or before the answer the earliest early dialog still alive.
	leg *leg

	// legs holds every dialog the call created. An outgoing INVITE forked downstream creates
	// one per callee that responds (RFC 3261 §12.1.2, §13.2.2.4).
	legs []*leg

	// initial is the media state just after the initial offer, from which each forked
	// dialog's own offer/answer exchange starts (RFC 3264 §4).
	initial *media

	saHeld bool

	itx       *transaction.ClientTransaction
	final     *sip.Response
	finalErr  error
	ackSent   <-chan struct{}
	cancelled bool
	// cancelEnd and cancelReason are the end reason of a cancelled call, and the Reason of its CANCEL, given again
	// in the BYE of a 2xx that crosses the CANCEL.
	cancelEnd    EndReason
	cancelReason []sip.Field
	retried      bool

	stx      *transaction.ServerTransaction
	offer    *sdp.Session
	answered bool
	rel100   bool
	rseqOut  uint32
	unacked  *reliable
	accepted *accepted

	interval  time.Duration
	refresher bool
	auto      bool
	timer     *time.Timer
}

// leg is one dialog of a call, with its own offer/answer and reliable-provisional state.
type leg struct {
	d *dialog.Dialog
	m *media

	offering bool

	rseq     uint32
	haveRSeq bool

	ack *sip.Request

	// released is set once the early dialog ended without the call: by a 199 (RFC 6228 §4),
	// or because another dialog was answered (RFC 3261 §13.2.2.4).
	released bool
}

func (l *leg) tag() string {
	if l.d == nil {
		return ""
	}

	return l.d.ID().RemoteTag
}

type reliable struct {
	res      *sip.Response
	rseq     uint32
	interval time.Duration
	timer    *time.Timer
	started  time.Time

	offer bool
}

type accepted struct {
	stx      *transaction.ServerTransaction
	res      *sip.Response
	cseq     uint32
	interval time.Duration
	timer    *time.Timer
	started  time.Time
	acked    bool
	initial  bool

	answerInAck bool
}

// newCall starts a call whose first offer, if the UE makes it, has video when video is set.
func (u *UE) newCall(incoming bool, invite *sip.Request, key callKey, precondition, video bool) (*Call, error) {
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
		key:      key,
		events:   make(chan Event, 256),
		done:     make(chan struct{}),
		changed:  make(chan struct{}),
		invite:   invite,
		leg:      &leg{m: m},
		auto:     true,
	}, nil
}

func (u *UE) Calls() <-chan *Call { return u.calls.incoming }

func (c *Call) Incoming() bool { return c.incoming }

func (c *Call) Invite() *sip.Request {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.invite.Clone()
}

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

// Reasons returns the Reason header field values (RFC 3326) of the CANCEL, BYE or final
// response that ended the call.
func (c *Call) Reasons() []sip.Reason {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.reasons)
}

func (c *Call) ID() dialog.ID {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.leg.d == nil {
		return dialog.ID{}
	}

	return c.leg.d.ID()
}

// EarlyDialogs returns the early dialogs of an outgoing call that are still alive: one per
// callee ringing when the INVITE was forked (RFC 3261 §13.2.2.4).
func (c *Call) EarlyDialogs() []dialog.ID {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []dialog.ID

	for _, l := range c.legs {
		if !l.released && l.d.State() == dialog.Early {
			out = append(out, l.d.ID())
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

func (c *Call) SessionTimer() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.interval, c.refresher
}

func (c *Call) SetAutoRefresh(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.auto = on
	c.sessionTimerLocked(c.interval, c.refresher)
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
		if !l.released {
			l.d.Terminate()
		}
	}

	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}

	if c.unacked != nil {
		c.unacked.timer.Stop()
		c.unacked = nil
	}

	if c.accepted != nil {
		c.accepted.timer.Stop()
		c.accepted = nil
	}

	c.releaseSALocked()

	close(c.done)
	c.notifyLocked()

	c.u.mu.Lock()
	if c.u.calls.active[c.key] == c {
		delete(c.u.calls.active, c.key)
	}
	c.u.mu.Unlock()
}

func (c *Call) terminate(reason EndReason) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.terminateLocked(reason)
}

// RFC 3311 §5.1, §5.2
func (c *Call) localOfferLocked(l *leg) bool {
	return l.offering || (c.unacked != nil && c.unacked.offer) || (c.accepted != nil && c.accepted.answerInAck)
}

func (c *Call) remoteOfferLocked() bool {
	return c.incoming && c.offer != nil && !c.answered
}

// RFC 4028 §7.4
func (c *Call) supportedLocked() string {
	if c.leg.m.precondition {
		return "100rel, timer, precondition"
	}

	return "100rel, timer"
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

// TS 24.229 §5.1.2A.1
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

// TS 24.229 §5.1.2A.2
func (u *UE) response(req *sip.Request, code int) *sip.Response {
	res := sip.NewResponse(req, code, "")

	if !u.cfg.Plain {
		res.Header.Add("P-Access-Network-Info", u.cfg.AccessNetworkInfo)
	}

	return res
}

// RFC 3261 §14.2, RFC 3311 §5.2
func retryAfter(res *sip.Response) *sip.Response {
	var b [1]byte

	_, _ = rand.Read(b[:])

	res.Header.Add("Retry-After", strconv.Itoa(int(b[0])%11))

	return res
}

// TS 24.229 §5.1.3.1, IR.92 §2.4
func (u *UE) Invite(target string, opts CallOptions) (*Call, error) {
	uri, err := sip.ParseURI(target)
	if err != nil {
		return nil, fmt.Errorf("testue: target: %w", err)
	}

	u.mu.Lock()
	closed, impu, route := u.closed, u.state.DefaultIMPU, u.state.ServiceRoute
	u.mu.Unlock()

	if closed {
		return nil, ErrClosed
	}

	if impu == "" {
		impu = u.id.impu
	}

	req := sip.NewRequest("INVITE", uri)
	if err := u.prepare(req); err != nil {
		return nil, err
	}

	req.Header.Add("Route", "<"+preloadedRoute(req.Flow).String()+">")

	for _, r := range route {
		req.Header.Add("Route", r)
	}

	tag := sip.NewTag()
	callID := timeUUID() + "@" + sip.FormatHost(u.cfg.Local)

	c, err := u.newCall(false, req, callKey{callID: callID, tag: tag}, opts.Preconditions, opts.Video)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<"+impu+">;tag="+tag)
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Call-ID", callID)
	req.Header.Add("CSeq", "1 INVITE")
	req.Header.Add("Contact", u.callContact())
	req.Header.Add("Accept-Contact", acceptContact(opts.Video))
	req.Header.Add("P-Preferred-Identity", "<"+impu+">")
	req.Header.Add("P-Preferred-Service", mmtelService)
	req.Header.Add("P-Early-Media", sip.EarlyMediaSupported)
	req.Header.Add("Supported", c.supportedLocked()+", 199")
	req.Header.Add("Allow", allow)
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

	for _, f := range opts.Headers {
		req.Header.Set(f.Name, f.Value)
	}

	u.mu.Lock()
	u.calls.active[c.key] = c
	u.mu.Unlock()

	c.mu.Lock()
	c.holdSALocked()
	c.mu.Unlock()

	itx, err := u.layer.Request(req, &inviteClient{c: c})
	if err != nil {
		c.terminate(TimedOut)
		return nil, fmt.Errorf("testue: send INVITE: %w", err)
	}

	c.mu.Lock()
	c.itx = itx
	c.mu.Unlock()

	return c, nil
}

func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

type inviteClient struct {
	c *Call
}

func (h *inviteClient) HandleResponse(res *sip.Response) {
	c := h.c
	c.event(Event{Response: res})

	switch {
	case res.StatusCode == 100:
	case res.StatusCode == 199:
		c.earlyTerminated(res)
	case res.IsProvisional():
		c.provisionalReceived(res)
	case res.IsSuccess():
		c.successReceived(res)
	case res.StatusCode == 422 && c.retryInterval(res):
	default:
		c.mu.Lock()
		defer c.mu.Unlock()

		c.final = res
		c.reasons, _ = res.Header.Reasons()

		reason := Rejected
		if c.cancelled && res.StatusCode == 487 {
			reason = c.cancelEnd
		}

		c.terminateLocked(reason)
	}
}

func (h *inviteClient) HandleError(err error) {
	c := h.c
	c.event(Event{Err: err})

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.final != nil {
		return
	}

	c.finalErr = err

	reason := TimedOut
	if c.cancelled {
		reason = c.cancelEnd
	}

	c.terminateLocked(reason)
}

// RFC 4028 §7.4, IR.92 §2.2.8
func (c *Call) retryInterval(res *sip.Response) bool {
	minSE, err := strconv.ParseUint(strings.TrimSpace(strings.Split(res.Header.Get("Min-SE"), ";")[0]), 10, 32)

	c.mu.Lock()

	if err != nil || c.retried || c.cancelled || c.state != CallInit {
		c.mu.Unlock()
		return false
	}

	c.retried = true

	req := c.invite.Clone()
	cseq, _ := req.Header.CSeq()

	if via, err := req.Header.TopVia(); err == nil {
		via.Params.Set("branch", sip.NewBranch())
		_ = req.Header.SetTopVia(via)
	}

	req.Header.Set("CSeq", sip.CSeq{Seq: cseq.Seq + 1, Method: "INVITE"}.String())
	req.Header.Set("Session-Expires", strconv.FormatUint(minSE, 10))
	req.Header.Set("Min-SE", strconv.FormatUint(minSE, 10))

	c.invite = req
	c.mu.Unlock()

	itx, err := c.u.layer.Request(req, &inviteClient{c: c})
	if err != nil {
		c.mu.Lock()
		c.finalErr = err
		c.terminateLocked(TimedOut)
		c.mu.Unlock()

		return true
	}

	c.mu.Lock()
	c.itx = itx
	c.mu.Unlock()

	return true
}

func (c *Call) legLocked(tag string) *leg {
	for _, l := range c.legs {
		if l.tag() == tag {
			return l
		}
	}

	return nil
}

// RFC 3261 §12.1.2, §13.2.2.4: each To tag in a response to the INVITE is a separate dialog.
func (c *Call) dialogLocked(res *sip.Response) (*leg, error) {
	if l := c.legLocked(dialog.ResponseID(res).RemoteTag); l != nil {
		return l, l.d.ReceiveResponse(res)
	}

	d, err := dialog.NewUAC(c.invite, res)
	if err != nil {
		return nil, err
	}

	l := c.leg
	if l.d != nil {
		l = &leg{m: c.initial.fork()}
	}

	l.d = d
	c.legs = append(c.legs, l)

	if c.leg.released {
		c.leg = l
	}

	if c.state == CallInit {
		c.state = CallEarly
	}

	return l, nil
}

// RFC 3262 §4
func (l *leg) inOrder(res *sip.Response) bool {
	if !has(res.Header, "Require", "100rel") {
		return true
	}

	rseq, err := res.Header.RSeq()
	if err != nil || (l.haveRSeq && rseq != l.rseq+1) {
		return false
	}

	l.rseq, l.haveRSeq = rseq, true

	return true
}

func (c *Call) provisionalReceived(res *sip.Response) {
	to, _ := res.Header.To()
	if to.Tag() == "" {
		return
	}

	c.mu.Lock()

	if c.state != CallInit && c.state != CallEarly {
		c.mu.Unlock()
		return
	}

	l, err := c.dialogLocked(res)
	if err != nil || l.released || !l.inOrder(res) {
		c.mu.Unlock()
		return
	}

	if err := answerReceived(l.m, res.Header.ContentType(), res.Body); err != nil {
		c.event(Event{Err: err})
	}

	c.notifyLocked()
	c.mu.Unlock()

	if has(res.Header, "Require", "100rel") {
		c.background(func(ctx context.Context) error { return c.prack(ctx, l, res) })
	}
}

// RFC 6228 §4
func (c *Call) earlyTerminated(res *sip.Response) {
	to, _ := res.Header.To()
	if to.Tag() == "" {
		return
	}

	reliable := has(res.Header, "Require", "100rel")

	c.mu.Lock()

	l := c.legLocked(to.Tag())

	switch {
	case c.state != CallInit && c.state != CallEarly:
		c.mu.Unlock()
		return
	case l == nil && reliable:
		var err error

		if l, err = c.dialogLocked(res); err != nil {
			c.mu.Unlock()
			return
		}
	case l == nil:
		c.mu.Unlock()
		return
	}

	if l.released || !l.inOrder(res) {
		c.mu.Unlock()
		return
	}

	c.releaseLocked(l)
	c.notifyLocked()
	c.mu.Unlock()

	if reliable {
		c.background(func(ctx context.Context) error { return c.prack(ctx, l, res) })
	}
}

// releaseLocked ends an early dialog that will not carry the call. Requests already under way
// on it, such as a PRACK, still complete.
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

func answerReceived(m *media, contentType string, body []byte) error {
	if m.remote != nil || mediaType(contentType) != sdp.ContentType || len(body) == 0 {
		return nil
	}

	s, err := sdp.Parse(body)
	if err != nil {
		return fmt.Errorf("testue: answer: %w", err)
	}

	return m.answered(s)
}

// RFC 3261 §13.2.2.4: every 2xx is acknowledged; the first one confirms the call, and a later one
// from another callee of a forked INVITE gets a BYE.
func (c *Call) successReceived(res *sip.Response) {
	c.mu.Lock()

	if l := c.legLocked(dialog.ResponseID(res).RemoteTag); l != nil && l.ack != nil {
		ack := l.ack.Clone()
		c.mu.Unlock()

		c.sendAck(ack)

		return
	}

	l, err := c.dialogLocked(res)
	if err != nil {
		c.event(Event{Err: err})
		c.mu.Unlock()

		return
	}

	if err := answerReceived(l.m, res.Header.ContentType(), res.Body); err != nil {
		c.event(Event{Err: err})
	}

	ack, err := l.d.NewAck(c.invite)
	if err == nil {
		err = c.u.prepare(ack)
	}

	if err != nil {
		c.event(Event{Err: err})
		c.mu.Unlock()

		return
	}

	l.ack = ack

	// A 2xx the call can no longer take, because another 2xx answered it or it ended, is acknowledged and ended
	// (RFC 3261 §13.2.2.4).
	if c.final != nil || c.state == CallTerminated && !c.cancelled {
		c.releaseLocked(l)
		c.mu.Unlock()

		c.background(func(ctx context.Context) error {
			if err := c.u.layer.SendAck(ctx, ack.Clone()); err != nil {
				return err
			}

			return c.byeLeg(ctx, l)
		})

		return
	}

	c.leg, c.final = l, res
	c.ackSent = c.sendAck(ack.Clone())
	c.releaseSALocked()

	for _, other := range c.legs {
		if other != l {
			other.released = true
		}
	}

	cancelled, sent, end, reason := c.cancelled, c.ackSent, c.cancelEnd, c.cancelReason
	if c.state != CallTerminated {
		c.state = CallConfirmed

		interval, refresher, _ := parseSessionExpires(res.Header)
		c.sessionTimerLocked(interval, refresher != "uas")
	}

	c.notifyLocked()
	c.mu.Unlock()

	// A 2xx crossing the CANCEL is acknowledged, then the call it confirms is ended (RFC 3261 §13.2.2.4,
	// §9.1): the BYE waits for the ACK.
	if cancelled {
		c.background(func(ctx context.Context) error {
			select {
			case <-sent:
			case <-ctx.Done():
				return ctx.Err()
			}

			return c.bye(ctx, end, reason...)
		})
	}
}

func (c *Call) sendAck(ack *sip.Request) <-chan struct{} {
	sent := make(chan struct{})

	err := c.u.layer.Go(func(ctx context.Context) {
		defer close(sent)

		if err := c.u.layer.SendAck(ctx, ack); err != nil {
			c.event(Event{Err: err})
		}
	})
	if err != nil {
		c.event(Event{Err: err})
		close(sent)
	}

	return sent
}

func (c *Call) background(f func(ctx context.Context) error) {
	err := c.u.layer.Go(func(ctx context.Context) {
		if err := f(ctx); err != nil && !errors.Is(err, ErrCallEnded) && !errors.Is(err, transaction.ErrClosed) {
			c.event(Event{Err: err})
		}
	})
	if err != nil && !errors.Is(err, transaction.ErrClosed) {
		c.event(Event{Err: err})
	}
}

func (c *Call) newRequest(l *leg, method string) (*sip.Request, error) {
	c.mu.Lock()

	if l.d == nil || c.state == CallTerminated && !l.released {
		c.mu.Unlock()
		return nil, ErrCallEnded
	}

	d := l.d
	c.mu.Unlock()

	req, err := d.NewRequest(method)
	if err != nil {
		return nil, fmt.Errorf("testue: %w", err)
	}

	if err := c.u.prepare(req); err != nil {
		return nil, err
	}

	if method == "INVITE" || method == "UPDATE" {
		req.Header.Add("Contact", c.u.callContact())
		req.Header.Add("Allow", allow)
	}

	return req, nil
}

func (c *Call) submit(build func() (*sip.Request, error), h transaction.ClientHandler) error {
	c.out.Lock()
	defer c.out.Unlock()

	req, err := build()
	if err != nil {
		return err
	}

	if _, err := c.u.layer.Request(req, h); err != nil {
		return fmt.Errorf("testue: send %s: %w", req.Method, err)
	}

	return nil
}

// RFC 3261 §17.1, §12.2.1.2
func (c *Call) send(ctx context.Context, l *leg, build func() (*sip.Request, error),
	settle func(res *sip.Response, err error) error,
) (*sip.Response, error) {
	h := &dialogClient{c: c, l: l, settle: settle, done: make(chan outcome, 1)}

	err := c.submit(func() (*sip.Request, error) {
		req, err := build()
		if err == nil {
			h.method = req.Method
		}

		return req, err
	}, h)
	if err != nil {
		if h.method != "" {
			h.finish(nil, err)
		}

		return nil, err
	}

	select {
	case o := <-h.done:
		return o.res, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type outcome struct {
	res *sip.Response
	err error
}

type dialogClient struct {
	c      *Call
	l      *leg
	method string
	settle func(res *sip.Response, err error) error
	once   sync.Once
	done   chan outcome
}

func (h *dialogClient) HandleResponse(res *sip.Response) {
	if res.IsProvisional() {
		return
	}

	h.finish(res, nil)
}

func (h *dialogClient) HandleError(err error) {
	h.finish(nil, fmt.Errorf("testue: %s: %w", h.method, err))
}

func (h *dialogClient) finish(res *sip.Response, err error) {
	h.once.Do(func() {
		c := h.c

		c.mu.Lock()

		switch res {
		case nil:
			c.event(Event{Err: err})
		default:
			c.event(Event{Response: res})

			if rerr := h.l.d.ReceiveResponse(res); rerr != nil {
				err = fmt.Errorf("testue: %s response: %w", h.method, rerr)
			} else if !res.IsSuccess() {
				err = &ResponseError{Response: res}
			}
		}

		if h.settle != nil {
			err = h.settle(res, err)
		}

		c.mu.Unlock()

		h.done <- outcome{res: res, err: err}
	})
}

// RFC 3261 §12.2.1.2
func dialogLost(res *sip.Response, err error) bool {
	if res != nil {
		return res.StatusCode == 481 || res.StatusCode == 408
	}

	return errors.Is(err, transaction.ErrTimeout)
}

// RFC 3312, IR.92 §2.4.1
func (c *Call) prack(ctx context.Context, l *leg, res *sip.Response) error {
	_, err := c.send(ctx, l, func() (*sip.Request, error) {
		prack, err := l.d.NewPrack(res)
		if err != nil {
			return nil, fmt.Errorf("testue: %w", err)
		}

		return prack, c.u.prepare(prack)
	}, func(r *sip.Response, err error) error {
		if err != nil && dialogLost(r, err) {
			c.dialogLostLocked(l)
		}

		return err
	})
	if err != nil {
		return err
	}

	c.mu.Lock()
	update := c.state == CallEarly && !l.released && l.m.precondition && !l.m.met() && l.m.remote != nil && !c.localOfferLocked(l)
	c.mu.Unlock()

	if !update {
		return nil
	}

	return c.update(ctx, l, true, nil)
}

// dialogLostLocked handles a dialog the remote end no longer knows (RFC 3261 §12.2.1.2). Losing an early dialog
// of an outgoing call releases only that dialog: the INVITE is still pending, and the call waits for other early
// dialogs or its final response (RFC 6228 §4).
func (c *Call) dialogLostLocked(l *leg) {
	if c.incoming || l == c.leg && c.state == CallConfirmed {
		c.terminateLocked(TimedOut)
		return
	}

	c.releaseLocked(l)
}

// update sends an UPDATE (RFC 3311), with an offer of the session as change, when not nil, leaves it. An offer is made
// once more after a 491 (RFC 3311 §5.1).
func (c *Call) update(ctx context.Context, l *leg, withOffer bool, change func(*media) error) error {
	if !withOffer {
		return c.updateOnce(ctx, l, false, nil)
	}

	return c.retryPending(ctx, func() error { return c.updateOnce(ctx, l, true, change) })
}

func (c *Call) updateOnce(ctx context.Context, l *leg, withOffer bool, change func(*media) error) error {
	var (
		previous mediaState
		offered  bool
	)

	if withOffer {
		ctx = context.WithoutCancel(ctx)
	}

	_, err := c.send(ctx, l, func() (*sip.Request, error) {
		req, err := c.newRequest(l, "UPDATE")
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if withOffer {
			if c.localOfferLocked(l) || c.remoteOfferLocked() {
				return nil, fmt.Errorf("%w: an offer is pending", ErrCallState)
			}

			previous = l.m.save()

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

			l.offering, offered = true, true

			req.SetBody(sdp.ContentType, offer.Bytes())

			if l.m.precondition {
				req.Header.Add("Require", "precondition")
			}
		}

		req.Header.Add("Supported", c.supportedLocked())

		// The session timer is the call's: only an UPDATE on its dialog refreshes it (RFC 4028).
		if interval := c.interval; l == c.leg && (interval > 0 || !withOffer) {
			if interval <= 0 {
				interval = DefaultSessionExpires
			}

			req.Header.Add("Session-Expires", seconds(interval)+";refresher="+c.refresherLocked())
		}

		return req, nil
	}, func(res *sip.Response, err error) error {
		defer c.notifyLocked()

		if offered {
			l.offering = false
		}

		if err != nil {
			if offered {
				l.m.restore(previous)
			}

			if withOffer && dialogLost(res, err) {
				c.dialogLostLocked(l)
			}

			return err
		}

		if withOffer {
			if err := answerOfOffer(l.m, res.Header.ContentType(), res.Body); err != nil {
				l.m.restore(previous)
				return err
			}
		}

		// An UPDATE on another early dialog of a forked INVITE, which may complete after the call was answered
		// on its own dialog, leaves the call's session timer alone.
		if l == c.leg && !l.released {
			interval, refresher, _ := parseSessionExpires(res.Header)
			c.sessionTimerLocked(interval, refresher != "uas")
		}

		return nil
	})

	return err
}

// RFC 4028 §7.4
func (c *Call) refresherLocked() string {
	if c.refresher || c.interval == 0 {
		return "uac"
	}

	return "uas"
}

func answerOfOffer(m *media, contentType string, body []byte) error {
	if mediaType(contentType) != sdp.ContentType || len(body) == 0 {
		return errors.New("testue: no answer to the offer")
	}

	s, err := sdp.Parse(body)
	if err != nil {
		return fmt.Errorf("testue: answer: %w", err)
	}

	return m.answered(s)
}

func (c *Call) Wait(ctx context.Context) (*sip.Response, error) {
	if c.incoming {
		return nil, fmt.Errorf("%w: Wait on an incoming call", ErrCallState)
	}

	if err := c.waitFor(ctx, func() bool { return c.final != nil || c.finalErr != nil || c.state == CallTerminated }); err != nil {
		return nil, err
	}

	c.mu.Lock()
	sent := c.ackSent
	c.mu.Unlock()

	if sent != nil {
		select {
		case <-sent:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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

// RFC 3329, RFC 3261 §9.1
// Cancel cancels the call the user ends before it is answered, with RELEASE_CAUSE 1 (TS 24.229 §5.1.3.1), as phones
// do.
func (c *Call) Cancel(ctx context.Context) error {
	return c.cancel(ctx, Cancelled, nil, userEnds())
}

// cancel cancels the INVITE (RFC 3261 §9.1), the call ending with end and the CANCEL carrying body, when not nil,
// and extra.
func (c *Call) cancel(ctx context.Context, end EndReason, body *sdp.Session, extra ...sip.Field) error {
	c.mu.Lock()

	if c.incoming || c.itx == nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: Cancel on an incoming call", ErrCallState)
	}

	if c.state == CallConfirmed || c.state == CallTerminated {
		c.mu.Unlock()
		return fmt.Errorf("%w: Cancel of a %s call", ErrCallState, c.state)
	}

	c.cancelled, c.cancelEnd, c.cancelReason = true, end, extra
	itx := c.itx
	c.mu.Unlock()

	if err := itx.CancelWith(func(cancel *sip.Request) {
		for _, f := range extra {
			cancel.Header.Add(f.Name, f.Value)
		}

		if body != nil {
			cancel.SetBody(sdp.ContentType, body.Bytes())
		}
	}); err != nil {
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

// RFC 3261 §15.1.1
func (c *Call) bye(ctx context.Context, reason EndReason, extra ...sip.Field) error {
	c.mu.Lock()
	l := c.leg
	c.mu.Unlock()

	res, err := c.send(ctx, l, func() (*sip.Request, error) {
		req, err := c.newRequest(l, "BYE")
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if c.state != CallConfirmed || c.leg != l {
			return nil, fmt.Errorf("%w: BYE in a %s call", ErrCallState, c.state)
		}

		for _, f := range extra {
			req.Header.Add(f.Name, f.Value)
		}

		c.terminateLocked(reason)

		return req, nil
	}, nil)

	if res != nil {
		return nil
	}

	return err
}

// byeLeg ends a dialog confirmed by a 2xx from a callee the call did not keep
// (RFC 3261 §13.2.2.4).
func (c *Call) byeLeg(ctx context.Context, l *leg) error {
	_, err := c.send(ctx, l, func() (*sip.Request, error) {
		return c.newRequest(l, "BYE")
	}, nil)

	return err
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

// RFC 4028 §7.4, §10, IR.92 §2.2.8
func (c *Call) Refresh(ctx context.Context) error {
	c.mu.Lock()
	l := c.leg
	c.mu.Unlock()

	err := c.update(ctx, l, false, nil)

	var rerr *ResponseError

	switch {
	case err == nil:
	case errors.As(err, &rerr) && rerr.Response.StatusCode == 481:
		c.terminate(Expired)
	case errors.Is(err, transaction.ErrTimeout), errors.As(err, &rerr) && rerr.Response.StatusCode == 408:
		_ = c.bye(ctx, Expired)
		c.terminate(Expired)
	}

	return err
}

// reinvite offers the session as change leaves it (RFC 3261 §14.1), and restores it when the offer fails. A stream it
// added has its resources reported in an UPDATE once it completes (RFC 3312 §5).
func (c *Call) reinvite(ctx context.Context, change func(*media) error) error {
	c.mu.Lock()
	l := c.leg
	c.mu.Unlock()

	if err := c.retryPending(ctx, func() error { return c.reinviteOnce(ctx, change) }); err != nil {
		return err
	}

	c.mu.Lock()
	update := c.state == CallConfirmed && c.leg == l && l.m.precondition && !l.m.met() && !c.localOfferLocked(l)
	c.mu.Unlock()

	if update {
		return c.update(ctx, l, true, nil)
	}

	return nil
}

// retryPending makes a request once more when it gets a 491 (Request Pending), after a random time: 2.1 to 4 s for
// the owner of the Call-ID, the caller, and up to 2 s otherwise, in units of 10 ms (RFC 3261 §14.1, RFC 3311 §5.1).
func (c *Call) retryPending(ctx context.Context, send func() error) error {
	err := send()

	var rerr *ResponseError
	if !errors.As(err, &rerr) || rerr.Response.StatusCode != 491 {
		return err
	}

	low, high := 0, 200
	if !c.incoming {
		low, high = 210, 400
	}

	timer := time.NewTimer(time.Duration(low+mrand.IntN(high-low+1)) * 10 * time.Millisecond)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-c.done:
		return ErrCallEnded
	case <-ctx.Done():
		return ctx.Err()
	}

	return send()
}

func (c *Call) reinviteOnce(ctx context.Context, change func(*media) error) error {
	// RFC 3264 §4: the offer waits for the one in progress, such as an UPDATE of the call's setup still unanswered.
	if err := c.waitFor(ctx, func() bool {
		return c.state == CallTerminated || c.accepted == nil && !c.localOfferLocked(c.leg)
	}); err != nil {
		return err
	}

	var previous mediaState

	c.mu.Lock()
	l := c.leg
	c.mu.Unlock()

	h := &reinviteClient{c: c, l: l, done: make(chan error, 1)}

	h.settle = func(res *sip.Response, err error) error {
		l.offering = false

		c.notifyLocked()

		switch {
		case err != nil || !res.IsSuccess():
			l.m.restore(previous)

			if dialogLost(res, err) {
				c.terminateLocked(TimedOut)
			}

			if err != nil {
				return err
			}

			return &ResponseError{Response: res}
		}

		// RFC 3261 §13.2.1, RFC 3262 §5: the answer is in the first reliable response, and the 2xx after it has
		// none.
		if !h.answered {
			if err := answerOfOffer(l.m, res.Header.ContentType(), res.Body); err != nil {
				l.m.restore(previous)
				return err
			}
		}

		interval, refresher, _ := parseSessionExpires(res.Header)
		c.sessionTimerLocked(interval, refresher != "uas")

		return nil
	}

	err := c.submit(func() (*sip.Request, error) {
		req, err := c.newRequest(l, "INVITE")
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if c.state != CallConfirmed || c.leg != l || c.localOfferLocked(l) || c.accepted != nil {
			return nil, fmt.Errorf("%w: re-INVITE in a %s call, or during another offer", ErrCallState, c.state)
		}

		previous = l.m.save()

		if err := change(l.m); err != nil {
			l.m.restore(previous)
			return nil, err
		}

		offer, err := l.m.offer()
		if err != nil {
			l.m.restore(previous)
			return nil, err
		}

		l.offering = true

		req.Header.Add("Supported", c.supportedLocked())

		if l.m.precondition {
			req.Header.Add("Require", "precondition")
		}

		if c.interval > 0 {
			req.Header.Add("Session-Expires", seconds(c.interval)+";refresher="+c.refresherLocked())
		}

		req.SetBody(sdp.ContentType, offer.Bytes())

		h.req = req

		return req, nil
	}, h)
	if err != nil {
		if h.req != nil {
			h.finish(nil, err)
		}

		return err
	}

	err = <-h.done

	h.mu.Lock()
	sent := h.sent
	h.mu.Unlock()

	if err == nil && sent != nil {
		<-sent
	}

	return err
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
		return state == CallTerminated || c.remoteOfferLocked() || !c.localOfferLocked(c.leg)
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

type reinviteClient struct {
	c      *Call
	l      *leg
	req    *sip.Request
	settle func(res *sip.Response, err error) error
	once   sync.Once
	done   chan error

	// answered is set, under the call's lock, once a reliable provisional response brought the answer.
	answered bool

	mu       sync.Mutex
	ack      *sip.Request
	sent     <-chan struct{}
	rseq     uint32
	haveRSeq bool
}

func (h *reinviteClient) HandleResponse(res *sip.Response) {
	c := h.c

	if res.IsProvisional() {
		c.event(Event{Response: res})
		h.provisional(res)

		return
	}

	if res.IsSuccess() {
		h.mu.Lock()

		if h.ack == nil {
			d := h.l.d

			if err := d.ReceiveResponse(res); err != nil {
				c.event(Event{Err: err})
			}

			ack, err := d.NewAck(h.req)
			if err == nil {
				err = c.u.prepare(ack)
			}

			if err != nil {
				h.mu.Unlock()
				h.HandleError(err)

				return
			}

			h.ack = ack
		}

		sent := c.sendAck(h.ack.Clone())
		if h.sent == nil {
			h.sent = sent
		}

		h.mu.Unlock()
	}

	if !h.finish(res, nil) {
		c.event(Event{Response: res})
	}
}

func (h *reinviteClient) finish(res *sip.Response, err error) bool {
	first := false

	h.once.Do(func() {
		first = true
		c := h.c

		c.mu.Lock()

		if res != nil {
			c.event(Event{Response: res})
		}

		err = h.settle(res, err)
		c.mu.Unlock()

		h.done <- err
	})

	return first
}

func (h *reinviteClient) provisional(res *sip.Response) {
	if !has(res.Header, "Require", "100rel") {
		return
	}

	rseq, err := res.Header.RSeq()
	if err != nil {
		return
	}

	h.mu.Lock()

	if h.haveRSeq && rseq != h.rseq+1 {
		h.mu.Unlock()
		return
	}

	h.rseq, h.haveRSeq = rseq, true
	h.mu.Unlock()

	c := h.c
	l := h.l

	// RFC 3262 §5: the first reliable response with SDP answers the offer. A remote end that waits for our
	// resources before its 2xx (RFC 3312 §5, TS 24.229 §6.1.4.2) gets them reported in an UPDATE (RFC 3311).
	update := false

	if mediaType(res.Header.ContentType()) == sdp.ContentType && len(res.Body) > 0 {
		c.mu.Lock()

		if !h.answered {
			if err := answerOfOffer(l.m, res.Header.ContentType(), res.Body); err != nil {
				c.event(Event{Err: err})
			} else {
				h.answered, l.offering = true, false
				update = l.m.precondition && !l.m.met()

				c.notifyLocked()
			}
		}

		c.mu.Unlock()
	}

	c.background(func(ctx context.Context) error {
		d := l.d

		_, err := c.send(ctx, l, func() (*sip.Request, error) {
			prack, err := d.NewPrack(res)
			if err != nil {
				return nil, fmt.Errorf("testue: %w", err)
			}

			return prack, c.u.prepare(prack)
		}, nil)
		if err != nil || !update {
			return err
		}

		return c.update(ctx, l, true, nil)
	})
}

func (h *reinviteClient) HandleError(err error) {
	h.c.event(Event{Err: err})
	h.finish(nil, fmt.Errorf("testue: re-INVITE: %w", err))
}

// RFC 4028
func parseSessionExpires(h sip.Header) (time.Duration, string, bool) {
	v := h.Get("Session-Expires")
	if v == "" {
		v = h.Get("x")
	}

	value, params, err := sip.ParseTokenParams(v)
	if err != nil {
		return 0, "", false
	}

	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, "", false
	}

	refresher, _ := params.Get("refresher")

	return time.Duration(n) * time.Second, strings.ToLower(refresher), true
}

// RFC 4028, TS 24.229 §5.1.4.1, IR.92 §2.2.8
func sessionTimerAnswer(req *sip.Request, initial bool) (time.Duration, string, bool) {
	supported := has(req.Header, "Supported", "timer") || has(req.Header, "Require", "timer")

	interval, refresher, ok := parseSessionExpires(req.Header)

	switch {
	case !ok && initial && supported:
		interval, refresher = DefaultSessionExpires, "uac"

		if v, err := strconv.ParseUint(strings.TrimSpace(strings.Split(req.Header.Get("Min-SE"), ";")[0]), 10, 32); err == nil {
			interval = max(interval, time.Duration(v)*time.Second)
		}
	case !ok:
		return 0, "", false
	case refresher == "" && supported:
		refresher = "uac"
	case refresher == "" || !supported:
		refresher = "uas"
	}

	return interval, refresher, supported
}

func (c *Call) addSessionTimerLocked(req *sip.Request, res *sip.Response, initial bool) {
	interval, refresher, supported := sessionTimerAnswer(req, initial)
	if interval == 0 {
		c.sessionTimerLocked(0, false)
		return
	}

	res.Header.Add("Session-Expires", seconds(interval)+";refresher="+refresher)

	if supported {
		res.Header.Add("Require", "timer")
	}

	c.sessionTimerLocked(interval, refresher == "uas")
}

// RFC 4028 §10
func (c *Call) sessionTimerLocked(interval time.Duration, refresher bool) {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}

	c.interval, c.refresher = interval, refresher

	if interval <= 0 || !c.auto || c.state != CallConfirmed {
		return
	}

	if refresher {
		c.timer = time.AfterFunc(interval/2, func() {
			c.background(c.Refresh)
		})

		return
	}

	c.timer = time.AfterFunc(interval-min(32*time.Second, interval/3), func() {
		c.background(func(ctx context.Context) error { return c.bye(ctx, Expired) })
	})
}

func has(h sip.Header, name, tag string) bool {
	return slices.ContainsFunc(h.Elements(name), func(e string) bool {
		return strings.EqualFold(strings.TrimSpace(e), tag)
	})
}

func (u *UE) incomingCall(tx *transaction.ServerTransaction, req *sip.Request) {
	reject := func(res *sip.Response) {
		_ = res.Header.SetToTag(tx.ToTag())
		_ = tx.Respond(res)
	}

	var unsupported []string

	for _, tag := range req.Header.Elements("Require") {
		if !slices.ContainsFunc(supportedTags, func(s string) bool { return strings.EqualFold(s, strings.TrimSpace(tag)) }) {
			unsupported = append(unsupported, strings.TrimSpace(tag))
		}
	}

	if len(unsupported) > 0 {
		res := u.response(req, 420)
		res.Header.Add("Unsupported", strings.Join(unsupported, ", "))
		reject(res)

		return
	}

	var offer *sdp.Session

	if mediaType(req.Header.ContentType()) == sdp.ContentType && len(req.Body) > 0 {
		var err error

		if offer, err = sdp.Parse(req.Body); err != nil {
			reject(u.response(req, 400))
			return
		}

		if !offerAudio(offer) {
			res := u.response(req, 488)
			if w, err := sip.NewWarning(304, sip.FormatHost(u.cfg.Local)); err == nil {
				res.Header.Add("Warning", w.String())
			}

			reject(res)

			return
		}
	}

	precondition := (has(req.Header, "Supported", "precondition") || has(req.Header, "Require", "precondition")) && offerQoS(offer)

	// IR.94 §2.2.2: the offer the UE makes to an INVITE without one has every media it is able and willing to use.
	c, err := u.newCall(true, req, callKey{callID: req.Header.CallID(), tag: tx.ToTag()}, precondition, u.cfg.Video)
	if err != nil {
		reject(u.response(req, 500))
		return
	}

	c.stx = tx
	c.offer = offer
	c.rel100 = has(req.Header, "Supported", "100rel") || has(req.Header, "Require", "100rel")
	c.rseqOut = randomRSeq()

	c.mu.Lock()
	c.holdSALocked()
	c.mu.Unlock()

	u.mu.Lock()
	u.calls.active[c.key] = c
	u.mu.Unlock()

	select {
	case u.calls.incoming <- c:
	default:
		c.terminate(Rejected)
		reject(u.response(req, 486))
	}
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

func randomRSeq() uint32 {
	var b [4]byte

	_, _ = rand.Read(b[:])

	return binary.BigEndian.Uint32(b[:])%(1<<30) + 1
}

func (c *Call) responseLocked(code int) (*sip.Response, error) {
	res := c.u.response(c.invite, code)
	if err := res.Header.SetToTag(c.stx.ToTag()); err != nil {
		return nil, err
	}

	res.Header.Add("Contact", c.u.callContact())
	res.Header.Add("Allow", allow)
	res.Header.Add("Supported", c.supportedLocked())

	if code > 100 && code < 200 && c.u.cfg.EarlyMedia != "" {
		res.Header.Add("P-Early-Media", c.u.cfg.EarlyMedia)
	}

	if c.leg.d == nil {
		d, err := dialog.NewUAS(c.invite, res)
		if err != nil {
			return nil, fmt.Errorf("testue: %w", err)
		}

		c.leg.d = d
		c.legs = append(c.legs, c.leg)
	} else {
		c.leg.d.PrepareResponse(c.invite, res)
	}

	if res.IsSuccess() {
		c.state = CallConfirmed
	} else {
		c.state = CallEarly
	}

	return res, nil
}

func (c *Call) sdpLocked(res *sip.Response) (bool, error) {
	if c.answered {
		return false, nil
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
		return false, err
	}

	c.answered = true

	res.SetBody(sdp.ContentType, s.Bytes())

	return c.offer == nil, nil
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
		update := c.state == CallEarly && !l.m.met() && l.m.remoteMet() && !c.localOfferLocked(l)
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

func (c *Call) provisional(ctx context.Context, code int, reliably bool) error {
	c.mu.Lock()

	if err := c.checkIncoming("provisional response"); err != nil {
		c.mu.Unlock()
		return err
	}

	if c.unacked != nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: a reliable provisional response waits for its PRACK", ErrCallState)
	}

	res, err := c.responseLocked(code)
	if err != nil {
		c.mu.Unlock()
		return err
	}

	var r *reliable

	if reliably {
		offer, err := c.sdpLocked(res)
		if err != nil {
			c.mu.Unlock()
			return err
		}

		require := "100rel"
		if c.leg.m.precondition {
			require = "precondition, 100rel"
		}

		res.Header.Add("Require", require)
		res.Header.Add("RSeq", strconv.FormatUint(uint64(c.rseqOut), 10))

		r = &reliable{res: res, rseq: c.rseqOut, interval: c.u.layer.T1(), started: time.Now(), offer: offer}
		c.rseqOut++
		c.unacked = r
		r.timer = time.AfterFunc(r.interval, func() { c.retransmitReliable(r) })
	}

	stx := c.stx
	c.notifyLocked()
	c.mu.Unlock()

	if err := stx.Respond(res); err != nil {
		return fmt.Errorf("testue: %d: %w", code, err)
	}

	if r == nil {
		return nil
	}

	if err := c.waitFor(ctx, func() bool { return c.unacked != r }); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.end {
	case TimedOut:
		return ErrNoPrack
	case NotEnded:
		return nil
	}

	return ErrCallEnded
}

// RFC 3262
func (c *Call) retransmitReliable(r *reliable) {
	c.mu.Lock()

	if c.unacked != r {
		c.mu.Unlock()
		return
	}

	deadline := 64 * c.u.layer.T1()
	elapsed := time.Since(r.started)
	stx := c.stx

	if elapsed >= deadline {
		res := c.u.response(c.invite, 504)
		_ = res.Header.SetToTag(stx.ToTag())

		c.terminateLocked(TimedOut)
		c.mu.Unlock()

		_ = stx.Respond(res)

		return
	}

	r.interval *= 2
	r.timer = time.AfterFunc(min(r.interval, deadline-elapsed), func() { c.retransmitReliable(r) })
	c.mu.Unlock()

	_ = stx.Respond(r.res)
}

func (c *Call) Answer(ctx context.Context) error {
	c.mu.Lock()

	if err := c.checkIncoming("Answer"); err != nil {
		c.mu.Unlock()
		return err
	}

	if c.unacked != nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: a reliable provisional response waits for its PRACK", ErrCallState)
	}

	res, err := c.responseLocked(200)
	if err != nil {
		c.mu.Unlock()
		return err
	}

	inAck, err := c.sdpLocked(res)
	if err != nil {
		c.mu.Unlock()
		return err
	}

	c.addSessionTimerLocked(c.invite, res, true)

	cseq, _ := c.invite.Header.CSeq()
	a := c.acceptLocked(c.stx, res, cseq.Seq)
	a.answerInAck, a.initial = inAck, true

	stx := c.stx
	c.mu.Unlock()

	if err := stx.Respond(res); err != nil {
		return fmt.Errorf("testue: 200: %w", err)
	}

	if err := c.waitFor(ctx, func() bool { return a.acked || c.state == CallTerminated }); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case a.acked:
		return nil
	case c.end == TimedOut:
		return ErrNoAck
	}

	return ErrCallEnded
}

// RFC 3261 §13.3.1.4
func (c *Call) acceptLocked(stx *transaction.ServerTransaction, res *sip.Response, cseq uint32) *accepted {
	a := &accepted{stx: stx, res: res, cseq: cseq, interval: c.u.layer.T1(), started: time.Now()}
	c.accepted = a
	a.timer = time.AfterFunc(a.interval, func() { c.retransmitAccepted(a) })

	return a
}

func (c *Call) retransmitAccepted(a *accepted) {
	c.mu.Lock()

	if c.accepted != a {
		c.mu.Unlock()
		return
	}

	deadline := 64 * c.u.layer.T1()
	elapsed := time.Since(a.started)

	if elapsed >= deadline {
		c.accepted = nil
		c.mu.Unlock()

		c.background(func(ctx context.Context) error { return c.bye(ctx, TimedOut) })

		return
	}

	a.interval = min(2*a.interval, transaction.DefaultT2)
	a.timer = time.AfterFunc(min(a.interval, deadline-elapsed), func() { c.retransmitAccepted(a) })
	c.mu.Unlock()

	_ = a.stx.Respond(a.res)
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

	res := c.u.response(c.invite, code)
	if err := res.Header.SetToTag(c.stx.ToTag()); err != nil {
		c.mu.Unlock()
		return err
	}

	for _, f := range extra {
		res.Header.Add(f.Name, f.Value)
	}

	if body != nil {
		res.SetBody(sdp.ContentType, body.Bytes())
	}

	if c.leg.d != nil {
		c.leg.d.PrepareResponse(c.invite, res)
	}

	c.terminateLocked(end)

	stx := c.stx
	c.mu.Unlock()

	return stx.Respond(res)
}

// RFC 3261 §9.2
func (c *Call) cancelReceived(cancel *sip.Request) {
	c.event(Event{Request: cancel})

	c.mu.Lock()

	if c.state == CallConfirmed || c.state == CallTerminated {
		c.mu.Unlock()
		return
	}

	res := c.u.response(c.invite, 487)
	_ = res.Header.SetToTag(c.stx.ToTag())

	if c.leg.d != nil {
		c.leg.d.PrepareResponse(c.invite, res)
	}

	c.reasons, _ = cancel.Header.Reasons()
	c.terminateLocked(Cancelled)

	stx := c.stx
	c.mu.Unlock()

	_ = stx.Respond(res)
}

func (c *Call) ackReceived(ack *sip.Request) {
	c.event(Event{Request: ack})

	cseq, err := ack.Header.CSeq()
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	a := c.accepted
	if a == nil || a.cseq != cseq.Seq {
		return
	}

	c.acknowledgedLocked(a)

	if a.answerInAck {
		if err := answerOfOffer(c.leg.m, ack.Header.ContentType(), ack.Body); err != nil {
			c.event(Event{Err: err})
		}
	}

	c.notifyLocked()
}

// acknowledgedLocked ends the retransmission of an accepted INVITE's 2xx (RFC 3261 §13.3.1.4).
func (c *Call) acknowledgedLocked(a *accepted) {
	a.acked = true
	a.timer.Stop()

	c.accepted = nil

	if a.initial {
		c.releaseSALocked()
	}
}

func (c *Call) requestReceived(tx *transaction.ServerTransaction, req *sip.Request) {
	c.event(Event{Request: req})

	c.mu.Lock()

	var (
		res   *sip.Response
		extra func()
	)

	l := c.leg
	if !c.incoming {
		l = c.legLocked(dialog.RequestID(req).RemoteTag)
	}

	switch {
	case l == nil || l.d == nil:
		res = c.u.response(req, 481)
	// A released early dialog takes only a BYE; another live early dialog than the call's, only an UPDATE too.
	case req.Method != "BYE" && (l.released || l != c.leg && req.Method != "UPDATE"):
		res = c.u.response(req, 481)
	case req.Method == "INVITE" && c.localOfferLocked(l):
		res = c.u.response(req, 491)
	case req.Method == "INVITE" && c.accepted != nil:
		// RFC 3261 §14.2 asks for a 500 only while the final response to the previous INVITE is not sent. It was,
		// and a new INVITE in the dialog shows the remote end has it (§13.3.1.4): its ACK is still on the way.
		c.acknowledgedLocked(c.accepted)

		fallthrough
	default:
		if err := l.d.ReceiveRequest(req); err != nil {
			code := 400
			if serr, ok := errors.AsType[*sip.StatusError](err); ok {
				code = serr.StatusCode
			}

			res = c.u.response(req, code)

			break
		}

		switch req.Method {
		case "PRACK":
			res = c.prackReceivedLocked(req)
		case "UPDATE", "INVITE":
			res = c.offerReceivedLocked(tx, l, req)
		case "BYE":
			res = c.u.response(req, 200)

			if !c.incoming && (l != c.leg || c.state == CallEarly) {
				c.releaseLocked(l)
				break
			}

			c.reasons, _ = req.Header.Reasons()

			if c.incoming && c.state == CallEarly {
				end := c.u.response(c.invite, 487)
				_ = end.Header.SetToTag(c.stx.ToTag())

				stx := c.stx
				extra = func() { _ = stx.Respond(end) }
			}

			c.terminateLocked(RemoteBye)
		}
	}

	c.mu.Unlock()

	if extra != nil {
		extra()
	}

	if res == nil {
		return
	}

	_ = tx.Respond(res)

	c.mu.Lock()
	c.notifyLocked()
	c.mu.Unlock()
}

func (c *Call) prackReceivedLocked(req *sip.Request) *sip.Response {
	rack, err := req.Header.RAck()
	cseq, _ := c.invite.Header.CSeq()

	r := c.unacked
	if err != nil || r == nil || rack.RSeq != r.rseq || rack.CSeq != cseq.Seq || rack.Method != "INVITE" {
		return c.u.response(req, 481)
	}

	r.timer.Stop()

	c.unacked = nil

	if r.offer {
		if err := answerOfOffer(c.leg.m, req.Header.ContentType(), req.Body); err != nil {
			c.event(Event{Err: err})
		}
	}

	return c.u.response(req, 200)
}

// RFC 4028
func (c *Call) offerReceivedLocked(tx *transaction.ServerTransaction, l *leg, req *sip.Request) *sip.Response {
	hasSDP := mediaType(req.Header.ContentType()) == sdp.ContentType && len(req.Body) > 0

	if req.Method == "UPDATE" && hasSDP {
		switch {
		case c.localOfferLocked(l):
			return c.u.response(req, 491)
		case c.remoteOfferLocked():
			return retryAfter(c.u.response(req, 500))
		}
	}

	res := c.u.response(req, 200)
	dialog.CopyRecordRoute(res, req)
	res.Header.Add("Contact", c.u.callContact())
	res.Header.Add("Allow", allow)

	inAck := false

	switch {
	case hasSDP:
		offer, err := sdp.Parse(req.Body)
		if err != nil {
			return c.u.response(req, 400)
		}

		answer, err := l.m.answer(offer)
		if errors.Is(err, ErrNoCodec) {
			return c.u.response(req, 488)
		}

		if err != nil {
			return c.u.response(req, 500)
		}

		res.SetBody(sdp.ContentType, answer.Bytes())

		if l.m.precondition && (has(req.Header, "Supported", "precondition") || has(req.Header, "Require", "precondition")) {
			res.Header.Add("Require", "precondition")
		}
	case req.Method == "INVITE":
		offer, err := l.m.offer()
		if err != nil {
			return c.u.response(req, 500)
		}

		res.SetBody(sdp.ContentType, offer.Bytes())

		inAck = true
	}

	if l != c.leg {
		return res
	}

	c.addSessionTimerLocked(req, res, false)

	if req.Method == "INVITE" {
		cseq, _ := req.Header.CSeq()

		a := c.acceptLocked(tx, res, cseq.Seq)
		a.answerInAck = inAck
	}

	return res
}
