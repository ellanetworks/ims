package ua

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ellanetworks/ims/sip"
)

type PublicationHandler struct {
	Published func(ctx context.Context, p *Publication, res *sip.Response)

	Ended func(ctx context.Context, p *Publication, e End)
}

// RFC 3903 section 4
type Publication struct {
	a  *Agent
	st *Strand
	h  PublicationHandler

	tmpl     *sip.Request
	state    []sip.BodyPart
	attached []sip.BodyPart
	etag     string

	expiresAt time.Time
	refresh   *timer
	expiry    *timer

	inflight *publishRequest
	next     *publishRequest
	ended    bool
	value    any

	closing bool
}

// RFC 3903 section 4.2
func (a *Agent) Publish(ctx context.Context, req *sip.Request, h PublicationHandler, opts ...Option) (*Publication, error) {
	if req.Method != "PUBLISH" {
		return nil, fmt.Errorf("sip/ua: Publish of a %s request", req.Method)
	}

	if _, ok, err := req.Header.Event(); !ok || err != nil {
		return nil, fmt.Errorf("sip/ua: PUBLISH without a valid Event: %v", err)
	}

	parts, err := req.Content().Parts()
	if err != nil || len(parts) == 0 {
		return nil, errors.New("sip/ua: an initial PUBLISH carries the state (RFC 3903 §4.2)")
	}

	tmpl := req.Clone()
	if err := a.completeInitial(tmpl); err != nil {
		return nil, err
	}

	tmpl.SetBodies()

	o := a.options(opts)
	p := &Publication{a: a, st: o.strand(a), h: h, tmpl: tmpl, state: parts, attached: o.attached}

	if err := a.addPublication(p); err != nil {
		return nil, err
	}

	if err := p.st.do(func() error { return p.send(&publishRequest{ctx: ctx, p: p, bodies: parts}) }); err != nil {
		a.removePublication(p)
		return nil, err
	}

	return p, nil
}

func (p *Publication) Strand() *Strand {
	return p.st
}

func (p *Publication) ETag() string {
	return get(p.st, func() string { return p.etag })
}

func (p *Publication) Expires() time.Time {
	return get(p.st, func() time.Time { return p.expiresAt })
}

func (p *Publication) Value() any {
	return get(p.st, func() any { return p.value })
}

func (p *Publication) SetValue(v any) {
	_ = p.st.do(func() error {
		p.value = v
		return nil
	})
}

// RFC 3903 section 4.4
func (p *Publication) Modify(ctx context.Context, bodies []sip.BodyPart, done func(res *sip.Response, err error)) error {
	if len(bodies) == 0 {
		return errors.New("sip/ua: a modifying PUBLISH carries the state (RFC 3903 §4.4)")
	}

	return p.st.do(func() error {
		if p.ended {
			return &Error{Op: "Modify", State: Terminated, Err: ErrState}
		}

		p.state = bodies

		return p.queue(&publishRequest{ctx: ctx, p: p, bodies: bodies, done: done})
	})
}

// RFC 3903 section 4.5
func (p *Publication) Remove(ctx context.Context, done func(res *sip.Response, err error)) error {
	return p.st.do(func() error {
		if p.ended {
			return &Error{Op: "Remove", State: Terminated, Err: ErrState}
		}

		return p.queue(&publishRequest{ctx: ctx, p: p, remove: true, done: done})
	})
}

func (p *Publication) queue(c *publishRequest) error {
	if p.inflight == nil {
		return p.send(c)
	}

	if old := p.next; old != nil {
		if old.remove && !c.remove {
			return &Error{Op: "Modify", State: Terminated, Err: ErrState}
		}

		p.emitDone(old.done, nil, &Error{Op: "Modify", State: Confirmed, Err: ErrStale})
	}

	p.next = c

	return nil
}

type publishRequest struct {
	ctx     context.Context
	p       *Publication
	bodies  []sip.BodyPart
	remove  bool
	initial bool
	done    func(*sip.Response, error)
	req     *sip.Request
	tries   int
	retried bool
	final   bool
}

// RFC 3903 section 4.1
func (c *publishRequest) build() {
	p := c.p

	req := p.tmpl.Clone()
	nextCSeq(p.tmpl)

	if p.etag != "" && !c.initial {
		req.Header.Set("SIP-If-Match", p.etag)
	}

	if c.remove {
		req.Header.Set("Expires", "0")
	}

	if bodies := append(slices.Clone(c.bodies), p.attached...); len(bodies) > 0 {
		req.SetBodies(bodies...)
	}

	c.req = req
}

func (p *Publication) send(c *publishRequest) error {
	if p.etag == "" {
		c.initial = true
		if len(c.bodies) == 0 {
			c.bodies = p.state
		}
	}

	c.build()

	if _, err := p.a.prepareAndSend(c.ctx, c.req, p.st.client(c.response, c.error)); err != nil {
		return err
	}

	p.inflight = c

	return nil
}

func (c *publishRequest) response(ctx context.Context, res *sip.Response) {
	p := c.p

	if res.IsProvisional() || c.final || p.ended {
		return
	}

	if c.retry(res) {
		if err := p.send(c); err != nil {
			c.error(ctx, err)
		}

		return
	}

	c.final = true
	p.inflight = nil

	switch {
	case res.IsSuccess():
		p.succeeded(ctx, c, res)
	case res.StatusCode == 412 && !c.initial && !c.remove:
		p.etag = ""
		p.emitDone(c.done, res, nil)

		if p.next == nil {
			p.next = &publishRequest{ctx: ctx, p: p}
		}
	case c.remove:
		p.emitDone(c.done, res, nil)
		p.end(ctx, End{Cause: EndRemoved, Response: res})

		return
	default:
		p.emitDone(c.done, res, nil)

		if c.initial || p.expiresAt.IsZero() || !p.a.clock.Now().Before(p.expiresAt) {
			p.end(ctx, End{Cause: EndRejected, Remote: true, Response: res})
			return
		}

		p.retryRefresh(ctx)
	}

	p.sendNext()
}

// RFC 3261 section 22.2
// RFC 3903 section 6
func (c *publishRequest) retry(res *sip.Response) bool {
	p := c.p

	switch {
	case res.StatusCode == 423 && !c.retried && !c.remove:
		if !raiseExpires(p.tmpl, res) {
			return false
		}

		c.retried = true
	case res.StatusCode == 401 || res.StatusCode == 407:
		next := c.req.Clone()
		if !p.a.authenticate(next, res, c.tries) {
			return false
		}

		c.tries++

		p.tmpl.Header.Set("Authorization", next.Header.Get("Authorization"))

		if v := next.Header.Get("Proxy-Authorization"); v != "" {
			p.tmpl.Header.Set("Proxy-Authorization", v)
		}
	default:
		return false
	}

	return true
}

func (c *publishRequest) error(ctx context.Context, err error) {
	p := c.p

	if c.final || p.ended {
		return
	}

	c.final = true
	p.inflight = nil

	p.emitDone(c.done, nil, err)

	if c.initial || c.remove {
		p.end(ctx, failureEnd(err))
		return
	}

	p.retryRefresh(ctx)
	p.sendNext()
}

// RFC 3903 section 4.1
// RFC 3903 section 5
func (p *Publication) succeeded(ctx context.Context, c *publishRequest, res *sip.Response) {
	if etag, ok, err := res.Header.SIPETag(); ok && err == nil {
		p.etag = etag
	}

	p.emitDone(c.done, res, nil)

	if c.remove {
		p.end(ctx, End{Cause: EndRemoved, Response: res})
		return
	}

	p.st.emit(func() {
		if p.h.Published != nil {
			p.h.Published(ctx, p, res)
		}
	})

	v, err := res.Header.Expires()
	if err != nil {
		v, err = p.tmpl.Header.Expires()
	}

	if err != nil || v == 0 {
		return
	}

	d := time.Duration(v) * time.Second
	p.expiresAt = p.a.clock.Now().Add(d)

	p.refresh.stop()
	p.refresh = p.st.detached(ctx, d-refreshLead(d), "publication refresh", p.autoRefresh)

	p.expiry.stop()
	p.expiry = p.st.detached(ctx, d, "publication expiry", func(ctx context.Context) { p.end(ctx, End{Cause: EndExpired}) })
}

// RFC 3903 section 4.3
func (p *Publication) autoRefresh(ctx context.Context) {
	if p.ended || p.inflight != nil || p.next != nil {
		return
	}

	if err := p.send(&publishRequest{ctx: ctx, p: p}); err != nil {
		p.retryRefresh(ctx)
	}
}

func (p *Publication) retryRefresh(ctx context.Context) {
	left := p.expiresAt.Sub(p.a.clock.Now())
	if p.ended || left < 2*64*p.a.layer.T1() {
		return
	}

	p.refresh.stop()
	p.refresh = p.st.detached(ctx, left/2, "publication refresh", p.autoRefresh)
}

func (p *Publication) sendNext() {
	c := p.next
	if c == nil || p.ended || p.inflight != nil {
		return
	}

	p.next = nil

	if err := p.send(c); err != nil {
		c.final = true
		p.emitDone(c.done, nil, err)
	}
}

func (p *Publication) emitDone(done func(*sip.Response, error), res *sip.Response, err error) {
	if done != nil {
		p.st.emit(func() { done(res, err) })
	}
}

func (p *Publication) end(ctx context.Context, e End) {
	p.st.check()

	if p.ended {
		return
	}

	p.ended = true
	p.refresh.stop()
	p.expiry.stop()

	if c := p.next; c != nil {
		p.next = nil
		p.emitDone(c.done, nil, &Error{Op: "Modify", State: Terminated, Err: ErrState})
	}

	p.a.removePublication(p)

	if p.closing {
		p.closing = false
		e.Cause = EndClosed

		p.a.await(-1)
	}

	p.st.emit(func() {
		if p.h.Ended != nil {
			p.h.Ended(ctx, p, e)
		}
	})
}

func (p *Publication) close(ctx context.Context) {
	if p.ended || p.closing {
		return
	}

	if p.etag == "" && p.inflight == nil {
		p.end(ctx, End{Cause: EndClosed})
		return
	}

	p.closing = true
	p.a.await(1)

	if err := p.queue(&publishRequest{ctx: ctx, p: p, remove: true}); err != nil {
		p.end(ctx, End{Cause: EndClosed, Err: err})
	}
}
