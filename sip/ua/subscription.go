package ua

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
)

// SubscriptionState is the state of a subscription of the agent, as its NOTIFYs tell it (RFC 6665 §4.1.3).
type SubscriptionState int

const (
	// SubscriptionNeutral: no NOTIFY yet (RFC 6665 §4.1.2.4).
	SubscriptionNeutral SubscriptionState = iota
	SubscriptionPending
	SubscriptionActive
	SubscriptionTerminated
)

func (s SubscriptionState) String() string {
	switch s {
	case SubscriptionNeutral:
		return "neutral"
	case SubscriptionPending:
		return sip.SubscriptionPending
	case SubscriptionActive:
		return sip.SubscriptionActive
	case SubscriptionTerminated:
		return sip.SubscriptionTerminated
	}

	return "SubscriptionState(?)"
}

// SubscriptionHandler receives the events of a subscription. Each function is optional; they run as the functions of
// a Handler do.
type SubscriptionHandler struct {
	// Accepted reports the 2xx to the initial SUBSCRIBE.
	Accepted func(s *Subscription, res *sip.Response)

	// Notify reports a NOTIFY r of the subscription, once the agent applied its Subscription-State; the agent
	// answers it with a 200 once Notify returns: a subscriber does not wait for the user (RFC 6665 §4.1.3). r.Done
	// tells when its transaction ends.
	Notify func(s *Subscription, r *Request)

	// Forked decides on a NOTIFY from another notifier than the first one, s the subscription it creates
	// (RFC 6665 §4.1.4): true keeps s, a subscription of its own with this handler; false, or no Forked, answers the
	// NOTIFY with a 481.
	Forked func(s *Subscription, notify *sip.Request) bool

	// Ended reports the end of the subscription: EndTerminated by a NOTIFY, End.State telling whether to subscribe
	// again (sip.SubscriptionState.Resubscribe); EndRejected by a final response other than 2xx to the initial
	// SUBSCRIBE, or by one to a refresh that ends the subscription (RFC 6665 §4.1.2.2); EndNoNotify without a NOTIFY
	// within Timer N; EndExpired without a refresh; EndTimeout by the error of a SUBSCRIBE transaction;
	// EndUnsubscribed by Unsubscribe, without the terminated NOTIFY; EndClosed.
	Ended func(s *Subscription, e End)
}

// Subscription is a subscription of the agent, as subscriber (RFC 6665 §4.1), and the dialog usage its first NOTIFY
// creates (RFC 6665 §4.4.1). The agent refreshes it before it expires. Its state belongs to the loop of the agent.
type Subscription struct {
	a     *Agent
	sh    SubscriptionHandler
	att   *subscribing
	event sip.Event

	d     *dialog.Dialog
	state SubscriptionState
	value any

	// duration is the last known duration of the subscription, from the 2xx to a SUBSCRIBE or the expires of a
	// NOTIFY; left what remains of it once the refresh is due; refresh and expiry their timers; timer the wait for the
	// NOTIFY of a SUBSCRIBE, Timer N (RFC 6665 §4.1.2.2).
	duration, left         time.Duration
	refresh, expiry, timer *timer

	// unsubscribing marks a SUBSCRIBE with Expires 0 sent: the subscription waits for its terminated NOTIFY.
	unsubscribing bool
	ended         bool

	// inflight are its SUBSCRIBEs in progress, completed with ErrState should the subscription end first.
	inflight map[*subscribeRequest]struct{}
}

type subscribeKey struct {
	callID, fromTag string
}

// subscribing is an initial SUBSCRIBE of the agent: until Timer N fires, its NOTIFYs create subscriptions, the first
// one the Subscription of Agent.Subscribe, and of other notifiers forks (RFC 6665 §4.1.2.4, §4.1.4).
type subscribing struct {
	a     *Agent
	key   subscribeKey
	first *Subscription

	// tmpl is the SUBSCRIBE before Prepare, sent again after a challenge; sent the last one sent, which the dialogs
	// of its NOTIFYs follow (RFC 6665 §4.4.1).
	tmpl  *sip.Request
	sent  *sip.Request
	tries int
	final bool

	// subs are the subscriptions of the NOTIFYs, by the tag of their notifier; okTag and okExpires the notifier and
	// the Expires of the 2xx, for its subscription.
	subs      map[string]*Subscription
	okTag     string
	okExpires *uint32

	timerN *timer
}

// Subscribe sends an initial SUBSCRIBE (RFC 6665 §4.1.2.1), and returns its subscription, in the neutral state until
// its first NOTIFY. req carries the Request-URI, To, Event, Accept, Expires, the Route of an outbound proxy or a
// service route, and the header fields of the application; the agent adds what it leaves out: the From tag, a
// Call-ID, CSeq, Max-Forwards and Contact. It answers 401 and 407 challenges through Config.Authenticate.
func (a *Agent) Subscribe(req *sip.Request, h SubscriptionHandler) (*Subscription, error) {
	if req.Method != "SUBSCRIBE" {
		return nil, fmt.Errorf("sip/ua: Subscribe of a %s request", req.Method)
	}

	event, ok, err := req.Header.Event()
	if !ok || err != nil {
		return nil, fmt.Errorf("sip/ua: SUBSCRIBE without a valid Event: %v", err)
	}

	tmpl := req.Clone()
	if err := a.completeInitial(tmpl); err != nil {
		return nil, err
	}

	if !tmpl.Header.Has("Contact") {
		if a.cfg.Contact.URI.Scheme == "" {
			return nil, errors.New("sip/ua: no Contact")
		}

		tmpl.Header.Set("Contact", a.cfg.Contact.String())
	}

	from, _ := tmpl.Header.From()

	att := &subscribing{
		a: a, key: subscribeKey{callID: tmpl.Header.CallID(), fromTag: from.Tag()},
		tmpl: tmpl, subs: map[string]*Subscription{},
	}
	s := &Subscription{a: a, sh: h, att: att, event: event}
	att.first = s

	err = a.loop.call(func() error {
		if a.closed {
			return ErrClosed
		}

		if err := att.send(); err != nil {
			return err
		}

		a.subscribing[att.key] = att

		return nil
	})
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (att *subscribing) send() error {
	a := att.a
	req := att.tmpl.Clone()

	if _, err := a.prepareAndSend(req, a.client(att.response, att.error)); err != nil {
		return err
	}

	att.sent = req
	att.timerN.stop()
	att.timerN = a.after(64*a.layer.T1(), att.noNotify)

	return nil
}

// response handles a response to the initial SUBSCRIBE.
func (att *subscribing) response(res *sip.Response) {
	a := att.a

	if res.IsProvisional() || att.final {
		return
	}

	if !res.IsSuccess() {
		next := att.tmpl.Clone()
		nextCSeq(next)

		if a.authenticate(next, res, att.tries) {
			att.tries++
			att.tmpl = next

			if err := att.send(); err != nil {
				att.error(err)
			}

			return
		}
	}

	att.final = true

	if !res.IsSuccess() {
		att.done()

		// A NOTIFY that came first made the subscription; the 2xx is the notifier's word for it.
		if s := att.first; s.d == nil {
			s.end(End{Cause: EndRejected, Remote: true, Response: res})
		}

		return
	}

	to, _ := res.Header.To()
	att.okTag = to.Tag()

	if v, err := res.Header.Expires(); err == nil {
		att.okExpires = &v
	}

	switch s := att.subs[att.okTag]; {
	case att.okExpires == nil:
	case s != nil:
		s.setDuration(*att.okExpires)
	case att.first.d == nil:
		// Its timers start with its dialog.
		att.first.duration = time.Duration(*att.okExpires) * time.Second
	}

	first := att.first
	first.emit(func(h SubscriptionHandler) {
		if h.Accepted != nil {
			h.Accepted(first, res)
		}
	})
}

func (att *subscribing) error(err error) {
	if att.final {
		return
	}

	att.final = true
	att.done()

	if s := att.first; s.d == nil {
		s.end(End{Cause: EndTimeout, Err: err})
	}
}

// noNotify ends the wait for the NOTIFYs of the initial SUBSCRIBE: the subscription fails without one; the NOTIFYs of
// new notifiers get a 481 from now on (RFC 6665 §4.1.2.4).
func (att *subscribing) noNotify() {
	att.done()

	if s := att.first; s.d == nil {
		s.end(End{Cause: EndNoNotify})
	}
}

func (att *subscribing) done() {
	att.timerN.stop()

	if att.a.subscribing[att.key] == att {
		delete(att.a.subscribing, att.key)
	}
}

// notifyFor receives a NOTIFY outside the dialogs of the agent: the first one of a subscription of the SUBSCRIBE, or of
// a fork (RFC 6665 §4.4.1).
func (att *subscribing) notifyFor(tx *transaction.ServerTransaction, req *sip.Request) {
	a := att.a
	first := att.first

	ss, code := checkNotify(req, first.event)
	if code != 0 {
		a.reply(tx, code)
		return
	}

	if ss.State == sip.SubscriptionTerminated {
		// RFC 6665 §4.4.1: a terminated NOTIFY creates no dialog usage; the first subscription ends with it.
		if first.d != nil {
			a.reply(tx, 200)
			return
		}

		att.done()

		first.state = SubscriptionTerminated
		first.notified(tx, req, sip.NewResponse(req, 200, ""))
		first.end(End{Cause: EndTerminated, Remote: true, Request: req, State: ss})

		return
	}

	d, err := dialog.NewFromNotify(att.sent, req)
	if err != nil {
		a.reply(tx, 400)
		return
	}

	if first.d == nil {
		first.bind(d)
		first.receiveNotify(tx, req, ss)

		return
	}

	fork := &Subscription{a: a, sh: first.sh, att: att, event: first.event}
	fork.bind(d)

	a.loop.emit(func() {
		if fork.sh.Forked == nil || !fork.sh.Forked(fork, req) {
			fork.unbind()
			a.reply(tx, 481)

			return
		}

		fork.receiveNotify(tx, req, ss)
	})
}

// checkNotify checks a NOTIFY for a subscription to event: the Event matching it (RFC 6665 §8.2.1), with 489
// otherwise, and a Subscription-State, with 400 otherwise.
func checkNotify(req *sip.Request, event sip.Event) (sip.SubscriptionState, int) {
	ev, ok, err := req.Header.Event()
	if !ok || err != nil || !ev.Matches(event) {
		return sip.SubscriptionState{}, 489
	}

	ss, ok, err := req.Header.SubscriptionState()
	if !ok || err != nil {
		return sip.SubscriptionState{}, 400
	}

	return ss, 0
}

// bind gives the subscription the dialog its first NOTIFY creates.
func (s *Subscription) bind(d *dialog.Dialog) {
	s.d = d

	tag := d.ID().RemoteTag
	s.att.subs[tag] = s
	s.a.addSubscription(s)

	if tag == s.att.okTag && s.att.okExpires != nil {
		s.setDuration(*s.att.okExpires)
	}
}

func (s *Subscription) unbind() {
	delete(s.att.subs, s.d.ID().RemoteTag)
	s.a.removeSubscription(s)
	s.d.Terminate()
}

// receive handles a request of the notifier within the dialog of the subscription.
func (s *Subscription) receive(tx *transaction.ServerTransaction, req *sip.Request) {
	if req.Method != "NOTIFY" {
		s.a.reply(tx, 405, sip.Field{Name: "Allow", Value: "NOTIFY"})
		return
	}

	ss, code := checkNotify(req, s.event)
	if code != 0 {
		s.a.reply(tx, code)
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

	s.receiveNotify(tx, req, ss)
}

// receiveNotify applies the Subscription-State of a NOTIFY (RFC 6665 §4.1.3), reports it, and answers it.
func (s *Subscription) receiveNotify(tx *transaction.ServerTransaction, req *sip.Request, ss sip.SubscriptionState) {
	res := sip.NewResponse(req, 200, "")
	s.d.PrepareResponse(req, res)

	s.timer.stop()

	switch ss.State {
	case sip.SubscriptionTerminated:
		s.state = SubscriptionTerminated
		s.notified(tx, req, res)
		s.end(End{Cause: EndTerminated, Remote: true, Request: req, State: ss})

		return
	case sip.SubscriptionPending:
		s.state = SubscriptionPending
	default:
		s.state = SubscriptionActive
	}

	if ss.Expires != nil && !s.unsubscribing {
		s.setDuration(*ss.Expires)
	}

	s.notified(tx, req, res)
}

// notified reports a NOTIFY to the application, then answers it with res.
func (s *Subscription) notified(tx *transaction.ServerTransaction, req *sip.Request, res *sip.Response) {
	r := newRequest(nil, tx, req)
	r.a, r.final = s.a, true

	s.emit(func(h SubscriptionHandler) {
		if h.Notify != nil {
			h.Notify(s, r)
		}

		if err := s.a.respond(tx, res); err != nil {
			s.a.log.Debug("NOTIFY response failed", slog.Any("error", err))
		}
	})
}

func (s *Subscription) emit(f func(h SubscriptionHandler)) {
	s.a.loop.emit(func() { f(s.sh) })
}

// setDuration sets the duration of the subscription, from now: the agent refreshes it at half, or 600 s before its
// end when it lasts more than 1200 s, and it ends at its end without a refresh.
func (s *Subscription) setDuration(seconds uint32) {
	if s.state == SubscriptionTerminated {
		return
	}

	s.duration = time.Duration(seconds) * time.Second
	s.refresh.stop()
	s.expiry.stop()
	s.refresh, s.expiry = nil, nil

	if s.duration == 0 {
		return
	}

	s.expiry = s.a.after(s.duration, func() { s.end(End{Cause: EndExpired}) })

	if s.d != nil && !s.unsubscribing {
		at := max(s.duration/2, s.duration-600*time.Second)
		s.left = s.duration - at
		s.refresh = s.a.after(at, s.autoRefresh)
	}
}

func (s *Subscription) autoRefresh() {
	if err := s.subscribe(false, nil); err != nil {
		s.a.log.Debug("subscription refresh failed", slog.Any("error", err))
		s.retryRefresh()
	}
}

// end terminates the subscription, once: its timers stop, and its dialog usage ends (RFC 5057 §5.4).
func (s *Subscription) end(e End) {
	s.a.loop.check()

	if s.ended {
		return
	}

	s.ended = true
	s.state = SubscriptionTerminated
	s.refresh.stop()
	s.expiry.stop()
	s.timer.stop()

	for c := range s.inflight {
		c.final = true
		s.emitDone(c.done, nil, ErrState)
	}

	s.inflight = nil

	if s.d != nil {
		s.a.removeSubscription(s)
		s.d.Terminate()
	}

	if s.att.first == s && s.d == nil || e.Cause == EndUnsubscribed {
		s.att.done()
	}

	s.emit(func(h SubscriptionHandler) {
		if h.Ended != nil {
			h.Ended(s, e)
		}
	})
}

// Event is the Event of the subscription.
func (s *Subscription) Event() sip.Event {
	return s.event
}

func (s *Subscription) State() SubscriptionState {
	return get(s.a.loop, func() SubscriptionState { return s.state })
}

// Duration is the last known duration of the subscription: of the 2xx to its last SUBSCRIBE, or of its last NOTIFY.
func (s *Subscription) Duration() time.Duration {
	return get(s.a.loop, func() time.Duration { return s.duration })
}

// Dialog is the state of the dialog of the subscription, once its first NOTIFY created it.
func (s *Subscription) Dialog() (dialog.Snapshot, bool) {
	var (
		snap dialog.Snapshot
		ok   bool
	)

	_ = s.a.loop.call(func() error {
		if s.d != nil {
			snap, ok = s.d.Snapshot(), true
		}

		return nil
	})

	return snap, ok
}

// Value is the value the application attached to the subscription.
func (s *Subscription) Value() any {
	return get(s.a.loop, func() any { return s.value })
}

func (s *Subscription) SetValue(v any) {
	_ = s.a.loop.call(func() error {
		s.value = v
		return nil
	})
}

// Refresh refreshes the subscription now, with a SUBSCRIBE within its dialog (RFC 6665 §4.1.2.2); done receives its
// final response or its error, as a handler. The agent refreshes on its own before the subscription expires.
func (s *Subscription) Refresh(done func(res *sip.Response, err error)) error {
	return s.a.loop.call(func() error { return s.subscribe(false, done) })
}

// Unsubscribe ends the subscription with a SUBSCRIBE with Expires 0 (RFC 6665 §4.1.2.3): it ends with the terminated
// NOTIFY that follows, or without it once Timer N fires. Before its first NOTIFY, or when that SUBSCRIBE cannot be
// sent, the subscription ends at once, with EndUnsubscribed; a NOTIFY for it then gets a 481.
func (s *Subscription) Unsubscribe() error {
	return s.a.loop.call(func() error {
		switch {
		case s.ended:
			return ErrState
		case s.unsubscribing:
			return nil
		case s.d == nil:
			s.end(End{Cause: EndUnsubscribed})
			return nil
		}

		if err := s.subscribe(true, nil); err != nil {
			s.end(End{Cause: EndUnsubscribed, Err: err})
		}

		return nil
	})
}

// subscribe sends a SUBSCRIBE within the dialog: a refresh, with the Expires of the initial SUBSCRIBE, or an
// unsubscribe. Timer N waits for the NOTIFY it brings.
func (s *Subscription) subscribe(unsubscribe bool, done func(*sip.Response, error)) error {
	if s.d == nil || s.ended || s.unsubscribing {
		return ErrState
	}

	req, err := s.d.NewRequest("SUBSCRIBE")
	if err != nil {
		return err
	}

	copyHeader(req, s.att.tmpl)

	if unsubscribe {
		req.Header.Set("Expires", "0")
	}

	c := &subscribeRequest{s: s, req: req, done: done, unsubscribe: unsubscribe}

	if err := c.send(); err != nil {
		return err
	}

	if unsubscribe {
		s.unsubscribing = true
		s.refresh.stop()
		s.refresh = nil
	}

	return nil
}

// copyHeader copies into a request within a dialog the header fields of the application of the initial request tmpl:
// all but those of the dialog and of the transaction.
func copyHeader(req, tmpl *sip.Request) {
	for _, f := range tmpl.Header {
		switch strings.ToLower(sip.LongName(f.Name)) {
		case "via", "cseq", "call-id", "from", "to", "route", "max-forwards", "content-length",
			"authorization", "proxy-authorization":
		default:
			req.Header.Add(f.Name, f.Value)
		}
	}
}

// subscribeRequest is a SUBSCRIBE within the dialog of a subscription, sent again after a challenge.
type subscribeRequest struct {
	s           *Subscription
	req         *sip.Request
	done        func(*sip.Response, error)
	unsubscribe bool
	tries       int
	final       bool
}

func (c *subscribeRequest) send() error {
	s := c.s

	if _, err := s.a.prepareAndSend(c.req, s.a.client(c.response, c.error)); err != nil {
		return err
	}

	if s.inflight == nil {
		s.inflight = map[*subscribeRequest]struct{}{}
	}

	s.inflight[c] = struct{}{}

	s.timer.stop()
	s.timer = s.a.after(64*s.a.layer.T1(), func() { s.end(End{Cause: EndNoNotify}) })

	return nil
}

// refreshEnds are the responses to a refresh that end the subscription (RFC 6665 §4.1.2.2).
func refreshEnds(code int) bool {
	switch code {
	case 404, 405, 410, 416, 489, 501, 604:
		return true
	}

	return code >= 480 && code <= 485
}

func (c *subscribeRequest) response(res *sip.Response) {
	s := c.s

	if res.IsProvisional() || c.final || s.ended {
		return
	}

	_ = s.d.ReceiveResponse(res)

	if next, ok := s.challenged(c.req, res, c.tries); ok {
		c.tries++
		c.req = next

		if err := c.send(); err != nil {
			c.error(err)
		}

		return
	}

	c.final = true
	delete(s.inflight, c)

	switch {
	case res.IsSuccess():
		if v, err := res.Header.Expires(); err == nil && !c.unsubscribe {
			s.setDuration(v)
		}
	case refreshEnds(res.StatusCode):
		s.end(End{Cause: EndRejected, Remote: true, Response: res})
	default:
		// The subscription lasts until its expiry; the refresh is tried again halfway to it.
		s.timer.stop()
		s.retryRefresh()
	}

	s.emitDone(c.done, res, nil)
}

func (c *subscribeRequest) error(err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true
	delete(s.inflight, c)

	s.timer.stop()

	if !s.ended {
		s.retryRefresh()
	}

	s.emitDone(c.done, nil, err)
}

// retryRefresh tries a failed refresh again halfway to the expiry, while that leaves time for one.
func (s *Subscription) retryRefresh() {
	if s.unsubscribing || s.expiry == nil || s.left < 2*64*s.a.layer.T1() {
		return
	}

	s.left /= 2
	s.refresh.stop()
	s.refresh = s.a.after(s.left, s.autoRefresh)
}

func (s *Subscription) emitDone(done func(*sip.Response, error), res *sip.Response, err error) {
	if done != nil {
		s.a.loop.emit(func() { done(res, err) })
	}
}

// challenged is the SUBSCRIBE to send again after a 401 or 407 to req: the next CSeq, with the credentials of
// Config.Authenticate (RFC 3261 §22.2).
func (s *Subscription) challenged(req *sip.Request, res *sip.Response, tries int) (*sip.Request, bool) {
	if res.StatusCode != 401 && res.StatusCode != 407 {
		return nil, false
	}

	next, err := s.d.NewRequest("SUBSCRIBE")
	if err != nil {
		return nil, false
	}

	copyHeader(next, req)

	if !s.a.authenticate(next, res, tries) {
		return nil, false
	}

	return next, true
}

// close ends the subscription as the agent closes, unsubscribing from a confirmed one.
func (s *Subscription) close() {
	if s.d != nil && !s.unsubscribing {
		if req, err := s.d.NewRequest("SUBSCRIBE"); err == nil {
			copyHeader(req, s.att.tmpl)
			req.Header.Set("Expires", "0")

			s.a.closing.Add(1)

			finished := false

			done := func() {
				if !finished {
					finished = true

					s.a.closing.Done()
				}
			}

			if _, err := s.a.prepareAndSend(req, s.a.client(func(res *sip.Response) {
				if !res.IsProvisional() {
					done()
				}
			}, func(error) { done() })); err != nil {
				done()
			}
		}
	}

	s.end(End{Cause: EndClosed})
}
