package ua

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
// a Handler do. No event follows Ended.
type SubscriptionHandler struct {
	// Accepted reports the 2xx to the initial SUBSCRIBE.
	Accepted func(s *Subscription, res *sip.Response)

	// Notify reports a NOTIFY r of the subscription, once the agent applied its Subscription-State. Notify may answer
	// r itself (r.Respond), as with a 400 for a body it cannot use; a response that ends the subscription for its
	// notifier (RFC 6665 §4.2.2, RFC 5057 Table 2), such as a 481, ends it with EndDropped. Otherwise the agent
	// answers 200 once Notify returns: a subscriber does not wait for the user (RFC 6665 §4.1.3). r.Done tells when
	// the transaction ends.
	Notify func(s *Subscription, r *Request)

	// Forked decides on a NOTIFY from another notifier than the first one, s the subscription it creates
	// (RFC 6665 §4.1.4): true keeps s, a subscription of its own with this handler; false, or no Forked, answers the
	// NOTIFY with a 481.
	Forked func(s *Subscription, notify *sip.Request) bool

	// Ended reports the end of the subscription:
	//   - EndTerminated by a NOTIFY, End.State telling whether to subscribe again (sip.SubscriptionState.Resubscribe);
	//   - EndRejected by a final response other than 2xx to the initial SUBSCRIBE, or by one to a refresh that ends
	//     the subscription (RFC 6665 §4.1.2.2);
	//   - EndLost by a failure that destroys its dialog (RFC 5057 §5.1);
	//   - EndNoNotify without a NOTIFY within Timer N;
	//   - EndExpired without a refresh, or by a NOTIFY with expires 0 not followed by the terminated one;
	//   - EndTimeout by the error of the initial SUBSCRIBE;
	//   - EndUnsubscribed by Unsubscribe without the terminated NOTIFY: before the first NOTIFY, or when the
	//     unsubscribe fails;
	//   - EndDropped by Drop, or by a NOTIFY the application refused;
	//   - EndClosed as the agent closes.
	Ended func(s *Subscription, e End)
}

// Subscription is a subscription of the agent, as subscriber (RFC 6665 §4.1), and the dialog usage its first NOTIFY
// creates (RFC 6665 §4.4.1). The agent refreshes it before it expires. Its state belongs to its strand.
type Subscription struct {
	a     *Agent
	st    *Strand
	sh    SubscriptionHandler
	att   *subscribing
	event sip.Event

	d     *dialog.Dialog
	state SubscriptionState
	value any

	// duration is the duration the notifier last granted: the Expires of the 2xx to a SUBSCRIBE, or the expires of
	// a NOTIFY. expiresAt is when the subscription expires without a refresh, which a NOTIFY moves (RFC 6665
	// §4.1.3); lead how long before it the agent refreshes, from the duration of the last 2xx; refreshAt when it
	// does.
	duration  time.Duration
	expiresAt time.Time
	lead      time.Duration
	refreshAt time.Time

	refresh, expiry *timer

	// timerN waits for the NOTIFY of the SUBSCRIBE nOwner, a refresh (RFC 6665 §4.1.2.2); guard for the terminated
	// NOTIFY of an unsubscribe, or of a NOTIFY with expires 0. unsubscribing marks a SUBSCRIBE with Expires 0
	// sent, closing one sent as the agent closes.
	timerN        *timer
	nOwner        *subscribeRequest
	guard         *timer
	unsubscribing bool
	closing       bool
	ended         bool

	// inflight are its SUBSCRIBEs in progress, completed with ErrState should the subscription end first.
	inflight map[*subscribeRequest]struct{}

	// log is the logger of the dialog, made once.
	log *slog.Logger
}

type subscribeKey struct {
	callID, fromTag string
}

// subscribing is an initial SUBSCRIBE of the agent: until Timer N fires, its NOTIFYs create subscriptions, the first
// one the Subscription of Agent.Subscribe, and of other notifiers forks (RFC 6665 §4.1.2.4, §4.1.4).
type subscribing struct {
	a     *Agent
	st    *Strand
	key   subscribeKey
	first *Subscription

	// tmpl is the SUBSCRIBE or the REFER as built, before Prepare; sub the SUBSCRIBE whose header fields and bodies
	// the refreshes repeat: tmpl itself, or for a REFER one asking for the refer event (RFC 3515 §2.4.4). tries
	// counts the challenges answered, retried a 423 retried.
	tmpl    *sip.Request
	sub     *sip.Request
	refer   bool
	tries   int
	retried bool
	final   bool

	// subs are the subscriptions of the NOTIFYs, by the tag of their notifier, ended ones and refused forks
	// included; okTag and okExpires the notifier and the Expires of the 2xx, for its subscription.
	subs      map[string]*Subscription
	okTag     string
	okExpires *uint32

	timerN *timer
}

// Subscribe sends an initial SUBSCRIBE (RFC 6665 §4.1.2.1), and returns its subscription, in the neutral state until
// its first NOTIFY. req carries the Request-URI, To, Event, Accept, Expires, the Route of an outbound proxy or a
// service route, the header fields of the application and its bodies, such as a filter; the refreshes repeat them.
// The agent adds what req leaves out: the From tag, a Call-ID, CSeq, Max-Forwards and Contact. It answers 401 and
// 407 challenges through Config.Authenticate, and a 423 with its Min-Expires.
func (a *Agent) Subscribe(req *sip.Request, h SubscriptionHandler, opts ...Option) (*Subscription, error) {
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

	st := a.options(opts).strand(a)
	att := &subscribing{
		a: a, st: st, key: subscribeKey{callID: tmpl.Header.CallID(), fromTag: from.Tag()},
		tmpl: tmpl, sub: tmpl, subs: map[string]*Subscription{},
	}
	s := &Subscription{a: a, st: st, sh: h, att: att, event: event}
	att.first = s

	// Its NOTIFY may come before the 2xx, as soon as it is sent (RFC 6665 §4.1.2.4).
	if err := a.addSubscribing(att); err != nil {
		return nil, err
	}

	if err := st.do(att.send); err != nil {
		a.removeSubscribing(att)
		return nil, err
	}

	return s, nil
}

// Strand is the strand of the subscription.
func (s *Subscription) Strand() *Strand {
	return s.st
}

func (att *subscribing) send() error {
	a := att.a

	if _, err := a.prepareAndSend(att.tmpl, att.st.client(att.response, att.error)); err != nil {
		return err
	}

	att.timerN.stop()
	att.timerN = att.st.after(64*a.layer.T1(), att.noNotify)

	return nil
}

// response handles a response to the initial SUBSCRIBE.
func (att *subscribing) response(res *sip.Response) {
	a := att.a
	first := att.first

	if res.IsProvisional() || att.final {
		return
	}

	// A challenge or a 423 is answered with the next CSeq, unless the subscription ended meanwhile.
	if !res.IsSuccess() && !first.ended {
		next := att.tmpl.Clone()
		nextCSeq(next)

		retry := false

		switch {
		case res.StatusCode == 423 && !att.retried && raiseExpires(next, res):
			// RFC 6665 §4.2.1.1: the duration the notifier asks for at least.
			att.retried, retry = true, true
		case a.authenticate(next, res, att.tries):
			att.tries++
			retry = true
		}

		if retry {
			att.tmpl = next
			if !att.refer {
				att.sub = next
			}

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
		if first.d == nil {
			first.end(End{Cause: EndRejected, Remote: true, Response: res})
		}

		return
	}

	// A REFER accepted without a subscription (RFC 4488, RFC 7614): its NOTIFYs get a 481 from now on.
	if att.refer && noSubscription(att.tmpl, res) {
		att.done()

		if first.d == nil && !first.ended {
			first.emit(func(h SubscriptionHandler) {
				if h.Accepted != nil {
					h.Accepted(first, res)
				}
			})
			first.end(End{Cause: EndNoSubscription, Remote: true, Response: res})
		}

		return
	}

	to, _ := res.Header.To()
	att.okTag = to.Tag()

	// RFC 6665 §4.2.1.1: the 2xx carries the duration; without it, the one asked for.
	if v, err := res.Header.Expires(); err == nil {
		att.okExpires = &v
	} else if v, err := att.tmpl.Header.Expires(); err == nil {
		att.okExpires = &v
	}

	switch s := att.subs[att.okTag]; {
	case att.okExpires == nil:
	case s != nil:
		s.granted(*att.okExpires)
	case first.d == nil:
		// Its timers start with its dialog.
		first.duration = time.Duration(*att.okExpires) * time.Second
		first.expiresAt = a.clock.Now().Add(first.duration)
	}

	if first.ended {
		return
	}

	first.emit(func(h SubscriptionHandler) {
		if h.Accepted != nil {
			h.Accepted(first, res)
		}
	})
}

// raiseExpires sets the Expires of the next SUBSCRIBE to the Min-Expires of a 423, reporting whether it has one.
func raiseExpires(next *sip.Request, res *sip.Response) bool {
	v, err := strconv.ParseUint(strings.TrimSpace(res.Header.Get("Min-Expires")), 10, 32)
	if err != nil {
		return false
	}

	next.Header.Set("Expires", strconv.FormatUint(v, 10))

	return true
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
	att.a.removeSubscribing(att)
}

// notifyFor receives a NOTIFY outside the dialogs of the agent: the first one of a subscription of the SUBSCRIBE, or of
// a fork (RFC 6665 §4.4.1).
func (att *subscribing) notifyFor(tx *transaction.ServerTransaction, req *sip.Request) {
	a := att.a
	first := att.first

	ss, code := checkNotify(req, first)
	if code != 0 {
		a.reply(tx, code)
		return
	}

	from, _ := req.Header.From()
	tag := from.Tag()

	// A notifier whose subscription ended, or whose fork was refused, has no subscription any more.
	if s := att.subs[tag]; s != nil || first.ended {
		a.reply(tx, 481)
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
		first.notified(tx, req, func() { first.end(End{Cause: EndTerminated, Remote: true, Request: req, State: ss}) })

		return
	}

	d, err := dialog.NewFromNotify(att.tmpl, req)
	if err != nil {
		a.reply(tx, 400)
		return
	}

	if first.d == nil {
		first.bind(d, req)
		first.receiveNotify(tx, req, ss)

		return
	}

	fork := &Subscription{a: a, st: att.st, sh: first.sh, att: att, event: first.event}
	fork.bind(d, req)

	att.st.emit(func() {
		keep := fork.sh.Forked != nil && fork.sh.Forked(fork, req)

		_ = att.st.do(func() error {
			if !keep || fork.ended {
				fork.ended = true
				fork.unbind()
				a.reply(tx, 481)

				return nil
			}

			fork.receiveNotify(tx, req, ss)

			return nil
		})
	})
}

// checkNotify checks a NOTIFY for the subscription s: its Event, with 489 for another event package and 481 for
// another subscription of the package (RFC 6665 §4.1.3, §8.2.1), and a Subscription-State, with 400 otherwise.
func checkNotify(req *sip.Request, s *Subscription) (sip.SubscriptionState, int) {
	ev, ok, err := req.Header.Event()

	switch {
	case !ok || err != nil:
		return sip.SubscriptionState{}, 400
	case ev.Type() != s.event.Type():
		return sip.SubscriptionState{}, 489
	case !s.matches(ev):
		return sip.SubscriptionState{}, 481
	}

	ss, ok, err := req.Header.SubscriptionState()
	if !ok || err != nil {
		return sip.SubscriptionState{}, 400
	}

	return ss, 0
}

// matches reports whether an Event names the subscription (RFC 6665 §8.2.1). The NOTIFYs of a REFER carry the id of
// its CSeq, or none for the first REFER of a dialog (RFC 3515 §2.4.6).
func (s *Subscription) matches(ev sip.Event) bool {
	if s.att.refer {
		return ev.Type() == "refer" && (ev.ID == "" || ev.ID == s.event.ID)
	}

	return s.event.Matches(ev)
}

// bind gives the subscription the dialog its first NOTIFY creates. A subscription of a REFER takes the Event of that
// NOTIFY, with its id or without, for its refreshes (RFC 3515 §2.4.6).
func (s *Subscription) bind(d *dialog.Dialog, notify *sip.Request) {
	if s.att.refer {
		if ev, ok, err := notify.Header.Event(); ok && err == nil {
			s.event = ev
		}
	}

	s.d = d

	tag := d.ID().RemoteTag
	s.att.subs[tag] = s
	s.a.addSubscription(s)

	if tag == s.att.okTag && s.att.okExpires != nil {
		s.granted(*s.att.okExpires)
	}
}

// unbind removes a refused fork: its tag stays in the subscriptions of the SUBSCRIBE, for a 481 to its NOTIFYs.
func (s *Subscription) unbind() {
	if s.a.removeSubscription(s) {
		s.d.Terminate()
	}
}

// receive handles a SUBSCRIBE, NOTIFY or REFER of the notifier within the dialog, for the Event of the subscription
// (RFC 5057 §5.3).
func (s *Subscription) receive(tx *transaction.ServerTransaction, req *sip.Request) {
	switch {
	case s.ended || s.state == SubscriptionTerminated:
		s.a.reply(tx, 481)
		return
	case req.Method != "NOTIFY":
		// RFC 5057 §5.6: a new usage refused, the dialog unharmed.
		s.a.reply(tx, 603)
		return
	}

	ss, code := checkNotify(req, s)
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

// receiveNotify applies the Subscription-State of a NOTIFY (RFC 6665 §4.1.3), then reports it, and answers it.
func (s *Subscription) receiveNotify(tx *transaction.ServerTransaction, req *sip.Request, ss sip.SubscriptionState) {
	// A NOTIFY ends the wait of a refresh; that of an unsubscribe waits for the terminated one (RFC 6665 §4.1.2.3).
	s.timerN.stop()
	s.timerN, s.nOwner = nil, nil

	if ss.State == sip.SubscriptionTerminated {
		// RFC 6665 §4.4.1: the subscription ends with the transaction of its terminated NOTIFY, once answered.
		s.state = SubscriptionTerminated
		s.notified(tx, req, func() { s.end(End{Cause: EndTerminated, Remote: true, Request: req, State: ss}) })

		return
	}

	if ss.State == sip.SubscriptionPending {
		s.state = SubscriptionPending
	} else {
		s.state = SubscriptionActive
	}

	if ss.Expires != nil && !s.unsubscribing {
		s.notifiedExpires(*ss.Expires)
	}

	s.notified(tx, req, nil)
}

// notified reports a NOTIFY to the application, then answers it with a 200 unless Notify did; then, when not nil,
// runs with the strand held.
func (s *Subscription) notified(tx *transaction.ServerTransaction, req *sip.Request, then func()) {
	r := newRequest(nil, tx, req)
	r.a, r.st, r.sub = s.a, s.st, s

	s.st.emit(func() {
		if h := s.sh.Notify; h != nil {
			h(s, r)
		}

		_ = s.st.do(func() error {
			if !r.final {
				_ = s.answerNotify(r, Reply{Code: 200})
			}

			if then != nil {
				then()
			}

			return nil
		})
	})
}

// answerNotify sends the response to a NOTIFY of the subscription, and applies its effect: one that ends the
// subscription for the notifier ends it here too (RFC 6665 §4.2.2, RFC 5057 §5.1).
func (s *Subscription) answerNotify(r *Request, rp Reply) error {
	if r.final {
		return transaction.ErrFinalResponseSent
	}

	res, err := r.build(rp)
	if err != nil {
		return err
	}

	// The 2xx to the NOTIFY that made the dialog carries its Record-Route (RFC 3261 §12.1.1); a NOTIFY refreshes the
	// target of the dialog (RFC 6665 §4.4.1): every 2xx carries the Contact of the agent.
	if s.d != nil {
		s.d.PrepareResponse(r.req, res)
	}

	if res.IsSuccess() && !res.Header.Has("Contact") {
		res.Header.Set("Contact", s.att.sub.Header.Get("Contact"))
	}

	r.complete(res)
	r.final = true

	if err := r.tx.Respond(res); err != nil {
		return err
	}

	if res.IsSuccess() || s.ended {
		return nil
	}

	switch failureOf("NOTIFY", res, nil) {
	case destroysDialog:
		s.a.destroyDialog(s.d.ID(), nil, nil)
	case destroysUsage:
		s.end(End{Cause: EndDropped, Response: res})
	}

	return nil
}

func (s *Subscription) emit(f func(h SubscriptionHandler)) {
	s.st.emit(func() { f(s.sh) })
}

// refreshLead is how long before the expiry of a subscription of duration d the agent refreshes it: halfway, or
// 600 s before when it lasts more than 1200 s (TS 24.229 §5.1.1.3).
func refreshLead(d time.Duration) time.Duration {
	if d > 1200*time.Second {
		return 600 * time.Second
	}

	return d / 2
}

// granted sets the duration the 2xx to a SUBSCRIBE granted, from now (RFC 6665 §4.1.2.1): the expiry, and the refresh
// at its lead before it.
func (s *Subscription) granted(seconds uint32) {
	if s.ended {
		return
	}

	s.duration = time.Duration(seconds) * time.Second

	if seconds == 0 {
		s.expireNow()
		return
	}

	s.lead = refreshLead(s.duration)
	s.setExpiry(s.a.clock.Now().Add(s.duration))
	s.schedule(s.expiresAt.Add(-s.lead), true)
}

// notifiedExpires applies the expires of a NOTIFY (RFC 6665 §4.1.3): it moves the expiry; the refresh comes its lead
// before it, or halfway to it when that is past, and stays where it was when earlier.
func (s *Subscription) notifiedExpires(seconds uint32) {
	if seconds == 0 {
		s.expireNow()
		return
	}

	now := s.a.clock.Now()
	d := time.Duration(seconds) * time.Second
	s.duration = d

	if s.lead == 0 {
		s.lead = refreshLead(d)
	}

	s.setExpiry(now.Add(d))

	at := s.expiresAt.Add(-s.lead)
	if !at.After(now) {
		at = now.Add(d / 2)
	}

	s.schedule(at, false)
}

func (s *Subscription) setExpiry(at time.Time) {
	s.expiresAt = at
	s.expiry.stop()
	s.expiry = s.st.after(at.Sub(s.a.clock.Now()), func() { s.end(End{Cause: EndExpired}) })
}

// schedule sets the refresh at at, or keeps an earlier one unless force.
func (s *Subscription) schedule(at time.Time, force bool) {
	if s.d == nil || s.unsubscribing || s.ended {
		return
	}

	if !force && !s.refreshAt.IsZero() && s.refresh != nil && !s.refresh.stopped && s.refreshAt.Before(at) {
		return
	}

	s.refreshAt = at
	s.refresh.stop()
	s.refresh = s.st.after(max(at.Sub(s.a.clock.Now()), 0), s.autoRefresh)
}

// expireNow ends a subscription its notifier gave no time left: once the terminated NOTIFY comes, or without it
// after 64*T1 (RFC 6665 §4.1.3).
func (s *Subscription) expireNow() {
	s.refresh.stop()
	s.expiry.stop()
	s.refresh, s.expiry = nil, nil

	if s.guard == nil {
		s.guard = s.st.after(64*s.a.layer.T1(), func() { s.end(End{Cause: EndExpired}) })
	}
}

func (s *Subscription) autoRefresh() {
	if err := s.subscribe(false, Change{}, nil); err != nil {
		s.logger().Debug("subscription refresh failed", slog.Any("error", err))
		s.retryRefresh()
	}
}

// retryRefresh tries a failed refresh again halfway to the expiry, while that leaves time for one.
func (s *Subscription) retryRefresh() {
	if s.unsubscribing || s.ended || s.expiresAt.IsZero() {
		return
	}

	left := s.expiresAt.Sub(s.a.clock.Now())
	if left < 2*64*s.a.layer.T1() {
		return
	}

	s.schedule(s.a.clock.Now().Add(left/2), true)
}

// end terminates the subscription, once: its timers stop, its requests in progress complete, and its dialog usage
// ends, with the dialog if it was the last one (RFC 5057 §4).
func (s *Subscription) end(e End) {
	s.st.check()

	if s.ended {
		return
	}

	s.ended = true
	s.state = SubscriptionTerminated

	for _, t := range []*timer{s.refresh, s.expiry, s.timerN, s.guard} {
		t.stop()
	}

	for c := range s.inflight {
		c.final = true
		s.emitDone(c.done, nil, ErrState)
	}

	s.inflight = nil

	if s.d != nil && s.a.removeSubscription(s) {
		s.d.Terminate()
	}

	if s.att.first == s && s.d == nil || e.Cause == EndUnsubscribed || e.Cause == EndDropped {
		s.att.done()
	}

	// An unsubscribe of Close ends as the agent closes, however it ends.
	if s.closing {
		s.closing = false
		e.Cause = EndClosed

		s.a.await(-1)
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
	return get(s.st, func() SubscriptionState { return s.state })
}

// Duration is the duration the notifier last granted: in the 2xx to a SUBSCRIBE, or in a NOTIFY.
func (s *Subscription) Duration() time.Duration {
	return get(s.st, func() time.Duration { return s.duration })
}

// Expires is when the subscription expires without a refresh, on the clock of the agent; zero before the 2xx or the
// first NOTIFY tells.
func (s *Subscription) Expires() time.Time {
	return get(s.st, func() time.Time { return s.expiresAt })
}

// Dialog is the state of the dialog of the subscription, once its first NOTIFY created it.
func (s *Subscription) Dialog() (dialog.Snapshot, bool) {
	var (
		snap dialog.Snapshot
		ok   bool
	)

	_ = s.st.do(func() error {
		if s.d != nil {
			snap, ok = s.d.Snapshot(), true
		}

		return nil
	})

	return snap, ok
}

// Value is the value the application attached to the subscription.
func (s *Subscription) Value() any {
	return get(s.st, func() any { return s.value })
}

func (s *Subscription) SetValue(v any) {
	_ = s.st.do(func() error {
		s.value = v
		return nil
	})
}

// Refresh refreshes the subscription now, with a SUBSCRIBE within its dialog (RFC 6665 §4.1.2.2): the header fields
// and bodies of the initial SUBSCRIBE, those of c in their place, such as another filter. done receives its final
// response or its error, as a handler. The agent refreshes on its own before the subscription expires.
func (s *Subscription) Refresh(c Change, done func(res *sip.Response, err error)) error {
	return s.st.do(func() error { return s.subscribe(false, c, done) })
}

// Unsubscribe ends the subscription with a SUBSCRIBE with Expires 0 (RFC 6665 §4.1.2.3), with the header fields of
// the initial SUBSCRIBE and of c, and the bodies of c: it ends with the terminated NOTIFY that follows, or without it
// after 64*T1. Before its first NOTIFY, or when that SUBSCRIBE fails, it ends at once, with EndUnsubscribed; its
// NOTIFYs then get a 481, which ends it for the notifier too (RFC 6665 §4.2.2).
func (s *Subscription) Unsubscribe(c Change) error {
	return s.st.do(func() error {
		switch {
		case s.ended:
			return ErrState
		case s.unsubscribing:
			return nil
		case s.d == nil:
			s.end(End{Cause: EndUnsubscribed})
			return nil
		}

		if err := s.subscribe(true, c, nil); err != nil {
			s.end(End{Cause: EndUnsubscribed, Err: err})
		}

		return nil
	})
}

// Drop ends the subscription at once, without a SUBSCRIBE: the application knows its notifier no longer has it,
// as a UE once deregistered (TS 24.229 §5.1.1.7). Its NOTIFYs then get a 481 (RFC 6665 §4.2.2).
func (s *Subscription) Drop() {
	_ = s.st.do(func() error {
		s.end(End{Cause: EndDropped})
		return nil
	})
}

// subscribe sends a SUBSCRIBE within the dialog: a refresh, with the Expires of the initial SUBSCRIBE, or an
// unsubscribe. Timer N waits for the NOTIFY of a refresh; a guard, for the terminated NOTIFY of the unsubscribe.
func (s *Subscription) subscribe(unsubscribe bool, ch Change, done func(*sip.Response, error)) error {
	if s.d == nil || s.ended || s.unsubscribing {
		return ErrState
	}

	c := &subscribeRequest{s: s, change: ch, done: done, unsubscribe: unsubscribe}

	req, err := c.build()
	if err != nil {
		return err
	}

	c.req = req

	if err := c.send(); err != nil {
		return err
	}

	if unsubscribe {
		s.unsubscribing = true
		s.refresh.stop()
		s.refresh = nil
		s.guard.stop()
		s.guard = s.st.after(64*s.a.layer.T1(), func() { s.end(End{Cause: EndUnsubscribed}) })
	}

	return nil
}

// copyHeader copies into a request within a dialog the header fields of the application of the initial request tmpl:
// all but those of the dialog, of the transaction, and of its bodies.
func copyHeader(req, tmpl *sip.Request) {
	for _, f := range tmpl.Header {
		switch name := strings.ToLower(sip.LongName(f.Name)); {
		case strings.HasPrefix(name, "content-"), name == "mime-version":
		case name == "via", name == "cseq", name == "call-id", name == "from", name == "to", name == "route",
			name == "max-forwards", name == "authorization", name == "proxy-authorization":
		default:
			req.Header.Add(f.Name, f.Value)
		}
	}
}

// applyChange puts the header fields of c in place of those of the same names, and sets the bodies of c, if any, or
// else those of tmpl when bodies is set.
func applyChange(req, tmpl *sip.Request, c Change, bodies bool) {
	for _, f := range c.Header {
		req.Header.Del(f.Name)
	}

	for _, f := range c.Header {
		req.Header.Add(f.Name, f.Value)
	}

	switch {
	case c.Bodies != nil:
		req.SetBodies(c.Bodies...)
	case bodies && len(tmpl.Body) > 0:
		for _, f := range tmpl.Header {
			if name := strings.ToLower(sip.LongName(f.Name)); strings.HasPrefix(name, "content-") && name != "content-length" ||
				name == "mime-version" {
				req.Header.Add(f.Name, f.Value)
			}
		}

		req.Body = tmpl.Body
		req.Header.Set("Content-Length", strconv.Itoa(len(tmpl.Body)))
	}
}

// subscribeRequest is a SUBSCRIBE within the dialog of a subscription, built again after a challenge or a 423.
type subscribeRequest struct {
	s           *Subscription
	change      Change
	req         *sip.Request
	done        func(*sip.Response, error)
	unsubscribe bool
	tries       int
	retried     bool
	final       bool
}

// build makes the SUBSCRIBE, with the next CSeq of the dialog.
func (c *subscribeRequest) build() (*sip.Request, error) {
	s := c.s

	req, err := s.d.NewRequest("SUBSCRIBE")
	if err != nil {
		return nil, err
	}

	copyHeader(req, s.att.sub)
	req.Header.Set("Event", s.event.String())
	applyChange(req, s.att.sub, c.change, !c.unsubscribe)

	if c.unsubscribe {
		req.Header.Set("Expires", "0")
	}

	return req, nil
}

func (c *subscribeRequest) send() error {
	s := c.s

	if _, err := s.a.prepareAndSend(c.req, s.st.client(c.response, c.error)); err != nil {
		return err
	}

	if s.inflight == nil {
		s.inflight = map[*subscribeRequest]struct{}{}
	}

	s.inflight[c] = struct{}{}

	if !c.unsubscribe {
		s.timerN.stop()
		s.timerN, s.nOwner = s.st.after(64*s.a.layer.T1(), func() { s.end(End{Cause: EndNoNotify}) }), c
	}

	return nil
}

// refreshEnds reports a failure of a refresh that ends the subscription: one of RFC 6665 §4.1.2.2, which are those
// that destroy the usage or the dialog (RFC 5057 Table 2); a timeout or a 408 leaves it until it expires.
func refreshEnds(res *sip.Response) effect {
	if res == nil || res.StatusCode == 408 {
		return affectsTransaction
	}

	return failureOf("SUBSCRIBE", res, nil)
}

func (c *subscribeRequest) response(res *sip.Response) {
	s := c.s

	if res.IsProvisional() || c.final || s.ended {
		return
	}

	_ = s.d.ReceiveResponse(res)

	if next, ok := c.retry(res); ok {
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
		// RFC 6665 §4.1.2.2: a 204 (RFC 5839) brings no NOTIFY.
		if res.StatusCode == 204 && s.nOwner == c {
			s.timerN.stop()
			s.timerN, s.nOwner = nil, nil
		}

		if !c.unsubscribe {
			if v, err := res.Header.Expires(); err == nil {
				s.granted(v)
			} else if v, err := s.att.sub.Header.Expires(); err == nil {
				s.granted(v)
			}
		}
	case c.unsubscribe:
		s.end(End{Cause: EndUnsubscribed, Remote: true, Response: res})
	default:
		switch refreshEnds(res) {
		case destroysDialog:
			s.a.destroyDialog(s.d.ID(), res, nil)
		case destroysUsage:
			s.end(End{Cause: EndRejected, Remote: true, Response: res})
		default:
			// The subscription lasts until its expiry; the refresh is tried again halfway to it.
			if s.nOwner == c {
				s.timerN.stop()
				s.timerN, s.nOwner = nil, nil
			}

			s.retryRefresh()
		}
	}

	s.emitDone(c.done, res, nil)
}

// retry is the SUBSCRIBE to send again after a 401 or 407 (RFC 3261 §22.2) or a 423 (RFC 6665 §4.2.1.1): built anew,
// with the next CSeq.
func (c *subscribeRequest) retry(res *sip.Response) (*sip.Request, bool) {
	s := c.s

	switch {
	case res.StatusCode == 423 && !c.retried && !c.unsubscribe:
		next := s.att.sub.Clone()
		if !raiseExpires(next, res) {
			return nil, false
		}

		c.retried = true
		s.att.sub = next

		if !s.att.refer {
			s.att.tmpl = next
		}
	case res.StatusCode == 401 || res.StatusCode == 407:
	default:
		return nil, false
	}

	next, err := c.build()
	if err != nil {
		return nil, false
	}

	if res.StatusCode == 401 || res.StatusCode == 407 {
		if !s.a.authenticate(next, res, c.tries) {
			return nil, false
		}

		c.tries++
	}

	return next, true
}

func (c *subscribeRequest) error(err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true
	delete(s.inflight, c)

	switch {
	case s.ended:
	case c.unsubscribe:
		s.end(End{Cause: EndUnsubscribed, Err: err})
	default:
		if s.nOwner == c {
			s.timerN.stop()
			s.timerN, s.nOwner = nil, nil
		}

		s.retryRefresh()
	}

	s.emitDone(c.done, nil, err)
}

func (s *Subscription) emitDone(done func(*sip.Response, error), res *sip.Response, err error) {
	if done != nil {
		s.st.emit(func() { done(res, err) })
	}
}

// close ends the subscription as the agent closes: one with a dialog unsubscribes, and waits for its terminated
// NOTIFY, which Close waits for; one without ends at once.
func (s *Subscription) close() {
	if s.ended || s.closing {
		return
	}

	if s.d == nil || s.unsubscribing {
		s.end(End{Cause: EndClosed})
		return
	}

	if err := s.subscribe(true, Change{}, nil); err != nil {
		s.end(End{Cause: EndClosed, Err: err})
		return
	}

	s.closing = true
	s.a.await(1)
}
