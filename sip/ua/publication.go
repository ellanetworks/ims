package ua

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// PublicationHandler receives the events of a publication. Each function is optional; they run as the functions of
// a Handler do. No event follows Ended.
type PublicationHandler struct {
	// Published reports a 2xx to a PUBLISH: its SIP-ETag and Expires (RFC 3903 §5).
	Published func(p *Publication, res *sip.Response)

	// Ended reports the end of the publication:
	//   - EndRemoved by Remove, End.Response its 2xx;
	//   - EndRejected by a final response other than 2xx, End.Response telling which, once the publication was sent
	//     again as an initial one after a 412 (RFC 3903 §5);
	//   - EndExpired when its refreshes failed until it expired;
	//   - EndTimeout by the error of a PUBLISH;
	//   - EndClosed as the agent closes, after the PUBLISH removing it.
	Ended func(p *Publication, e End)
}

// Publication is event state the agent publishes, as an event publication agent (RFC 3903 §4), for one Request-URI
// and event package: its entity-tag, its expiry, and its state, which the agent sends again when the compositor lost
// it. The agent refreshes it before it expires. Its PUBLISHes go one at a time (§4), the changes made meanwhile sent
// after, the last state only. Its state belongs to its strand.
type Publication struct {
	a  *Agent
	st *Strand
	h  PublicationHandler

	// tmpl is the PUBLISH as built, before Prepare, without bodies; state the bodies of the state published,
	// attached those sent with each PUBLISH (WithBodies).
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

	// closing marks the removal of Shutdown, which Shutdown waits for.
	closing bool
}

// Publish publishes event state (RFC 3903 §4.2): req carries the Request-URI, To, Event, Expires (up to 2^32-1, as
// TS 24.379 §9.2.2.2.6), the Route and the header fields of the application, and the state in its bodies. The agent
// adds what req leaves out: the From tag, a Call-ID, CSeq and Max-Forwards. It answers 401 and 407 challenges
// through Config.Authenticate, and a 423 with its Min-Expires.
func (a *Agent) Publish(req *sip.Request, h PublicationHandler, opts ...Option) (*Publication, error) {
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

	if err := p.st.do(func() error { return p.send(&publishRequest{p: p, bodies: parts}) }); err != nil {
		a.removePublication(p)
		return nil, err
	}

	return p, nil
}

// Strand is the strand of the publication.
func (p *Publication) Strand() *Strand {
	return p.st
}

// ETag is the entity-tag of the publication, empty before its first 2xx.
func (p *Publication) ETag() string {
	return get(p.st, func() string { return p.etag })
}

// Expires is when the publication expires without a refresh, on the clock of the agent.
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

// Modify publishes the new state bodies (RFC 3903 §4.4); done receives the final response of its PUBLISH, or ErrStale
// when a later Modify replaced it before it went.
func (p *Publication) Modify(bodies []sip.BodyPart, done func(res *sip.Response, err error)) error {
	if len(bodies) == 0 {
		return errors.New("sip/ua: a modifying PUBLISH carries the state (RFC 3903 §4.4)")
	}

	return p.st.do(func() error {
		if p.ended {
			return &Error{Op: "Modify", State: Terminated, Err: ErrState}
		}

		p.state = bodies

		return p.queue(&publishRequest{p: p, bodies: bodies, done: done})
	})
}

// Remove removes the publication (RFC 3903 §4.5); it ends with the response, EndRemoved.
func (p *Publication) Remove(done func(res *sip.Response, err error)) error {
	return p.st.do(func() error {
		if p.ended {
			return &Error{Op: "Remove", State: Terminated, Err: ErrState}
		}

		return p.queue(&publishRequest{p: p, remove: true, done: done})
	})
}

// queue sends a PUBLISH, or keeps it for after the one in flight, in place of one kept already.
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

// publishRequest is a PUBLISH of the publication: initial, refreshing (no bodies), modifying or removing.
type publishRequest struct {
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

// build makes the PUBLISH with the next CSeq, the entity-tag of the publication unless initial (RFC 3903 §4.1).
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

	if _, err := p.a.prepareAndSend(c.req, p.st.client(c.response, c.error)); err != nil {
		return err
	}

	p.inflight = c

	return nil
}

func (c *publishRequest) response(res *sip.Response) {
	p := c.p

	if res.IsProvisional() || c.final || p.ended {
		return
	}

	if c.retry(res) {
		if err := p.send(c); err != nil {
			c.error(err)
		}

		return
	}

	c.final = true
	p.inflight = nil

	switch {
	case res.IsSuccess():
		p.succeeded(c, res)
	case res.StatusCode == 412 && !c.initial && !c.remove:
		// RFC 3903 §5: the compositor lost the state: the entity-tag goes, and the state is published anew.
		p.etag = ""
		p.emitDone(c.done, res, nil)

		if p.next == nil {
			p.next = &publishRequest{p: p}
		}
	case c.remove:
		p.emitDone(c.done, res, nil)
		p.end(End{Cause: EndRemoved, Response: res})

		return
	default:
		p.emitDone(c.done, res, nil)

		if c.initial || p.expiresAt.IsZero() || !p.a.clock.Now().Before(p.expiresAt) {
			p.end(End{Cause: EndRejected, Remote: true, Response: res})
			return
		}

		// The refresh failed: tried again halfway to the expiry.
		p.retryRefresh()
	}

	p.sendNext()
}

// retry prepares a PUBLISH to send again after a 401 or 407 (RFC 3261 §22.2) or a 423 (RFC 3903 §6 step 4).
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

func (c *publishRequest) error(err error) {
	p := c.p

	if c.final || p.ended {
		return
	}

	c.final = true
	p.inflight = nil

	p.emitDone(c.done, nil, err)

	if c.initial || c.remove {
		p.end(failureEnd(err))
		return
	}

	p.retryRefresh()
	p.sendNext()
}

// succeeded applies a 2xx: its entity-tag replaces the previous one, its Expires sets the expiry and the refresh
// (RFC 3903 §4.1, §5).
func (p *Publication) succeeded(c *publishRequest, res *sip.Response) {
	if etag, ok, err := res.Header.SIPETag(); ok && err == nil {
		p.etag = etag
	}

	p.emitDone(c.done, res, nil)

	if c.remove {
		p.end(End{Cause: EndRemoved, Response: res})
		return
	}

	p.st.emit(func() {
		if p.h.Published != nil {
			p.h.Published(p, res)
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
	p.refresh = p.st.after(d-refreshLead(d), p.autoRefresh)

	p.expiry.stop()
	p.expiry = p.st.after(d, func() { p.end(End{Cause: EndExpired}) })
}

// autoRefresh refreshes the publication, without a body (RFC 3903 §4.3), unless a PUBLISH is in flight or waiting.
func (p *Publication) autoRefresh() {
	if p.ended || p.inflight != nil || p.next != nil {
		return
	}

	if err := p.send(&publishRequest{p: p}); err != nil {
		p.retryRefresh()
	}
}

// retryRefresh tries a failed refresh again halfway to the expiry, while that leaves time for one.
func (p *Publication) retryRefresh() {
	left := p.expiresAt.Sub(p.a.clock.Now())
	if p.ended || left < 2*64*p.a.layer.T1() {
		return
	}

	p.refresh.stop()
	p.refresh = p.st.after(left/2, p.autoRefresh)
}

// sendNext sends the PUBLISH that waited for the one in flight.
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

// end ends the publication, once.
func (p *Publication) end(e End) {
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
			p.h.Ended(p, e)
		}
	})
}

// close removes the publication as the agent closes; Shutdown waits for the removal.
func (p *Publication) close() {
	if p.ended || p.closing {
		return
	}

	if p.etag == "" && p.inflight == nil {
		p.end(End{Cause: EndClosed})
		return
	}

	p.closing = true
	p.a.await(1)

	if err := p.queue(&publishRequest{p: p, remove: true}); err != nil {
		p.end(End{Cause: EndClosed, Err: err})
	}
}
