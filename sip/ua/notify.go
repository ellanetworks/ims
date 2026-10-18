package ua

import (
	"errors"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
)

// Notification is the content of a NOTIFY: header fields beyond those of the subscription, and the bodies of the
// state of the resource.
type Notification struct {
	Header sip.Header
	Bodies []sip.BodyPart

	// Full marks the full state of the resource: the NOTIFYs queued and not sent yet, which it makes obsolete, are
	// dropped for it. Partial notifications, as the deltas of conference-info (RFC 4575 §4.2), queue in order.
	Full bool
}

// Acceptance is how the application accepts a SUBSCRIBE, with Request.AcceptSubscription.
type Acceptance struct {
	// Expires is the duration granted: at most the one asked for (RFC 6665 §4.2.1.1), which zero grants. A SUBSCRIBE
	// without Expires is granted Expires, the default of its event package (RFC 6665 §7.2), which it then needs.
	// Durations up to 2^32-1 s are kept, as those of TS 24.379 §10.1.3.2.
	Expires time.Duration

	// Pending accepts the subscription in the pending state, its authorization still to come (RFC 6665 §4.2.1.3):
	// ServerSubscription.Activate or Terminate settles it.
	Pending bool

	// MinExpires is the shortest duration the notifier grants: a SUBSCRIBE asking for less, but not zero, gets a 423
	// with it as Min-Expires (RFC 6665 §4.2.1.1, TS 24.379 §9.2.2.3.4), and so do the refreshes of the subscription.
	MinExpires time.Duration

	// State is the full state of the resource, for the NOTIFYs the agent sends on its own: the first one, the one
	// after each refresh (RFC 6665 §4.2.1.2), the one of Activate, and the terminated one of an unsubscribe or an
	// expiry (§4.2.1.4). seq is the number of NOTIFYs sent before it in the subscription, for the version of the
	// packages that count them (RFC 3680 §5.2, RFC 4575 §5.1). It runs with the strand held: it must not call the
	// agent.
	State func(seq uint32) (Notification, error)

	// Resource, in place of State, binds the subscription to a resource, whose changes it notifies as they come, and
	// whose state it renders for those NOTIFYs.
	Resource Source

	// Value is the value of the subscription, given to the renderings of its Resource: its filter (RFC 4661) or its
	// subscriber.
	Value any

	Handler ServerSubscriptionHandler

	// Contact is the Contact of the agent in the dialog, a GRUU (RFC 6665 §4.5.1); Config.Contact without it.
	Contact string
}

// ServerSubscriptionHandler receives the events of a subscription of the agent as notifier. Each function is
// optional; they run as the functions of a Handler do. No event follows Ended.
type ServerSubscriptionHandler struct {
	// Refresh receives a SUBSCRIBE refreshing the subscription, for the application to accept with
	// Request.AcceptSubscription (only Acceptance.Expires counts) or to reject. Without it, the agent grants the
	// duration asked for, at most the Acceptance.Expires of the subscription. An unsubscribe (Expires 0) is not
	// offered: the agent accepts it and ends the subscription with a terminated NOTIFY (RFC 6665 §4.2.1.4).
	Refresh func(s *ServerSubscription, r *Request)

	// Ended reports the end of the subscription:
	//   - EndTerminated by Terminate, once its NOTIFY transaction ended;
	//   - EndUnsubscribed by an unsubscribe of the subscriber, EndExpired by its expiry, each after their terminated
	//     NOTIFY;
	//   - EndLost by a failure of a NOTIFY that ends the subscription (RFC 6665 §4.2.2, RFC 5057), End.Response or
	//     End.Err telling which;
	//   - EndClosed as the agent closes, after its NOTIFY terminated with reason deactivated (RFC 6665 §4.4.2).
	Ended func(s *ServerSubscription, e End)
}

// ServerSubscription is a subscription of the agent as notifier (RFC 6665 §4.2): created by a SUBSCRIBE the
// application accepted, or by a REFER (RFC 3515 §2.4.4), and the dialog usage it makes. Its NOTIFYs go one at a
// time, in order, each with the Subscription-State of when it is sent. Its state belongs to its strand.
type ServerSubscription struct {
	a     *Agent
	st    *Strand
	h     ServerSubscriptionHandler
	event sip.Event
	value any

	d *dialog.Dialog

	// shared marks a subscription in the dialog of an INVITE session, as an implicit one of a REFER within it.
	shared bool

	contact string
	state   SubscriptionState
	stateOf func(seq uint32) (Notification, error)

	// limit is the longest duration the application grants, Acceptance.Expires, for the refreshes it leaves to
	// the agent.
	limit time.Duration

	expiresAt time.Time
	expiry    *timer

	// queue are the NOTIFYs to send after the one in flight; sent counts those sent; ending is the end that follows
	// the terminated NOTIFY queued or in flight.
	queue    []*pendingNotify
	inflight *notifyRequest
	sent     uint32
	ending   *End
	ended    bool

	// closing marks the NOTIFY of Close, which Close waits for.
	closing bool

	// referStatus is the last status of the request of a REFER (ReferStatus).
	referStatus *sip.BodyPart

	// minExpires is Acceptance.MinExpires, for the refreshes.
	minExpires time.Duration

	// src is the resource of the subscription, and version the version of its state the last NOTIFY rendered;
	// nextChange is when a change may be notified, their rate limited, and held the timer of the changes waiting for
	// it.
	src        Source
	version    uint64
	nextChange time.Time
	held       *timer
}

type pendingNotify struct {
	build func(seq uint32) (Notification, error)
	full  bool

	// terminate is the terminated Subscription-State of the last NOTIFY, and end the end it brings.
	terminate *sip.SubscriptionState
	end       End
}

// Strand is the strand of the subscription.
func (s *ServerSubscription) Strand() *Strand {
	return s.st
}

// Event is the Event of the subscription, with its id.
func (s *ServerSubscription) Event() sip.Event {
	return s.event
}

func (s *ServerSubscription) State() SubscriptionState {
	return get(s.st, func() SubscriptionState { return s.state })
}

// Expires is when the subscription expires without a refresh, on the clock of the agent.
func (s *ServerSubscription) Expires() time.Time {
	return get(s.st, func() time.Time { return s.expiresAt })
}

// Dialog is the state of the dialog of the subscription.
func (s *ServerSubscription) Dialog() dialog.Snapshot {
	return get(s.st, func() dialog.Snapshot { return s.d.Snapshot() })
}

// Value is the value the application attached to the subscription.
func (s *ServerSubscription) Value() any {
	return get(s.st, func() any { return s.value })
}

func (s *ServerSubscription) SetValue(v any) {
	_ = s.st.do(func() error {
		s.value = v
		return nil
	})
}

// Notify sends a NOTIFY of n, the state of the resource having changed (RFC 6665 §4.2.2), after those queued.
func (s *ServerSubscription) Notify(n Notification) error {
	return s.NotifyWith(n.Full, func(uint32) (Notification, error) { return n, nil })
}

// NotifyWith sends a NOTIFY whose content build makes as it is sent, after those queued, with the number of NOTIFYs
// sent before it, for the versions of the packages that count them. full marks the full state of the resource.
func (s *ServerSubscription) NotifyWith(full bool, build func(seq uint32) (Notification, error)) error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil {
			return &Error{Op: "Notify", State: Terminated, Err: ErrState}
		}

		s.enqueue(&pendingNotify{build: build, full: full})

		return nil
	})
}

// Activate moves a pending subscription to active, its subscriber authorized, with a NOTIFY of the full state
// (RFC 6665 §4.2.2).
func (s *ServerSubscription) Activate() error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil || s.state != SubscriptionPending {
			return &Error{Op: "Activate", State: Terminated, Err: ErrState}
		}

		s.state = SubscriptionActive
		s.enqueue(&pendingNotify{build: s.stateOf, full: true})

		return nil
	})
}

// Terminate ends the subscription with a NOTIFY of n terminated with reason (RFC 6665 §4.1.3: rejected for an
// authorization declined, noresource for a resource gone, deactivated for a migration), and retryAfter when not
// zero. The subscription ends once the NOTIFY transaction ends.
func (s *ServerSubscription) Terminate(reason string, retryAfter time.Duration, n Notification) error {
	return s.st.do(func() error {
		if s.ended || s.ending != nil {
			return &Error{Op: "Terminate", State: Terminated, Err: ErrState}
		}

		ss := sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: reason}

		if retryAfter > 0 {
			v := uint32(min(retryAfter/time.Second, math.MaxUint32))
			ss.RetryAfter = &v
		}

		s.terminate(ss, End{Cause: EndTerminated}, func(uint32) (Notification, error) { return n, nil })

		return nil
	})
}

// terminate queues the terminated NOTIFY, in place of the NOTIFYs it makes obsolete, and the end it brings.
func (s *ServerSubscription) terminate(ss sip.SubscriptionState, e End, build func(uint32) (Notification, error)) {
	if s.ended || s.ending != nil {
		return
	}

	s.expiry.stop()
	s.held.stop()
	s.held = nil

	if build == nil {
		build = func(uint32) (Notification, error) { return Notification{}, nil }
	}

	s.ending = &e
	s.enqueue(&pendingNotify{build: build, full: true, terminate: &ss, end: e})
}

func (s *ServerSubscription) enqueue(p *pendingNotify) {
	if p.full {
		s.queue = s.queue[:0]
	}

	s.queue = append(s.queue, p)
	s.pump()
}

// pump sends the next NOTIFY once the one in flight completed.
func (s *ServerSubscription) pump() {
	for s.inflight == nil && len(s.queue) > 0 && !s.ended {
		p := s.queue[0]
		s.queue = s.queue[1:]

		n, err := p.build(s.sent)
		if err != nil {
			s.a.log.Debug("NOTIFY not built", slog.Any("error", err))

			if p.terminate != nil {
				n = Notification{}
			} else {
				continue
			}
		}

		c := &notifyRequest{s: s, p: p, n: n}

		if err := c.send(); err != nil {
			c.finish(nil, err)
		}
	}
}

// subscriptionState is the Subscription-State of a NOTIFY sent now: active or pending with the time left
// (RFC 6665 §4.2.2), or the terminated one of p.
func (s *ServerSubscription) subscriptionState(p *pendingNotify) sip.SubscriptionState {
	if p.terminate != nil {
		return *p.terminate
	}

	left := s.expiresAt.Sub(s.a.clock.Now())
	v := uint32(min(max((left+time.Second-1)/time.Second, 0), math.MaxUint32))

	return sip.SubscriptionState{State: s.state.String(), Expires: &v}
}

// notifyRequest is a NOTIFY of the subscription, sent again after a challenge.
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

	// RFC 6665 §4.4.1: NOTIFY refreshes the target of the dialog.
	if s.contact != "" {
		req.Header.Set("Contact", s.contact)
	}

	Change{Header: c.n.Header, Bodies: c.n.Bodies}.apply(req)

	c.req = req

	return nil
}

func (c *notifyRequest) send() error {
	s := c.s

	if c.req == nil {
		if err := c.build(); err != nil {
			return err
		}
	}

	if _, err := s.a.prepareAndSend(c.req, s.st.client(c.response, func(err error) { c.finish(nil, err) })); err != nil {
		return err
	}

	s.inflight = c
	s.sent++

	return nil
}

func (c *notifyRequest) response(res *sip.Response) {
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

				if err := c.send(); err != nil {
					c.finish(nil, err)
				}

				return
			}
		}
	}

	c.finish(res, nil)
}

// finish ends the NOTIFY transaction: a terminated NOTIFY ends the subscription, however its transaction ends; a
// failure that ends the subscription for its subscriber ends it here (RFC 6665 §4.2.2, RFC 5057); then the next one
// goes.
func (c *notifyRequest) finish(res *sip.Response, err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true

	if s.inflight == c {
		s.inflight = nil
	}

	if c.p.terminate != nil {
		s.end(c.p.end)
		return
	}

	if res == nil || !res.IsSuccess() {
		switch failureOf("NOTIFY", res, err) {
		case destroysDialog:
			if s.shared {
				s.a.destroyDialog(s.d.ID(), res, err)
				return
			}

			s.end(End{Cause: EndLost, Response: res, Err: err})

			return
		case destroysUsage:
			s.end(End{Cause: EndLost, Response: res, Err: err})
			return
		}
	}

	s.pump()
}

// end terminates the subscription, once: its usage of the dialog ends, with the dialog if it was the last one.
func (s *ServerSubscription) end(e End) {
	s.st.check()

	if s.ended {
		return
	}

	s.ended = true
	s.state = SubscriptionTerminated
	s.queue = nil
	s.expiry.stop()
	s.held.stop()
	s.held = nil

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
			s.h.Ended(s, e)
		}
	})
}

// setExpiry sets when the subscription expires: then it ends with a terminated NOTIFY, reason timeout (RFC 6665
// §4.2.1.4).
func (s *ServerSubscription) setExpiry(d time.Duration) {
	s.expiresAt = s.a.clock.Now().Add(d)
	s.expiry.stop()
	s.expiry = s.st.after(d, func() {
		s.terminate(sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionTimeout},
			End{Cause: EndExpired}, s.stateOf)
	})
}

// receive handles a SUBSCRIBE within the dialog for the subscription: a refresh, or an unsubscribe.
func (s *ServerSubscription) receive(tx *transaction.ServerTransaction, req *sip.Request) {
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

	r := newRequest(nil, tx, req)
	r.a, r.st, r.server = s.a, s.st, s

	if v, err := req.Header.Expires(); err == nil && v == 0 {
		_ = r.respondSubscribe(0)

		s.terminate(sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionTimeout},
			End{Cause: EndUnsubscribed, Remote: true, Request: req}, s.stateOf)

		return
	}

	if s.h.Refresh == nil {
		_ = r.acceptSubscription(Acceptance{Expires: s.limit, MinExpires: s.minExpires})
		return
	}

	s.st.emit(func() { s.h.Refresh(s, r) })
}

// close ends the subscription as the agent closes, with a NOTIFY terminated with reason deactivated, for the
// subscriber to subscribe again elsewhere (RFC 6665 §4.4.2); Close waits for it.
func (s *ServerSubscription) close() {
	if s.ended || s.closing {
		return
	}

	s.closing = true
	s.a.await(1)

	if s.ending == nil {
		s.terminate(sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionDeactivated},
			End{Cause: EndClosed}, s.stateOf)
	}
}

// AcceptSubscription accepts a SUBSCRIBE: an initial one, outside a dialog, creates the subscription and its dialog;
// a refresh of Handler.Refresh renews it. The agent sends the 200 with the duration granted, then at once the NOTIFY
// of the state (RFC 6665 §4.2.1.2). An initial SUBSCRIBE with Expires 0, a fetch (RFC 6665 §4.4.3), is answered with
// that NOTIFY terminated, reason timeout.
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

// granted is the duration a SUBSCRIBE asked for, cut to limit when not zero; without one, limit.
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

// acceptSubscription accepts a refresh.
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

	s.setExpiry(d)
	s.enqueue(&pendingNotify{build: s.stateOf, full: true})

	return nil
}

// respondSubscribe sends the 200 to a SUBSCRIBE of a subscription, with its duration (RFC 6665 §4.2.1.1; never a 202,
// §8.3.1).
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

// tooBrief answers a SUBSCRIBE asking for less than minimum, but not zero, with a 423 (RFC 6665 §4.2.1.1).
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

// subscribe creates the subscription of a SUBSCRIBE or of a REFER outside a dialog (RFC 6665 §4.2.1.1): its dialog
// from the 200 the agent sends, and its first NOTIFY. event is the Event of a REFER, which has none of its own.
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

	a.addServerSubscription(s)

	// Bound once it exists, its first NOTIFY rendering the state after the changes before; a resource terminated
	// meanwhile ends it at once (RFC 6665 §4.1.3).
	if s.src != nil {
		if err := s.src.bind(s); err != nil {
			s.src = nil
			s.terminate(sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionNoResource},
				End{Cause: EndTerminated}, nil)

			return s, nil
		}
	}

	// RFC 6665 §4.4.3: a fetch ends with its NOTIFY.
	if d == 0 && !refer {
		s.expiresAt = a.clock.Now()
		s.terminate(sip.SubscriptionState{State: sip.SubscriptionTerminated, Reason: sip.SubscriptionTimeout},
			End{Cause: EndExpired}, s.stateOf)

		return s, nil
	}

	s.setExpiry(d)
	s.enqueue(&pendingNotify{build: s.stateOf, full: true})

	return s, nil
}
