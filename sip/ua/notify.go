package ua

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

type Notification struct {
	Header sip.Header
	Bodies []sip.BodyPart

	Full bool
}

type Acceptance struct {
	Expires time.Duration

	Pending bool

	MinExpires time.Duration

	State func(seq uint32) (Notification, error)

	Resource Source

	Value any

	Handler ServerSubscriptionHandler

	Contact string
}

type ServerSubscriptionHandler struct {
	Refresh func(ctx context.Context, s *ServerSubscription, r *Request)

	Ended func(ctx context.Context, s *ServerSubscription, e End)
}

// RFC 6665 section 4.2
// RFC 3515 section 2.4.4
type ServerSubscription struct {
	a     *Agent
	st    *Strand
	h     ServerSubscriptionHandler
	event sip.Event
	value any

	d *dialog.Dialog

	shared bool

	contact string
	state   SubscriptionState
	stateOf func(seq uint32) (Notification, error)

	limit time.Duration

	expiresAt time.Time
	expiry    *timer

	queue    []*pendingNotify
	inflight *notifyRequest
	sent     uint32
	ending   *End
	ended    bool

	closing bool

	referStatus *sip.BodyPart

	minExpires time.Duration

	src        Source
	version    uint64
	nextChange time.Time
	held       *timer
	heldCauses []trace.SpanContext

	log *slog.Logger
}

const probationRetryAfter = 5 * time.Minute

type pendingNotify struct {
	ctx   context.Context
	build func(seq uint32) (Notification, error)
	full  bool

	terminate *sip.SubscriptionState
	end       End
}

func (s *ServerSubscription) Strand() *Strand {
	return s.st
}

func (s *ServerSubscription) Event() sip.Event {
	return s.event
}

func (s *ServerSubscription) State() SubscriptionState {
	return get(s.st, func() SubscriptionState { return s.state })
}

func (s *ServerSubscription) Expires() time.Time {
	return get(s.st, func() time.Time { return s.expiresAt })
}

func (s *ServerSubscription) Dialog() dialog.Snapshot {
	return get(s.st, func() dialog.Snapshot { return s.d.Snapshot() })
}

func (s *ServerSubscription) Value() any {
	return get(s.st, func() any { return s.value })
}

func (s *ServerSubscription) SetValue(v any) {
	_ = s.st.do(func() error {
		s.value = v
		return nil
	})
}

// RFC 6665 section 4.2.2
func (s *ServerSubscription) Notify(ctx context.Context, n Notification) error {
	return s.NotifyWith(ctx, n.Full, func(uint32) (Notification, error) { return n, nil })
}

func (s *ServerSubscription) NotifyWith(ctx context.Context, full bool, build func(seq uint32) (Notification, error)) error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil {
			return &Error{Op: "Notify", State: Terminated, Err: ErrState}
		}

		s.enqueue(&pendingNotify{ctx: ctx, build: build, full: full})

		return nil
	})
}

// RFC 6665 section 4.2.2
func (s *ServerSubscription) Activate(ctx context.Context) error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil || s.state != SubscriptionPending {
			return &Error{Op: "Activate", State: Terminated, Err: ErrState}
		}

		s.state = SubscriptionActive
		s.enqueue(&pendingNotify{ctx: ctx, build: s.stateOf, full: true})

		return nil
	})
}

// RFC 6665 section 4.1.3
func (s *ServerSubscription) Terminate(ctx context.Context, reason string, retryAfter time.Duration, n Notification) error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil {
			return &Error{Op: "Terminate", State: Terminated, Err: ErrState}
		}

		ss := sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: reason}

		if retryAfter > 0 {
			v := uint32(min(retryAfter/time.Second, math.MaxUint32))
			ss.RetryAfter = &v
		}

		s.terminate(ctx, ss, End{Cause: EndTerminated}, func(uint32) (Notification, error) { return n, nil })

		return nil
	})
}

func (s *ServerSubscription) terminate(ctx context.Context, ss sip.SubscriptionState, e End, build func(uint32) (Notification, error)) {
	if s.ended || s.ending != nil {
		return
	}

	s.expiry.stop()
	s.held.stop()
	s.held, s.heldCauses = nil, nil

	if build == nil {
		build = func(uint32) (Notification, error) { return Notification{}, nil }
	}

	s.ending = &e
	s.enqueue(&pendingNotify{ctx: ctx, build: build, full: true, terminate: &ss, end: e})
}

func (s *ServerSubscription) enqueue(p *pendingNotify) {
	if p.full {
		s.queue = s.queue[:0]
	}

	s.queue = append(s.queue, p)
	s.pump()
}

func (s *ServerSubscription) pump() {
	for s.inflight == nil && len(s.queue) > 0 && !s.ended {
		p := s.queue[0]
		s.queue = s.queue[1:]

		n, err := p.build(s.sent)
		if err != nil {
			s.logger().Warn("NOTIFY not built", slog.Bool("full", p.full), slog.Any("error", err))

			switch {
			case p.terminate != nil:
				n = Notification{}
			case !p.full && s.stateOf != nil:
				s.enqueue(&pendingNotify{ctx: p.ctx, build: s.stateOf, full: true})
				continue
			default:
				retry := uint32(probationRetryAfter / time.Second)
				s.terminate(p.ctx,
					sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: "probation", RetryAfter: &retry},
					End{Cause: EndFailed, Err: fmt.Errorf("sip/ua: NOTIFY not built: %w", err)}, nil)

				continue
			}
		}

		c := &notifyRequest{s: s, p: p, n: n}

		if err := c.send(p.ctx); err != nil {
			c.finish(p.ctx, nil, err)
		}
	}
}

// RFC 6665 section 4.2.2
func (s *ServerSubscription) subscriptionState(p *pendingNotify) sip.SubscriptionState {
	if p.terminate != nil {
		return *p.terminate
	}

	left := s.expiresAt.Sub(s.a.clock.Now())
	v := uint32(min(max((left+time.Second-1)/time.Second, 0), math.MaxUint32))

	return sip.SubscriptionState{State: s.state.String(), Expires: &v}
}

type notifyRequest struct {
	s     *ServerSubscription
	p     *pendingNotify
	n     Notification
	req   *sip.Request
	tries int
	final bool
}

func (c *notifyRequest) build() error {
	s := c.s

	req, err := s.d.NewRequest("NOTIFY")
	if err != nil {
		return err
	}

	req.Header.Set("Event", s.event.String())
	req.Header.Set("Subscription-State", s.subscriptionState(c.p).String())

	if s.contact != "" {
		req.Header.Set("Contact", s.contact)
	}

	Change{Header: c.n.Header, Bodies: c.n.Bodies}.apply(req)

	c.req = req

	return nil
}

func (c *notifyRequest) send(ctx context.Context) error {
	s := c.s

	if c.req == nil {
		if err := c.build(); err != nil {
			return err
		}
	}

	if _, err := s.a.prepareAndSend(ctx, c.req, s.st.client(c.response, func(ctx context.Context, err error) { c.finish(ctx, nil, err) })); err != nil {
		return err
	}

	s.inflight = c
	s.sent++

	return nil
}

func (c *notifyRequest) response(ctx context.Context, res *sip.Response) {
	s := c.s

	if res.IsProvisional() || c.final {
		return
	}

	_ = s.d.ReceiveResponse(res)

	if (res.StatusCode == 401 || res.StatusCode == 407) && s.inflight == c {
		next, err := s.d.NewRequest("NOTIFY")
		if err == nil {
			copyHeader(next, c.req)
			next.Body = c.req.Body

			if s.a.authenticate(next, res, c.tries) {
				c.tries++
				c.req = next
				s.inflight = nil

				if err := c.send(ctx); err != nil {
					c.finish(ctx, nil, err)
				}

				return
			}
		}
	}

	c.finish(ctx, res, nil)
}

// RFC 6665 section 4.2.2
// RFC 5057
func (c *notifyRequest) finish(ctx context.Context, res *sip.Response, err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true

	if s.inflight == c {
		s.inflight = nil
	}

	if c.p.terminate != nil {
		s.end(ctx, c.p.end)
		return
	}

	if res == nil || !res.IsSuccess() {
		switch failureOf("NOTIFY", res, err) {
		case destroysDialog:
			if s.shared {
				s.a.destroyDialog(ctx, s.d.ID(), res, err)
				return
			}

			s.end(ctx, End{Cause: EndLost, Response: res, Err: err})

			return
		case destroysUsage:
			s.end(ctx, End{Cause: EndLost, Response: res, Err: err})
			return
		}
	}

	s.pump()
}

func (s *ServerSubscription) end(ctx context.Context, e End) {
	s.st.check()

	if s.ended {
		return
	}

	s.ended = true
	s.state = SubscriptionTerminated
	s.queue = nil
	s.expiry.stop()
	s.held.stop()
	s.held, s.heldCauses = nil, nil

	if s.src != nil {
		s.src.detach(s)
	}

	if s.a.removeServerSubscription(s) && !s.shared {
		s.d.Terminate()
	}

	if s.closing {
		s.closing = false
		e.Cause = EndClosed

		s.a.await(-1)
	}

	s.st.emit(func() {
		if s.h.Ended != nil {
			s.h.Ended(ctx, s, e)
		}
	})
}

// RFC 6665 section 4.2.1.4
func (s *ServerSubscription) setExpiry(ctx context.Context, d time.Duration) {
	s.expiresAt = s.a.clock.Now().Add(d)
	s.expiry.stop()
	s.expiry = s.st.detached(ctx, d, "subscription expiry", func(ctx context.Context) {
		s.terminate(ctx, sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionTimeout},
			End{Cause: EndExpired}, s.stateOf)
	})
}

func (s *ServerSubscription) receive(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if s.ended || s.ending != nil {
		s.a.reply(tx, 481)
		return
	}

	if err := s.d.ReceiveRequest(req); err != nil {
		code := 400

		var serr *sip.StatusError
		if errors.As(err, &serr) {
			code = serr.StatusCode
		}

		s.a.reply(tx, code)

		return
	}

	r := newRequest(ctx, nil, tx, req)
	r.a, r.st, r.server = s.a, s.st, s

	if v, err := req.Header.Expires(); err == nil && v == 0 {
		_ = r.respondSubscribe(0)

		s.terminate(ctx, sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionTimeout},
			End{Cause: EndUnsubscribed, Remote: true, Request: req}, s.stateOf)

		return
	}

	if s.h.Refresh == nil {
		_ = r.acceptSubscription(Acceptance{Expires: s.limit, MinExpires: s.minExpires})
		return
	}

	s.st.emit(func() { s.h.Refresh(ctx, s, r) })
}

// RFC 6665 section 4.4.2
func (s *ServerSubscription) close(ctx context.Context) {
	if s.ended || s.closing {
		return
	}

	s.closing = true
	s.a.await(1)

	if s.ending == nil {
		s.terminate(ctx, sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionDeactivated},
			End{Cause: EndClosed}, s.stateOf)
	}
}

// RFC 6665 section 4.2.1.2
// RFC 6665 section 4.4.3
func (r *Request) AcceptSubscription(o Acceptance) (*ServerSubscription, error) {
	var s *ServerSubscription

	err := r.st.do(func() error {
		if r.server != nil {
			s = r.server
			return r.acceptSubscription(o)
		}

		var err error

		s, err = r.subscribe(o, sip.Event{}, false)

		return err
	})

	return s, err
}

func granted(req *sip.Request, limit time.Duration) (time.Duration, error) {
	v, err := req.Header.Expires()

	switch {
	case err == nil && (limit == 0 || time.Duration(v)*time.Second < limit):
		return time.Duration(v) * time.Second, nil
	case err == nil, limit > 0:
		return limit, nil
	}

	return 0, errors.New("sip/ua: a SUBSCRIBE without Expires needs Acceptance.Expires, the default of its package")
}

func (r *Request) acceptSubscription(o Acceptance) error {
	s := r.server

	if r.final || s.ended || s.ending != nil {
		return &Error{Op: "AcceptSubscription", State: Terminated, Err: ErrState}
	}

	if err := r.tooBrief(max(o.MinExpires, s.minExpires)); err != nil {
		return err
	}

	d, err := granted(r.req, o.Expires)
	if err != nil {
		return err
	}

	if err := r.respondSubscribe(d); err != nil {
		return err
	}

	s.setExpiry(r.ctx, d)
	s.enqueue(&pendingNotify{ctx: r.ctx, build: s.stateOf, full: true})

	return nil
}

// RFC 6665 section 4.2.1.1
// RFC 6665 section 8.3.1
func (r *Request) respondSubscribe(d time.Duration) error {
	s := r.server

	res, err := r.build(Reply{Code: 200})
	if err != nil {
		return err
	}

	res.Header.Set("Expires", strconvSeconds(d))

	if s.contact != "" {
		res.Header.Set("Contact", s.contact)
	}

	s.d.PrepareResponse(r.req, res)
	r.complete(res)
	r.final = true

	return r.tx.Respond(res)
}

// RFC 6665 section 4.2.1.1
func (r *Request) tooBrief(minimum time.Duration) error {
	v, err := r.req.Header.Expires()
	if minimum <= 0 || err != nil || v == 0 || time.Duration(v)*time.Second >= minimum {
		return nil
	}

	h := sip.Header{}
	h.Set("Min-Expires", strconvSeconds(minimum))

	if err := r.respond(Reply{Code: 423, Header: h}); err != nil {
		return err
	}

	return ErrIntervalTooBrief
}

func strconvSeconds(d time.Duration) string {
	return strconv.FormatUint(uint64(min(d/time.Second, math.MaxUint32)), 10)
}

// RFC 6665 section 4.2.1.1
func (r *Request) subscribe(o Acceptance, event sip.Event, refer bool) (*ServerSubscription, error) {
	a := r.a

	if r.final {
		return nil, transaction.ErrFinalResponseSent
	}

	if (o.State == nil) == (o.Resource == nil) {
		return nil, errors.New("sip/ua: an Acceptance needs the State of the resource, or the Resource")
	}

	if !refer {
		if err := r.tooBrief(o.MinExpires); err != nil {
			return nil, err
		}
	}

	if !refer {
		ev, ok, err := r.req.Header.Event()
		if !ok || err != nil {
			return nil, errors.New("sip/ua: a SUBSCRIBE without a valid Event")
		}

		event = ev
	}

	d := o.Expires

	if !refer {
		var err error
		if d, err = granted(r.req, o.Expires); err != nil {
			return nil, err
		}
	}

	contact := o.Contact
	if contact == "" && a.cfg.Contact.URI.Scheme != "" {
		contact = a.cfg.Contact.String()
	}

	s := &ServerSubscription{
		a: a, st: r.st, h: o.Handler, event: event, contact: contact, stateOf: o.State, state: SubscriptionActive,
		limit: o.Expires, minExpires: o.MinExpires, value: o.Value, src: o.Resource,
	}

	if s.src != nil {
		s.stateOf = func(seq uint32) (Notification, error) { return s.src.render(s, seq) }
	}

	if o.Pending {
		s.state = SubscriptionPending
	}

	res, err := r.build(Reply{Code: 200})
	if err != nil {
		return nil, err
	}

	if !refer {
		res.Header.Set("Expires", strconvSeconds(d))
	}

	if contact != "" {
		res.Header.Set("Contact", contact)
	}

	dlg, err := dialog.NewUAS(r.req, res)
	if err != nil {
		return nil, err
	}

	s.d = dlg
	r.server = s

	r.complete(res)
	r.final = true

	if err := r.tx.Respond(res); err != nil {
		return nil, err
	}

	a.addServerSubscription(s, trace.SpanContextFromContext(r.ctx))

	if s.src != nil {
		if err := s.src.bind(s); err != nil {
			s.src = nil
			s.terminate(r.ctx, sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionNoResource},
				End{Cause: EndTerminated}, nil)

			return s, nil
		}
	}

	if d == 0 && !refer {
		s.expiresAt = a.clock.Now()
		s.terminate(r.ctx, sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionTimeout},
			End{Cause: EndExpired}, s.stateOf)

		return s, nil
	}

	s.setExpiry(r.ctx, d)
	s.enqueue(&pendingNotify{ctx: r.ctx, build: s.stateOf, full: true})

	return s, nil
}
