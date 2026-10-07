package testue

import (
	"context"
	"crypto/rand"
	"encoding/binary"
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
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	DefaultSessionExpires = 1800 * time.Second

	mmtelService = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

	allow = "INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE"

	firstMediaPort = 40000
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
	Preconditions bool

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
	invite  *sip.Request
	d       *dialog.Dialog
	m       *media

	offering bool

	saHeld bool

	itx       *transaction.ClientTransaction
	final     *sip.Response
	finalErr  error
	ack       *sip.Request
	ackSent   <-chan struct{}
	rseq      uint32
	haveRSeq  bool
	cancelled bool
	retried   bool

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

func (c *Call) ID() dialog.ID {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.d == nil {
		return dialog.ID{}
	}

	return c.d.ID()
}

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

func (c *Call) PreconditionsMet() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.m.met()
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
func (c *Call) localOfferLocked() bool {
	return c.offering || (c.unacked != nil && c.unacked.offer) || (c.accepted != nil && c.accepted.answerInAck)
}

func (c *Call) remoteOfferLocked() bool {
	return c.incoming && c.offer != nil && !c.answered
}

// RFC 4028 §7.4
func (c *Call) supportedLocked() string {
	if c.m.precondition {
		return "100rel, timer, precondition"
	}

	return "100rel, timer"
}

// TS 24.229 §5.1.2A.1.1
func (u *UE) callContact() string {
	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	return sip.Address{URI: uri, Params: sip.Params{
		{Name: "+g.3gpp.icsi-ref", Value: icsiMMTel},
		{Name: "audio"},
	}}.String()
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

	c := u.newCall(false, req, callKey{callID: callID, tag: tag}, opts.Preconditions)

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

	offer, err := c.m.offer(sdp.SendRecv)
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
	case res.IsProvisional():
		c.provisionalReceived(res)
	case res.IsSuccess():
		c.successReceived(res)
	case res.StatusCode == 422 && c.retryInterval(res):
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

	if c.final != nil {
		return
	}

	c.finalErr = err

	reason := TimedOut
	if c.cancelled {
		reason = Cancelled
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
	if to.Tag() == "" || res.StatusCode == 199 {
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

	reliable := has(res.Header, "Require", "100rel")

	if reliable {
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
	c.releaseSALocked()

	cancelled := c.cancelled
	if c.state != CallTerminated {
		c.state = CallConfirmed

		interval, refresher, _ := parseSessionExpires(res.Header)
		c.sessionTimerLocked(interval, refresher != "uas")
	}

	c.notifyLocked()
	c.mu.Unlock()

	if cancelled {
		c.background(func(ctx context.Context) error { return c.bye(ctx, Cancelled) })
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
func (c *Call) send(ctx context.Context, build func() (*sip.Request, error),
	settle func(res *sip.Response, err error) error,
) (*sip.Response, error) {
	h := &dialogClient{c: c, settle: settle, done: make(chan outcome, 1)}

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

			if rerr := c.d.ReceiveResponse(res); rerr != nil {
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
func (c *Call) prack(ctx context.Context, res *sip.Response) error {
	c.mu.Lock()
	d := c.d
	c.mu.Unlock()

	_, err := c.send(ctx, func() (*sip.Request, error) {
		prack, err := d.NewPrack(res)
		if err != nil {
			return nil, fmt.Errorf("testue: %w", err)
		}

		return prack, c.u.prepare(prack)
	}, func(r *sip.Response, err error) error {
		if err != nil && dialogLost(r, err) {
			c.terminateLocked(TimedOut)
		}

		return err
	})
	if err != nil {
		return err
	}

	c.mu.Lock()
	update := c.state == CallEarly && c.m.precondition && !c.m.met() && c.m.remote != nil && !c.localOfferLocked()
	c.mu.Unlock()

	if !update {
		return nil
	}

	return c.update(ctx, true)
}

func (c *Call) update(ctx context.Context, withOffer bool) error {
	var (
		previous *sdp.Session
		offered  bool
	)

	if withOffer {
		ctx = context.WithoutCancel(ctx)
	}

	_, err := c.send(ctx, func() (*sip.Request, error) {
		req, err := c.newRequest("UPDATE")
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if withOffer {
			if c.localOfferLocked() || c.remoteOfferLocked() {
				return nil, fmt.Errorf("%w: an offer is pending", ErrCallState)
			}

			previous = c.m.local

			offer, err := c.m.offer(c.m.direction)
			if err != nil {
				return nil, err
			}

			c.offering, offered = true, true

			req.SetBody(sdp.ContentType, offer.Bytes())

			if c.m.precondition {
				req.Header.Add("Require", "precondition")
			}
		}

		req.Header.Add("Supported", c.supportedLocked())

		if interval := c.interval; interval > 0 || !withOffer {
			if interval <= 0 {
				interval = DefaultSessionExpires
			}

			req.Header.Add("Session-Expires", seconds(interval)+";refresher="+c.refresherLocked())
		}

		return req, nil
	}, func(res *sip.Response, err error) error {
		defer c.notifyLocked()

		if offered {
			c.offering = false
		}

		if err != nil {
			if offered {
				c.m.local = previous
			}

			if withOffer && dialogLost(res, err) {
				c.terminateLocked(TimedOut)
			}

			return err
		}

		if withOffer {
			if err := c.answerOfOfferLocked(res.Header.ContentType(), res.Body); err != nil {
				return err
			}
		}

		interval, refresher, _ := parseSessionExpires(res.Header)
		c.sessionTimerLocked(interval, refresher != "uas")

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

	if err := itx.Cancel(); err != nil {
		return fmt.Errorf("testue: CANCEL: %w", err)
	}

	return c.waitFor(ctx, func() bool { return c.state == CallTerminated })
}

func (c *Call) Bye(ctx context.Context) error {
	return c.bye(ctx, LocalBye)
}

// RFC 3261 §15.1.1
func (c *Call) bye(ctx context.Context, reason EndReason) error {
	res, err := c.send(ctx, func() (*sip.Request, error) {
		req, err := c.newRequest("BYE")
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if c.state != CallConfirmed {
			return nil, fmt.Errorf("%w: BYE in a %s call", ErrCallState, c.state)
		}

		c.terminateLocked(reason)

		return req, nil
	}, nil)

	if res != nil {
		return nil
	}

	return err
}

// RFC 3264 §8.4
func (c *Call) Hold(ctx context.Context) error {
	return c.reinvite(ctx, sdp.SendOnly)
}

func (c *Call) Resume(ctx context.Context) error {
	return c.reinvite(ctx, sdp.SendRecv)
}

// RFC 4028 §7.4, §10, IR.92 §2.2.8
func (c *Call) Refresh(ctx context.Context) error {
	err := c.update(ctx, false)

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

// RFC 3261 §14.1
func (c *Call) reinvite(ctx context.Context, direction sdp.Direction) error {
	if err := c.waitFor(ctx, func() bool { return c.accepted == nil || c.state == CallTerminated }); err != nil {
		return err
	}

	var (
		previous      sdp.Direction
		previousLocal *sdp.Session
	)

	h := &reinviteClient{c: c, done: make(chan error, 1)}

	h.settle = func(res *sip.Response, err error) error {
		c.offering = false
		c.notifyLocked()

		switch {
		case err != nil || !res.IsSuccess():
			c.m.direction, c.m.local = previous, previousLocal

			if dialogLost(res, err) {
				c.terminateLocked(TimedOut)
			}

			if err != nil {
				return err
			}

			return &ResponseError{Response: res}
		}

		if err := c.answerOfOfferLocked(res.Header.ContentType(), res.Body); err != nil {
			return err
		}

		interval, refresher, _ := parseSessionExpires(res.Header)
		c.sessionTimerLocked(interval, refresher != "uas")

		return nil
	}

	err := c.submit(func() (*sip.Request, error) {
		req, err := c.newRequest("INVITE")
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if c.state != CallConfirmed || c.localOfferLocked() || c.accepted != nil {
			return nil, fmt.Errorf("%w: re-INVITE in a %s call, or during another offer", ErrCallState, c.state)
		}

		offered := direction
		if direction == sdp.SendOnly && c.m.held() {
			offered = sdp.Inactive
		}

		previous, previousLocal = c.m.direction, c.m.local
		c.m.direction = direction

		offer, err := c.m.offer(offered)
		if err != nil {
			c.m.direction = previous
			return nil, err
		}

		c.offering = true

		req.Header.Add("Supported", c.supportedLocked())

		if c.m.precondition {
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

type reinviteClient struct {
	c      *Call
	req    *sip.Request
	settle func(res *sip.Response, err error) error
	once   sync.Once
	done   chan error

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

	c.background(func(ctx context.Context) error {
		c.mu.Lock()
		d := c.d
		c.mu.Unlock()

		_, err := c.send(ctx, func() (*sip.Request, error) {
			prack, err := d.NewPrack(res)
			if err != nil {
				return nil, fmt.Errorf("testue: %w", err)
			}

			return prack, c.u.prepare(prack)
		}, nil)

		return err
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

	c := u.newCall(true, req, callKey{callID: req.Header.CallID(), tag: tx.ToTag()}, precondition)
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
		s, err = c.m.offer(sdp.SendRecv)
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
		update := c.state == CallEarly && !c.m.met() && c.m.remoteQoS == sdp.QoSSendRecv && !c.localOfferLocked()
		c.mu.Unlock()

		if update {
			if err := c.update(ctx, true); err != nil {
				return err
			}
		}

		if err := c.waitFor(ctx, func() bool { return c.m.met() || c.state == CallTerminated }); err != nil {
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
		if c.m.precondition {
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

	if c.d != nil {
		c.d.PrepareResponse(c.invite, res)
	}

	c.terminateLocked(Rejected)

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

	if a.initial {
		c.releaseSALocked()
	}

	if a.answerInAck {
		if err := c.answerOfOfferLocked(ack.Header.ContentType(), ack.Body); err != nil {
			c.event(Event{Err: err})
		}
	}

	c.notifyLocked()
}

func (c *Call) requestReceived(tx *transaction.ServerTransaction, req *sip.Request) {
	c.event(Event{Request: req})

	c.mu.Lock()

	var (
		res   *sip.Response
		extra func()
	)

	switch {
	case c.d == nil:
		res = c.u.response(req, 481)
	case req.Method == "INVITE" && c.localOfferLocked():
		res = c.u.response(req, 491)
	case req.Method == "INVITE" && c.accepted != nil:
		res = retryAfter(c.u.response(req, 500))
	default:
		if err := c.d.ReceiveRequest(req); err != nil {
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
			res = c.offerReceivedLocked(tx, req)
		case "BYE":
			res = c.u.response(req, 200)

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
		if err := c.answerOfOfferLocked(req.Header.ContentType(), req.Body); err != nil {
			c.event(Event{Err: err})
		}
	}

	return c.u.response(req, 200)
}

// RFC 4028
func (c *Call) offerReceivedLocked(tx *transaction.ServerTransaction, req *sip.Request) *sip.Response {
	hasSDP := mediaType(req.Header.ContentType()) == sdp.ContentType && len(req.Body) > 0

	if req.Method == "UPDATE" && hasSDP {
		switch {
		case c.localOfferLocked():
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

		answer, err := c.m.answer(offer)
		if errors.Is(err, ErrNoCodec) {
			return c.u.response(req, 488)
		}

		if err != nil {
			return c.u.response(req, 500)
		}

		res.SetBody(sdp.ContentType, answer.Bytes())

		if c.m.precondition && (has(req.Header, "Supported", "precondition") || has(req.Header, "Require", "precondition")) {
			res.Header.Add("Require", "precondition")
		}
	case req.Method == "INVITE":
		offer, err := c.m.offer(c.m.direction)
		if err != nil {
			return c.u.response(req, 500)
		}

		res.SetBody(sdp.ContentType, offer.Bytes())

		inAck = true
	}

	c.addSessionTimerLocked(req, res, false)

	if req.Method == "INVITE" {
		cseq, _ := req.Header.CSeq()

		a := c.acceptLocked(tx, res, cseq.Seq)
		a.answerInAck = inAck
	}

	return res
}
