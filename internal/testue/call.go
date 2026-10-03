package testue

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
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
)

var (
	ErrCallEnded = errors.New("testue: call ended")

	ErrCallState = errors.New("testue: not allowed in this call state")

	ErrNoAck = errors.New("testue: no ACK to the 2xx")

	ErrNoPrack = errors.New("testue: no PRACK to the reliable provisional response")
)

type CallState int

const (
	// CallInit is a call whose INVITE has no dialog yet.
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
	// Cancelled is a call the caller cancelled, on either side.
	Cancelled
	// Rejected is a call answered with a final response of 300 or above.
	Rejected
	// TimedOut is a call whose INVITE, PRACK or ACK never came.
	TimedOut
	// Expired is a call whose session timer ran out (RFC 4028 §10).
	Expired
	Closed
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
	}

	return fmt.Sprintf("EndReason(%d)", int(r))
}

type CallOptions struct {
	// Preconditions offers qos preconditions, as IR.92 §2.4.1 phones do.
	Preconditions bool

	// SessionExpires is the INVITE's Session-Expires; DefaultSessionExpires
	// when zero.
	SessionExpires time.Duration

	NoSessionTimer bool

	// Headers are set on the INVITE, replacing the fields of the same name.
	Headers []sip.Field
}

type callKey struct {
	callID, tag string
}

// Call is one INVITE dialog of the UE, as caller or callee.
type Call struct {
	u        *UE
	incoming bool
	key      callKey
	events   chan Event
	done     chan struct{}

	mu      sync.Mutex
	changed chan struct{}
	state   CallState
	end     EndReason
	invite  *sip.Request
	d       *dialog.Dialog
	m       *media

	// offering is set while an offer of ours waits for its answer, outside
	// the initial INVITE.
	offering bool

	// The caller's side.
	itx       *transaction.ClientTransaction
	final     *sip.Response
	finalErr  error
	ack       *sip.Request
	ackSent   <-chan struct{}
	rseq      uint32
	haveRSeq  bool
	cancelled bool

	// The callee's side.
	stx      *transaction.ServerTransaction
	offer    *sdp.Session
	answered bool
	rel100   bool
	rseqOut  uint32
	unacked  *reliable
	accepted *accepted

	// The session timer (RFC 4028).
	interval  time.Duration
	refresher bool
	auto      bool
	timer     *time.Timer
}

// reliable is a reliable provisional response waiting for its PRACK (RFC
// 3262 §3).
type reliable struct {
	res      *sip.Response
	rseq     uint32
	interval time.Duration
	timer    *time.Timer
	started  time.Time
}

// accepted is a 2xx to an INVITE waiting for its ACK (RFC 3261 §13.3.1.4).
type accepted struct {
	stx      *transaction.ServerTransaction
	res      *sip.Response
	cseq     uint32
	interval time.Duration
	timer    *time.Timer
	started  time.Time
	acked    bool

	// answerInAck is set when the 2xx carries the offer.
	answerInAck bool
}

func (u *UE) newCall(incoming bool, invite *sip.Request, key callKey, precondition bool) *Call {
	u.mu.Lock()
	port := firstMediaPort + 2*u.calls.next
	u.calls.next = (u.calls.next + 1) % 10000
	u.mu.Unlock()

	return &Call{
		u:        u,
		incoming: incoming,
		key:      key,
		events:   make(chan Event, 256),
		done:     make(chan struct{}),
		changed:  make(chan struct{}),
		invite:   invite,
		m:        newMedia(u.cfg.Local, uint16(port), precondition),
		auto:     true,
	}
}

// Calls delivers the incoming calls of a UE configured with AcceptCalls.
func (u *UE) Calls() <-chan *Call { return u.calls.incoming }

func (c *Call) Incoming() bool { return c.incoming }

// Invite returns the initial INVITE, as sent or received.
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

func (c *Call) ID() dialog.ID {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.d == nil {
		return dialog.ID{}
	}

	return c.d.ID()
}

// LocalSDP and RemoteSDP return the last description each side sent.
func (c *Call) LocalSDP() *sdp.Session {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m.local == nil {
		return nil
	}

	return c.m.local.Clone()
}

func (c *Call) RemoteSDP() *sdp.Session {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m.remote == nil {
		return nil
	}

	return c.m.remote.Clone()
}

// PreconditionsMet reports whether the qos preconditions, if any, are met on
// both segments.
func (c *Call) PreconditionsMet() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.m.met()
}

// SessionTimer returns the negotiated session interval, zero without one, and
// whether this side refreshes it.
func (c *Call) SessionTimer() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.interval, c.refresher
}

// SetAutoRefresh turns off, or back on, the automatic session refresh and
// the BYE sent when the session expires.
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

func (c *Call) terminateLocked(reason EndReason) {
	if c.state == CallTerminated {
		return
	}

	c.state, c.end = CallTerminated, reason

	if c.d != nil {
		c.d.Terminate()
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

func (u *UE) callContact() string {
	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	return sip.Address{URI: uri, Params: sip.Params{
		{Name: "+sip.instance", Value: sip.Quote("<" + u.instance + ">")},
		{Name: "+g.3gpp.icsi-ref", Value: icsiMMTel},
		{Name: "audio"},
	}}.String()
}

func (u *UE) verify() []string {
	if u.cfg.Plain {
		return nil
	}

	if est := u.established(); est != nil {
		return est.server
	}

	return nil
}

// prepare addresses req to the P-CSCF over the established SAs, with the
// headers TS 24.229 §5.1.2A.1 has the UE put in every request.
func (u *UE) prepare(req *sip.Request) error {
	flow, verify, err := u.requestFlow()
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

// Invite calls target (TS 24.229 §5.1.3.1, IR.92 §2.4) and returns once the
// INVITE is sent. Wait returns its final response.
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

	supported := "100rel, timer"
	if opts.Preconditions {
		supported += ", precondition"
	}

	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<"+impu+">;tag="+tag)
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Call-ID", callID)
	req.Header.Add("CSeq", "1 INVITE")
	req.Header.Add("Contact", u.callContact())
	req.Header.Add("Accept-Contact", "*;+g.3gpp.icsi-ref="+icsiMMTel)
	req.Header.Add("P-Preferred-Identity", "<"+impu+">")
	req.Header.Add("P-Preferred-Service", mmtelService)
	req.Header.Add("P-Early-Media", sip.EarlyMediaSupported)
	req.Header.Add("Supported", supported)
	req.Header.Add("Allow", allow)
	req.Header.Add("Accept", sdp.ContentType+", application/3gpp-ims+xml")

	if !opts.NoSessionTimer {
		se := opts.SessionExpires
		if se <= 0 {
			se = DefaultSessionExpires
		}

		req.Header.Add("Session-Expires", seconds(se))
	}

	c := u.newCall(false, req, callKey{callID: callID, tag: tag}, opts.Preconditions)

	offer, err := c.m.offer()
	if err != nil {
		return nil, err
	}

	req.SetBody(sdp.ContentType, offer.Bytes())

	for _, f := range opts.Headers {
		req.Header.Set(f.Name, f.Value)
	}

	u.mu.Lock()
	u.calls.active[c.key] = c
	u.mu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	itx, err := u.layer.Request(req, &inviteClient{c: c})
	if err != nil {
		c.terminateLocked(TimedOut)
		return nil, fmt.Errorf("testue: send INVITE: %w", err)
	}

	c.itx = itx

	return c, nil
}

func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

// inviteClient handles the responses to the caller's initial INVITE.
type inviteClient struct {
	c *Call
}

func (h *inviteClient) HandleResponse(res *sip.Response) {
	c := h.c
	c.event(Event{Response: res})

	switch {
	case res.StatusCode == 100:
	case res.IsProvisional():
		c.provisionalReceived(res)
	case res.IsSuccess():
		c.successReceived(res)
	default:
		c.mu.Lock()
		defer c.mu.Unlock()

		c.final = res

		reason := Rejected
		if c.cancelled && res.StatusCode == 487 {
			reason = Cancelled
		}

		c.terminateLocked(reason)
	}
}

func (h *inviteClient) HandleError(err error) {
	c := h.c
	c.event(Event{Err: err})

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.final == nil {
		c.finalErr = err
		c.terminateLocked(TimedOut)
	}
}

// earlyLocked creates or updates the dialog from a response to the INVITE.
// It reports false for a response from another fork, which the single target
// of the IMS never sends.
func (c *Call) earlyLocked(res *sip.Response) bool {
	if c.d == nil {
		d, err := dialog.NewUAC(c.invite, res)
		if err != nil {
			return false
		}

		c.d, c.state = d, CallEarly

		return true
	}

	return dialog.ResponseID(res) == c.d.ID() && c.d.ReceiveResponse(res) == nil
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

	if !c.earlyLocked(res) {
		c.mu.Unlock()
		return
	}

	reliable := has100rel(res.Header, "Require")

	if reliable {
		// Retransmissions, and responses out of order, get no PRACK (RFC
		// 3262 §4).
		rseq, err := res.Header.RSeq()
		if err != nil || (c.haveRSeq && rseq != c.rseq+1) {
			c.mu.Unlock()
			return
		}

		c.rseq, c.haveRSeq = rseq, true
	}

	if err := c.answerReceivedLocked(res.Header.ContentType(), res.Body); err != nil {
		c.event(Event{Err: err})
	}

	c.notifyLocked()
	c.mu.Unlock()

	if reliable {
		c.background(func(ctx context.Context) error { return c.prack(ctx, res) })
	}
}

// answerReceivedLocked takes the first description from the callee as the
// answer to the INVITE's offer.
func (c *Call) answerReceivedLocked(contentType string, body []byte) error {
	if c.m.remote != nil || mediaType(contentType) != sdp.ContentType || len(body) == 0 {
		return nil
	}

	s, err := sdp.Parse(body)
	if err != nil {
		return fmt.Errorf("testue: answer: %w", err)
	}

	return c.m.answered(s)
}

func (c *Call) successReceived(res *sip.Response) {
	c.mu.Lock()

	if c.ack != nil {
		// A retransmission of the 2xx: its ACK again.
		ack := c.ack.Clone()
		c.mu.Unlock()

		c.sendAck(ack)

		return
	}

	if !c.earlyLocked(res) {
		c.mu.Unlock()
		return
	}

	if err := c.answerReceivedLocked(res.Header.ContentType(), res.Body); err != nil {
		c.event(Event{Err: err})
	}

	ack, err := c.d.NewAck(c.invite)
	if err == nil {
		err = c.u.prepare(ack)
	}

	if err != nil {
		c.event(Event{Err: err})
		c.mu.Unlock()

		return
	}

	c.ack, c.final = ack, res
	c.ackSent = c.sendAck(ack.Clone())

	cancelled := c.cancelled
	if c.state != CallTerminated {
		c.state = CallConfirmed
		c.sessionTimerLocked(sessionExpires(res.Header, "uac"))
	}

	c.notifyLocked()
	c.mu.Unlock()

	if cancelled {
		// The 2xx crossed the CANCEL (RFC 3261 §9.1).
		c.background(func(ctx context.Context) error { return c.bye(ctx, Cancelled) })
	}
}

// sendAck sends an ACK to a 2xx; the channel is closed once it has left.
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

// background runs f outside the transaction layer's callbacks; its error is
// an event of the call.
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

// newRequest builds an in-dialog request.
func (c *Call) newRequest(method string) (*sip.Request, error) {
	c.mu.Lock()

	if c.d == nil || c.state == CallTerminated {
		c.mu.Unlock()
		return nil, ErrCallEnded
	}

	d := c.d
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

// send sends an in-dialog request other than INVITE and returns its final
// response.
func (c *Call) send(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	res, err := c.u.request(ctx, req)
	if err != nil {
		c.event(Event{Err: err})
		return nil, err
	}

	c.event(Event{Response: res})

	c.mu.Lock()
	d := c.d
	c.mu.Unlock()

	if err := d.ReceiveResponse(res); err != nil {
		return res, fmt.Errorf("testue: %s response: %w", req.Method, err)
	}

	if !res.IsSuccess() {
		return res, &ResponseError{Response: res}
	}

	return res, nil
}

// prack acknowledges a reliable provisional response, then, with
// preconditions, reports the local resources reserved in an UPDATE (IR.92
// §2.4.1).
func (c *Call) prack(ctx context.Context, res *sip.Response) error {
	c.mu.Lock()
	d := c.d
	c.mu.Unlock()

	prack, err := d.NewPrack(res)
	if err != nil {
		return fmt.Errorf("testue: %w", err)
	}

	if err := c.u.prepare(prack); err != nil {
		return err
	}

	if _, err := c.send(ctx, prack); err != nil {
		return err
	}

	c.mu.Lock()
	update := c.m.precondition && !c.m.met() && c.m.remote != nil && !c.offering && c.state == CallEarly
	c.mu.Unlock()

	if !update {
		return nil
	}

	return c.update(ctx, true)
}

// update sends an UPDATE, with a new offer or as a session refresh.
func (c *Call) update(ctx context.Context, withOffer bool) error {
	req, err := c.newRequest("UPDATE")
	if err != nil {
		return err
	}

	c.mu.Lock()

	if c.offering {
		c.mu.Unlock()
		return fmt.Errorf("%w: an offer is pending", ErrCallState)
	}

	previous := c.m.local

	if withOffer {
		offer, err := c.m.offer()
		if err != nil {
			c.mu.Unlock()
			return err
		}

		c.offering = true

		req.SetBody(sdp.ContentType, offer.Bytes())

		if c.m.precondition {
			req.Header.Add("Require", "precondition")
		}
	}

	interval := c.interval
	c.mu.Unlock()

	if interval > 0 || !withOffer {
		if interval <= 0 {
			interval = DefaultSessionExpires
		}

		req.Header.Add("Session-Expires", seconds(interval)+";refresher=uac")
		req.Header.Add("Supported", "timer")
	}

	res, err := c.send(ctx, req)

	c.mu.Lock()
	defer c.mu.Unlock()

	if withOffer {
		c.offering = false
	}

	if err != nil {
		if withOffer {
			// A refused offer leaves the session as it was (RFC 3264 §8).
			c.m.local = previous
		}

		c.notifyLocked()

		return err
	}

	if withOffer {
		if err := c.answerOfOfferLocked(res.Header.ContentType(), res.Body); err != nil {
			return err
		}
	}

	c.sessionTimerLocked(sessionExpires(res.Header, "uac"))
	c.notifyLocked()

	return nil
}

func (c *Call) answerOfOfferLocked(contentType string, body []byte) error {
	if mediaType(contentType) != sdp.ContentType || len(body) == 0 {
		return errors.New("testue: no answer to the offer")
	}

	s, err := sdp.Parse(body)
	if err != nil {
		return fmt.Errorf("testue: answer: %w", err)
	}

	return c.m.answered(s)
}

// Wait waits for the final response to the INVITE. It returns a
// ResponseError for a final response of 300 or above.
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

	// The ACK leaves before anything that follows the call's answer.
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

// Cancel cancels the INVITE and waits for the call to end. A 2xx that crosses
// the CANCEL is acknowledged, then the call is ended with a BYE.
func (c *Call) Cancel(ctx context.Context) error {
	c.mu.Lock()

	if c.incoming || c.itx == nil {
		c.mu.Unlock()
		return fmt.Errorf("%w: Cancel on an incoming call", ErrCallState)
	}

	if c.state == CallConfirmed || c.state == CallTerminated {
		c.mu.Unlock()
		return fmt.Errorf("%w: Cancel of a %s call", ErrCallState, c.state)
	}

	c.cancelled = true
	itx := c.itx
	c.mu.Unlock()

	var extra []sip.Field
	for _, v := range c.u.verify() {
		extra = append(extra, sip.Field{Name: "Security-Verify", Value: v})
	}

	if err := itx.Cancel(extra...); err != nil {
		return fmt.Errorf("testue: CANCEL: %w", err)
	}

	return c.waitFor(ctx, func() bool { return c.state == CallTerminated })
}

// Bye ends a confirmed call.
func (c *Call) Bye(ctx context.Context) error {
	return c.bye(ctx, LocalBye)
}

func (c *Call) bye(ctx context.Context, reason EndReason) error {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()

	if state != CallConfirmed {
		return fmt.Errorf("%w: BYE in a %s call", ErrCallState, state)
	}

	req, err := c.newRequest("BYE")
	if err != nil {
		return err
	}

	_, err = c.send(ctx, req)

	c.terminate(reason)

	return err
}

// Hold puts the call on hold with a re-INVITE offering sendonly; Resume
// takes it off hold.
func (c *Call) Hold(ctx context.Context) error {
	return c.reinvite(ctx, sdp.SendOnly)
}

func (c *Call) Resume(ctx context.Context) error {
	return c.reinvite(ctx, sdp.SendRecv)
}

// Refresh refreshes the session with an UPDATE without a body (RFC 4028 §7.4,
// IR.92 §2.2.8).
func (c *Call) Refresh(ctx context.Context) error {
	return c.update(ctx, false)
}

func (c *Call) reinvite(ctx context.Context, direction sdp.Direction) error {
	req, err := c.newRequest("INVITE")
	if err != nil {
		return err
	}

	c.mu.Lock()

	if c.state != CallConfirmed || c.offering || c.accepted != nil {
		state := c.state
		c.mu.Unlock()

		return fmt.Errorf("%w: re-INVITE in a %s call, or during another offer", ErrCallState, state)
	}

	previous, previousLocal := c.m.direction, c.m.local
	c.m.direction = direction

	offer, err := c.m.offer()
	if err != nil {
		c.m.direction = previous
		c.mu.Unlock()

		return err
	}

	c.offering = true
	interval := c.interval
	c.mu.Unlock()

	req.Header.Add("Supported", "100rel, timer")

	if interval > 0 {
		req.Header.Add("Session-Expires", seconds(interval)+";refresher=uac")
	}

	req.SetBody(sdp.ContentType, offer.Bytes())

	h := &reinviteClient{c: c, req: req, final: make(chan *sip.Response, 1), err: make(chan error, 1)}

	var res *sip.Response

	if _, err = c.u.layer.Request(req, h); err == nil {
		select {
		case res = <-h.final:
		case err = <-h.err:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}

	h.mu.Lock()
	sent := h.sent
	h.mu.Unlock()

	if err == nil && sent != nil {
		// The ACK leaves before the next offer.
		select {
		case <-sent:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.offering = false
	c.notifyLocked()

	switch {
	case err != nil:
		c.m.direction, c.m.local = previous, previousLocal
		return fmt.Errorf("testue: re-INVITE: %w", err)
	case !res.IsSuccess():
		// A refused offer leaves the session as it was (RFC 3264 §8).
		c.m.direction, c.m.local = previous, previousLocal
		return &ResponseError{Response: res}
	}

	if err := c.answerOfOfferLocked(res.Header.ContentType(), res.Body); err != nil {
		return err
	}

	c.sessionTimerLocked(sessionExpires(res.Header, "uac"))

	return nil
}

// reinviteClient handles the responses to a re-INVITE.
type reinviteClient struct {
	c     *Call
	req   *sip.Request
	final chan *sip.Response
	err   chan error

	mu   sync.Mutex
	ack  *sip.Request
	sent <-chan struct{}
}

func (h *reinviteClient) HandleResponse(res *sip.Response) {
	c := h.c
	c.event(Event{Response: res})

	if res.IsProvisional() {
		return
	}

	if res.IsSuccess() {
		h.mu.Lock()

		if h.ack == nil {
			c.mu.Lock()
			d := c.d
			c.mu.Unlock()

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

	select {
	case h.final <- res:
	default:
	}
}

func (h *reinviteClient) HandleError(err error) {
	h.c.event(Event{Err: err})

	select {
	case h.err <- err:
	default:
	}
}

// sessionExpires returns the session interval of a 2xx, or of a refresh
// request, and whether the given role, uac or uas, refreshes it (RFC 4028
// §9).
func sessionExpires(h sip.Header, role string) (time.Duration, bool) {
	v := h.Get("Session-Expires")
	if v == "" {
		v = h.Get("x")
	}

	value, params, err := sip.ParseTokenParams(v)
	if err != nil {
		return 0, false
	}

	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, false
	}

	refresher, ok := params.Get("refresher")
	if !ok {
		refresher = "uac"
	}

	return time.Duration(n) * time.Second, strings.EqualFold(refresher, role)
}

// sessionTimerLocked arms the session timer: the refresher refreshes at half
// the interval, the other side sends a BYE when it runs out (RFC 4028 §10).
func (c *Call) sessionTimerLocked(interval time.Duration, refresher bool) {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}

	c.interval, c.refresher = interval, refresher

	if interval <= 0 || !c.auto || c.state == CallTerminated {
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

// has100rel reports whether the option tags of the named field include 100rel
// (RFC 3262).
func has100rel(h sip.Header, name string) bool {
	for _, e := range h.Elements(name) {
		if strings.EqualFold(strings.TrimSpace(e), "100rel") {
			return true
		}
	}

	return false
}

// The callee's side.

func (u *UE) incomingCall(tx *transaction.ServerTransaction, req *sip.Request) {
	c := u.newCall(true, req, callKey{callID: req.Header.CallID(), tag: tx.ToTag()}, false)
	c.stx = tx
	c.rel100 = has100rel(req.Header, "Supported") || has100rel(req.Header, "Require")
	c.rseqOut = randomRSeq()

	if mediaType(req.Header.ContentType()) == sdp.ContentType && len(req.Body) > 0 {
		offer, err := sdp.Parse(req.Body)
		if err != nil {
			_ = tx.Respond(sip.NewResponse(req, 400, "Bad SDP"))
			return
		}

		if !offerAudio(offer) {
			res := sip.NewResponse(req, 488, "")
			if w, err := sip.NewWarning(304, sip.FormatHost(u.cfg.Local)); err == nil {
				res.Header.Add("Warning", w.String())
			}

			_ = tx.Respond(res)

			return
		}

		c.offer = offer
	}

	u.mu.Lock()
	u.calls.active[c.key] = c
	u.mu.Unlock()

	select {
	case u.calls.incoming <- c:
	default:
		c.terminate(Rejected)

		_ = tx.Respond(sip.NewResponse(req, 486, ""))
	}
}

// offerAudio reports whether the offer has an audio stream with one of our
// codecs.
func offerAudio(offer *sdp.Session) bool {
	for _, m := range offer.Media {
		if m.Type() == sdp.Audio && m.Port() != 0 && choose(m) != nil {
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

// responseLocked builds a response to the INVITE that creates or updates the
// early dialog, or confirms it for a 2xx.
func (c *Call) responseLocked(code int) (*sip.Response, error) {
	res := sip.NewResponse(c.invite, code, "")
	if err := res.Header.SetToTag(c.stx.ToTag()); err != nil {
		return nil, err
	}

	res.Header.Add("Contact", c.u.callContact())
	res.Header.Add("Allow", allow)

	supported := "100rel, timer"
	if c.m.precondition {
		supported += ", precondition"
	}

	res.Header.Add("Supported", supported)

	if c.d == nil {
		d, err := dialog.NewUAS(c.invite, res)
		if err != nil {
			return nil, fmt.Errorf("testue: %w", err)
		}

		c.d = d
	} else {
		c.d.PrepareResponse(c.invite, res)
	}

	if res.IsSuccess() {
		c.state = CallConfirmed
	} else {
		c.state = CallEarly
	}

	return res, nil
}

// sdpLocked puts the answer to the INVITE's offer in res, or, without an
// offer, an offer of ours, whose answer comes in the PRACK or the ACK.
func (c *Call) sdpLocked(res *sip.Response) (bool, error) {
	if c.answered {
		return false, nil
	}

	var (
		s   *sdp.Session
		err error
	)

	if c.offer != nil {
		s, err = c.m.answer(c.offer)
	} else {
		s, err = c.m.offer()
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

// Ring alerts the caller. When the caller supports 100rel, a reliable 183
// carries the answer first, and, with preconditions, the 180 waits until
// they are met (IR.92 §2.4.1, TS 24.229 §5.1.4.1).
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

		if err := c.waitFor(ctx, func() bool { return c.m.met() || c.state == CallTerminated }); err != nil {
			return err
		}
	}

	return c.provisional(ctx, 180, has100rel(c.invite.Header, "Require"))
}

// provisional sends a provisional response, and, for a reliable one, waits
// for its PRACK.
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
		if _, err := c.sdpLocked(res); err != nil {
			c.mu.Unlock()
			return err
		}

		require := "100rel"
		if c.m.precondition {
			require = "precondition, 100rel"
		}

		res.Header.Add("Require", require)
		res.Header.Add("RSeq", strconv.FormatUint(uint64(c.rseqOut), 10))

		r = &reliable{res: res, rseq: c.rseqOut, interval: c.u.layer.T1(), started: time.Now()}
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

// retransmitReliable resends a reliable provisional response until its PRACK,
// for 64*T1, after which the INVITE is rejected (RFC 3262 §3).
func (c *Call) retransmitReliable(r *reliable) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.unacked != r {
		return
	}

	t1 := c.u.layer.T1()

	if time.Since(r.started) >= 64*t1 {
		res := sip.NewResponse(c.invite, 504, "No PRACK")
		_ = res.Header.SetToTag(c.stx.ToTag())
		_ = c.stx.Respond(res)

		c.terminateLocked(TimedOut)

		return
	}

	_ = c.stx.Respond(r.res)

	r.interval *= 2
	r.timer = time.AfterFunc(r.interval, func() { c.retransmitReliable(r) })
}

// Answer answers the call with a 200 and waits for its ACK.
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

	if interval, ok := sessionExpires(c.invite.Header, "uac"); interval > 0 {
		refresher := "uas"
		if ok {
			refresher = "uac"
		}

		res.Header.Add("Session-Expires", seconds(interval)+";refresher="+refresher)
		res.Header.Add("Require", "timer")

		c.sessionTimerLocked(interval, !ok)
	}

	cseq, _ := c.invite.Header.CSeq()
	a := c.acceptLocked(c.stx, res, cseq.Seq)
	a.answerInAck = inAck

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

// acceptLocked starts resending a 2xx to an INVITE until its ACK, for 64*T1,
// after which the call ends with a BYE (RFC 3261 §13.3.1.4).
func (c *Call) acceptLocked(stx *transaction.ServerTransaction, res *sip.Response, cseq uint32) *accepted {
	a := &accepted{stx: stx, res: res, cseq: cseq, interval: c.u.layer.T1(), started: time.Now()}
	c.accepted = a
	a.timer = time.AfterFunc(a.interval, func() { c.retransmitAccepted(a) })

	return a
}

func (c *Call) retransmitAccepted(a *accepted) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accepted != a {
		return
	}

	t1 := c.u.layer.T1()

	if time.Since(a.started) >= 64*t1 {
		c.accepted = nil
		c.background(func(ctx context.Context) error { return c.bye(ctx, TimedOut) })

		return
	}

	_ = a.stx.Respond(a.res)

	a.interval = min(2*a.interval, 8*t1)
	a.timer = time.AfterFunc(a.interval, func() { c.retransmitAccepted(a) })
}

// Reject answers the call with a final response of 300 or above.
func (c *Call) Reject(code int) error {
	if code < 300 || code > 699 {
		return fmt.Errorf("testue: Reject with %d", code)
	}

	c.mu.Lock()

	if err := c.checkIncoming("Reject"); err != nil {
		c.mu.Unlock()
		return err
	}

	res := sip.NewResponse(c.invite, code, "")
	if err := res.Header.SetToTag(c.stx.ToTag()); err != nil {
		c.mu.Unlock()
		return err
	}

	if c.d != nil {
		c.d.PrepareResponse(c.invite, res)
	}

	c.terminateLocked(Rejected)

	stx := c.stx
	c.mu.Unlock()

	return stx.Respond(res)
}

// cancelReceived ends a call the caller cancelled (RFC 3261 §9.2).
func (c *Call) cancelReceived(cancel *sip.Request) {
	c.event(Event{Request: cancel})

	c.mu.Lock()

	if c.state == CallConfirmed || c.state == CallTerminated {
		c.mu.Unlock()
		return
	}

	res := sip.NewResponse(c.invite, 487, "")
	_ = res.Header.SetToTag(c.stx.ToTag())

	if c.d != nil {
		c.d.PrepareResponse(c.invite, res)
	}

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

	a.acked = true
	a.timer.Stop()

	c.accepted = nil

	if a.answerInAck {
		if err := c.answerOfOfferLocked(ack.Header.ContentType(), ack.Body); err != nil {
			c.event(Event{Err: err})
		}
	}

	c.notifyLocked()
}

// requestReceived handles a request inside the call's dialog.
func (c *Call) requestReceived(tx *transaction.ServerTransaction, req *sip.Request) {
	c.event(Event{Request: req})

	c.mu.Lock()

	if c.d == nil {
		c.mu.Unlock()

		_ = tx.Respond(sip.NewResponse(req, 481, ""))

		return
	}

	if req.Method == "INVITE" && (c.offering || c.accepted != nil) {
		// Glare (RFC 3261 §14.2).
		code := 491
		if c.accepted != nil {
			code = 500
		}

		c.mu.Unlock()

		_ = tx.Respond(sip.NewResponse(req, code, ""))

		return
	}

	if err := c.d.ReceiveRequest(req); err != nil {
		c.mu.Unlock()

		code := 400
		if serr, ok := errors.AsType[*sip.StatusError](err); ok {
			code = serr.StatusCode
		}

		_ = tx.Respond(sip.NewResponse(req, code, ""))

		return
	}

	var res *sip.Response

	switch req.Method {
	case "PRACK":
		res = c.prackReceivedLocked(req)
	case "UPDATE", "INVITE":
		res = c.offerReceivedLocked(tx, req)
	case "BYE":
		res = sip.NewResponse(req, 200, "")

		if c.incoming && c.state == CallEarly {
			end := sip.NewResponse(c.invite, 487, "")
			_ = end.Header.SetToTag(c.stx.ToTag())
			_ = c.stx.Respond(end)
		}

		c.terminateLocked(RemoteBye)
	}

	c.mu.Unlock()

	if res == nil {
		return
	}

	_ = tx.Respond(res)

	// What waits on the request, like the 180 on the preconditions, follows
	// its response.
	c.mu.Lock()
	c.notifyLocked()
	c.mu.Unlock()
}

func (c *Call) prackReceivedLocked(req *sip.Request) *sip.Response {
	rack, err := req.Header.RAck()
	cseq, _ := c.invite.Header.CSeq()

	r := c.unacked
	if err != nil || r == nil || rack.RSeq != r.rseq || rack.CSeq != cseq.Seq || rack.Method != "INVITE" {
		return sip.NewResponse(req, 481, "")
	}

	r.timer.Stop()

	c.unacked = nil

	if c.offer == nil && c.answered {
		// The PRACK answers the offer of the reliable provisional response.
		if err := c.answerOfOfferLocked(req.Header.ContentType(), req.Body); err != nil {
			c.event(Event{Err: err})
		}
	}

	return sip.NewResponse(req, 200, "")
}

// offerReceivedLocked answers an UPDATE or a re-INVITE: the answer to its
// offer, and, for a session refresh, its Session-Expires (RFC 4028 §9).
func (c *Call) offerReceivedLocked(tx *transaction.ServerTransaction, req *sip.Request) *sip.Response {
	if req.Method == "UPDATE" && c.offering {
		return sip.NewResponse(req, 491, "")
	}

	res := sip.NewResponse(req, 200, "")
	dialog.CopyRecordRoute(res, req)
	res.Header.Add("Contact", c.u.callContact())
	res.Header.Add("Allow", allow)

	inAck := false

	switch {
	case mediaType(req.Header.ContentType()) == sdp.ContentType && len(req.Body) > 0:
		offer, err := sdp.Parse(req.Body)
		if err != nil {
			return sip.NewResponse(req, 400, "Bad SDP")
		}

		answer, err := c.m.answer(offer)
		if errors.Is(err, ErrNoCodec) {
			return sip.NewResponse(req, 488, "")
		}

		if err != nil {
			return sip.NewResponse(req, 500, "")
		}

		res.SetBody(sdp.ContentType, answer.Bytes())

		if c.m.precondition {
			res.Header.Add("Require", "precondition")
		}
	case req.Method == "INVITE":
		// A re-INVITE without an offer gets ours; the answer comes in the
		// ACK.
		offer, err := c.m.offer()
		if err != nil {
			return sip.NewResponse(req, 500, "")
		}

		res.SetBody(sdp.ContentType, offer.Bytes())

		inAck = true
	}

	if interval, ok := sessionExpires(req.Header, "uas"); interval > 0 {
		refresher := "uac"
		if ok {
			refresher = "uas"
		}

		res.Header.Add("Session-Expires", seconds(interval)+";refresher="+refresher)
		res.Header.Add("Require", "timer")

		c.sessionTimerLocked(interval, ok)
	}

	if req.Method == "INVITE" {
		cseq, _ := req.Header.CSeq()

		a := c.acceptLocked(tx, res, cseq.Seq)
		a.answerInAck = inAck
	}

	return res
}
