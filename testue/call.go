package testue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/ue"
)

const (
	DefaultSessionExpires = 1800 * time.Second

	firstMediaPort = 40000

	portsPerCall = 4
	maxCalls     = 5000
)

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

	Video bool

	SessionExpires time.Duration

	NoSessionTimer bool

	Headers []sip.Field
}

type Call struct {
	u        *UE
	mm       *MMTel
	incoming bool
	callID   string
	events   chan ue.Event
	done     chan struct{}

	mu      sync.Mutex
	changed chan struct{}
	state   CallState
	end     EndReason
	endErr  error
	reasons []sip.Reason
	invite  *sip.Request

	leg  *leg
	legs []*leg

	initial *media

	releaseSAs func()

	out       *ua.Outgoing
	final     *sip.Response
	finalErr  error
	cancelled bool
	cancelEnd EndReason

	in       *ua.Request
	offer    *sdp.Session
	answered bool
	rel100   bool
	pracks   int
	acked    bool

	byeEnd, rejectEnd EndReason

	refuse bool
}

type leg struct {
	s *ua.Session
	m *media

	offering  bool
	reporting bool

	released bool
}

func (mm *MMTel) newCall(incoming bool, invite *sip.Request, callID string, precondition, video bool) (*Call, error) {
	u := mm.u

	mm.mu.Lock()
	port := firstMediaPort + portsPerCall*mm.next
	mm.next = (mm.next + 1) % maxCalls
	mm.mu.Unlock()

	m := newMedia(u.core().Local(), uint16(port), precondition, mm.cfg.Video)

	if video {
		if err := m.addVideo(); err != nil {
			return nil, err
		}
	}

	return &Call{
		u:        u,
		mm:       mm,
		incoming: incoming,
		callID:   callID,
		events:   make(chan ue.Event, 256),
		done:     make(chan struct{}),
		changed:  make(chan struct{}),
		invite:   invite,
		leg:      &leg{m: m},
		byeEnd:   LocalBye,
	}, nil
}

func (c *Call) Incoming() bool { return c.incoming }

func (c *Call) Invite() *sip.Request {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.invite.Clone()
}

func (c *Call) Events() <-chan ue.Event { return c.events }

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

type Stream struct {
	Index         int
	Kind          string
	Active        bool
	Direction     sdp.Direction
	Local, Remote sdp.Endpoint
}

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
	s := c.leg.s
	c.mu.Unlock()

	if s == nil {
		return 0, false
	}

	return s.SessionTimer()
}

func (c *Call) event(e ue.Event) {
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

// TS 33.203 clause 7.4
func (c *Call) holdSALocked() {
	if c.releaseSAs == nil {
		c.releaseSAs = c.u.core().HoldSAs()
	}
}

func (c *Call) releaseSALocked() {
	if c.releaseSAs != nil {
		c.releaseSAs()
		c.releaseSAs = nil
	}
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

// RFC 3311 section 5.1
// RFC 3311 section 5.2
func (c *Call) remoteOfferLocked() bool {
	return c.incoming && c.offer != nil && !c.answered
}

func (c *Call) handler() ua.Handler {
	return ua.Handler{
		Progress:    c.progress,
		Established: c.established,
		Answer:      c.answerReceived,
		Offer:       c.offerReceived,
		Pracked:     c.pracked,
		Request:     c.request,
		Cancelled:   func(context.Context, *ua.Session, *ua.Request, *sip.Request) {},
		Received:    c.received,
		Ended:       c.ended,
		Failed:      c.failed,
	}
}

func (c *Call) received(ctx context.Context, _ *ua.Session, m sip.Message) {
	switch m := m.(type) {
	case *sip.Request:
		c.event(ue.Event{Request: m.Clone()})
	case *sip.Response:
		c.event(ue.Event{Response: m.Clone()})
	}
}

// TS 24.229 clause 5.1.3.1
// GSMA IR.92 section 2.4
func (mm *MMTel) Invite(target string, opts CallOptions) (*Call, error) {
	u := mm.u

	if opts.Preconditions && mm.cfg.NoPreconditions {
		return nil, fmt.Errorf("%w: preconditions on a UE without them", ErrCallState)
	}

	uri, err := sip.ParseURI(target)
	if err != nil {
		return nil, fmt.Errorf("testue: target: %w", err)
	}

	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("Accept-Contact", acceptContact(opts.Video))

	if err := u.core().Originate(req, MMTelService); err != nil {
		return nil, err
	}

	callID := u.core().NewCallID()

	c, err := mm.newCall(false, req, callID, opts.Preconditions, opts.Video)
	if err != nil {
		return nil, err
	}

	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Call-ID", callID)
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

	o, err := u.core().Agent().Invite(context.Background(), req, c.handler())
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

// RFC 3312 section 5
// TS 24.229 clause 6.1.4.2
func (c *Call) progress(ctx context.Context, s *ua.Session, res *sip.Response) {
	c.mu.Lock()

	l := c.legLocked(s)
	if l == nil || l.released || c.state != CallEarly && c.state != CallConfirmed {
		c.mu.Unlock()
		return
	}

	if c.state == CallEarly && !reliable(res) && l.m.remote == nil {
		if err := answerIn(l.m, res.Header.ContentType(), res.Body); err != nil {
			c.event(ue.Event{Err: err})
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

// RFC 3312 section 5
func (c *Call) reportResources(ctx context.Context, l *leg) error {
	defer func() {
		c.mu.Lock()

		l.reporting = false

		c.notifyLocked()
		c.mu.Unlock()
	}()

	return c.update(ctx, l, true, nil)
}

func (c *Call) established(ctx context.Context, s *ua.Session, m sip.Message) {
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

func (c *Call) answerReceived(ctx context.Context, s *ua.Session, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	l := c.legLocked(s)
	if l == nil {
		return
	}

	if err := answerIn(l.m, sdp.ContentType, body); err != nil {
		c.event(ue.Event{Err: err})
	}

	l.offering = false

	c.notifyLocked()
}

// RFC 3262 section 5
// RFC 3261 section 13.2.1
func (c *Call) offerReceived(ctx context.Context, s *ua.Session, o *ua.Offer) {
	c.mu.Lock()

	l := c.legLocked(s)
	if l == nil {
		c.mu.Unlock()
		o.Reject(ctx)

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
		c.event(ue.Event{Err: err})
		o.Reject(ctx)

		return
	}

	_ = o.Answer(ctx, ua.Change{Bodies: []sip.BodyPart{ua.SDP(answer.Bytes())}})
}

// RFC 4028 section 7.4
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

	if o != nil {
		o.SetPolicy(p)
	}

	for _, s := range sessions {
		s.SetPolicy(p)
	}
}

func (c *Call) pracked(context.Context, *ua.Session, *sip.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pracks++
	c.notifyLocked()
}

// RFC 3264 section 8
func (c *Call) request(ctx context.Context, s *ua.Session, r *ua.Request) {
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

	if err != nil {
		c.event(ue.Event{Err: err})

		_ = r.Reject(500)
	}
}

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

// RFC 6228 section 4
// RFC 3261 section 12.2.1.2
func (c *Call) ended(ctx context.Context, s *ua.Session, e ua.End) {
	c.mu.Lock()
	defer c.mu.Unlock()

	l := c.legLocked(s)
	if l == nil {
		return
	}

	if !c.incoming && (l != c.leg || c.state == CallEarly && e.Cause != ua.EndCancelled && e.Cause != ua.EndRejected &&
		e.Cause != ua.EndTimeout && e.Cause != ua.EndTransport && e.Cause != ua.EndDropped && e.Cause != ua.EndClosed) {
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

func (c *Call) failed(ctx context.Context, _ *ua.Outgoing, res *sip.Response, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.final, c.finalErr = res, err

	defer c.notifyLocked()

	reason := Rejected

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
		c.event(ue.Event{Err: err})
	}
}

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

func asError(res *sip.Response, err error) error {
	switch {
	case err != nil:
		return err
	case res != nil && !res.IsSuccess():
		return &ue.ResponseError{Response: res}
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

// RFC 3311 section 5.1
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

	err := l.s.Update(ctx, ch, f)
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
		return c.final, &ue.ResponseError{Response: c.final}
	case c.finalErr != nil:
		return nil, c.finalErr
	}

	return nil, ErrCallEnded
}

// TS 24.229 clause 5.1.3.1
// RFC 3261 section 9.1
func (c *Call) Cancel(ctx context.Context) error {
	return c.cancel(ctx, Cancelled, nil, userEnds())
}

// RFC 3261 section 9.1
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

	if err := o.Cancel(ctx, ch); err != nil {
		return fmt.Errorf("testue: CANCEL: %w", err)
	}

	return c.waitFor(ctx, func() bool { return c.state == CallTerminated })
}

// TS 24.229 clause 5.1.5
func (c *Call) Bye(ctx context.Context) error {
	return c.bye(ctx, LocalBye, userEnds())
}

// TS 24.229 clause 7.2A.18.11
func userEnds() sip.Field {
	r, _ := sip.NewReason(sip.ReasonReleaseCause, sip.ReleaseUserEndsCall, sip.ReasonText(sip.ReasonReleaseCause, sip.ReleaseUserEndsCall))

	return sip.Field{Name: "Reason", Value: r.String()}
}

// RFC 3261 section 15.1.1
// RFC 3326
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

	if err := s.Bye(ctx, ua.Change{Header: sip.Header(extra)}, func(_ *sip.Response, err error) { done <- err }); err != nil {
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

// RFC 3264 section 8.4
// GSMA IR.94 section 2.3.2
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

// RFC 4028 section 7.4
// GSMA IR.92 section 2.2.8
func (c *Call) Refresh(ctx context.Context) error {
	c.mu.Lock()
	l := c.leg
	c.mu.Unlock()

	return c.update(ctx, l, false, nil)
}

// RFC 3261 section 14.1
// RFC 3312 section 5
func (c *Call) reinvite(ctx context.Context, change func(*media) error) error {
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

	err = l.s.Reinvite(ctx, ch, f)
	if err == nil {
		var res *sip.Response

		res, err = waitDone(context.WithoutCancel(ctx), done)
		err = asError(res, err)
	}

	c.mu.Lock()

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

	return c.waitFor(ctx, func() bool { return c.state == CallTerminated || !l.reporting })
}

// GSMA IR.94 section 2.2.2
func (c *Call) AddVideo(ctx context.Context) error {
	return c.reinvite(ctx, (*media).addVideo)
}

// GSMA IR.94 section 2.2.2
func (c *Call) RemoveVideo(ctx context.Context) error {
	return c.reinvite(ctx, func(m *media) error { return m.remove(sdp.Video) })
}

// TS 24.229 clause 6.1.1
// GSMA IR.94 section 2.4.1
// GSMA NG.114 section 4.6.2
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

// RFC 3326
// TS 24.229 clause 5.1.3.1
// TS 24.229 clause 5.1.5
// RFC 3312 section 8
func (c *Call) release(ctx context.Context, end EndReason, protocol string, cause int) error {
	reason, err := sip.NewReason(protocol, cause, sip.ReasonText(protocol, cause))
	if err != nil {
		return err
	}

	field := sip.Field{Name: "Reason", Value: reason.String()}

	c.mu.Lock()
	state, incoming, precondition := c.state, c.incoming, c.leg.m.precondition

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

// TS 24.229 clause 5.1.4.1
func (mm *MMTel) incomingCall(r *ua.Request) ua.Handler {
	u := mm.u

	req := r.Message()
	u.event(ue.Event{Request: req})

	if !mm.cfg.AcceptCalls {
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
			if w, err := sip.NewWarning(304, sip.FormatHost(u.core().Local())); err == nil {
				warning = append(warning, sip.Field{Name: "Warning", Value: w.String()})
			}

			_ = r.Reject(488, warning...)

			return ua.Handler{}
		}
	}

	precondition := !mm.cfg.NoPreconditions &&
		(has(req.Header, "Supported", "precondition") || has(req.Header, "Require", "precondition")) && offerQoS(offer)

	c, err := mm.newCall(true, req, req.Header.CallID(), precondition, mm.cfg.Video)
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
	case mm.incoming <- c:
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

func (c *Call) responseHeaders(code int) sip.Header {
	h := sip.Header{{Name: "Contact", Value: c.mm.callContact()}}

	if code < 200 && c.mm.cfg.EarlyMedia != "" {
		h.Add("P-Early-Media", c.mm.cfg.EarlyMedia)
	}

	return h
}

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

// GSMA IR.92 section 2.4.1
// TS 24.229 clause 5.1.4.1
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

// RFC 3262 section 3
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

// RFC 3261 section 14.2
func (c *Call) RefuseOffers(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.refuse = on
}
