package ua

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

// RFC 6665 section 4.1.3
type SubscriptionState int

const (
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

type SubscriptionHandler struct {
	Accepted func(ctx context.Context, s *Subscription, res *sip.Response)

	Notify func(ctx context.Context, s *Subscription, r *Request)

	Forked func(ctx context.Context, s *Subscription, notify *sip.Request) bool

	Ended func(ctx context.Context, s *Subscription, e End)
}

// RFC 6665 section 4.1
// RFC 6665 section 4.4.1
type Subscription struct {
	a     *Agent
	st    *Strand
	sh    SubscriptionHandler
	att   *subscribing
	event sip.Event

	d     *dialog.Dialog
	state SubscriptionState
	value any

	duration  time.Duration
	expiresAt time.Time
	lead      time.Duration
	refreshAt time.Time

	refresh, expiry *timer

	timerN        *timer
	nOwner        *subscribeRequest
	guard         *timer
	unsubscribing bool
	closing       bool
	ended         bool

	inflight map[*subscribeRequest]struct{}

	log *slog.Logger
}

type subscribeKey struct {
	callID, fromTag string
}

// RFC 6665 section 4.1.2.4
// RFC 6665 section 4.1.4
type subscribing struct {
	a      *Agent
	st     *Strand
	key    subscribeKey
	first  *Subscription
	origin trace.SpanContext

	tmpl    *sip.Request
	sub     *sip.Request
	refer   bool
	tries   int
	retried bool
	final   bool

	subs      map[string]*Subscription
	okTag     string
	okExpires *uint32

	timerN *timer
}

// RFC 6665 section 4.1.2.1
func (a *Agent) Subscribe(ctx context.Context, req *sip.Request, h SubscriptionHandler, opts ...Option) (*Subscription, error) {
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
		tmpl: tmpl, sub: tmpl, subs: map[string]*Subscription{}, origin: trace.SpanContextFromContext(ctx),
	}
	s := &Subscription{a: a, st: st, sh: h, att: att, event: event}
	att.first = s

	if err := a.addSubscribing(att); err != nil {
		return nil, err
	}

	if err := st.do(func() error { return att.send(ctx) }); err != nil {
		a.removeSubscribing(att)
		return nil, err
	}

	return s, nil
}

func (s *Subscription) Strand() *Strand {
	return s.st
}

func (att *subscribing) send(ctx context.Context) error {
	a := att.a

	if _, err := a.prepareAndSend(ctx, att.tmpl, att.st.client(att.response, att.error)); err != nil {
		return err
	}

	att.timerN.stop()
	att.timerN = att.st.later(ctx, 64*a.layer.T1(), att.noNotify)

	return nil
}

func (att *subscribing) response(ctx context.Context, res *sip.Response) {
	a := att.a
	first := att.first

	if res.IsProvisional() || att.final {
		return
	}

	if !res.IsSuccess() && !first.ended {
		next := att.tmpl.Clone()
		nextCSeq(next)

		retry := false

		switch {
		case res.StatusCode == 423 && !att.retried && raiseExpires(next, res):
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

			if err := att.send(ctx); err != nil {
				att.error(ctx, err)
			}

			return
		}
	}

	att.final = true

	if !res.IsSuccess() {
		att.done()

		if first.d == nil {
			first.end(ctx, End{Cause: EndRejected, Remote: true, Response: res})
		}

		return
	}

	if att.refer && noSubscription(att.tmpl, res) {
		att.done()

		if first.d == nil && !first.ended {
			first.emit(func(h SubscriptionHandler) {
				if h.Accepted != nil {
					h.Accepted(ctx, first, res)
				}
			})
			first.end(ctx, End{Cause: EndNoSubscription, Remote: true, Response: res})
		}

		return
	}

	to, _ := res.Header.To()
	att.okTag = to.Tag()

	if v, err := res.Header.Expires(); err == nil {
		att.okExpires = &v
	} else if v, err := att.tmpl.Header.Expires(); err == nil {
		att.okExpires = &v
	}

	switch s := att.subs[att.okTag]; {
	case att.okExpires == nil:
	case s != nil:
		s.granted(ctx, *att.okExpires)
	case first.d == nil:
		first.duration = time.Duration(*att.okExpires) * time.Second
		first.expiresAt = a.clock.Now().Add(first.duration)
	}

	if first.ended {
		return
	}

	first.emit(func(h SubscriptionHandler) {
		if h.Accepted != nil {
			h.Accepted(ctx, first, res)
		}
	})
}

func raiseExpires(next *sip.Request, res *sip.Response) bool {
	v, err := strconv.ParseUint(strings.TrimSpace(res.Header.Get("Min-Expires")), 10, 32)
	if err != nil {
		return false
	}

	next.Header.Set("Expires", strconv.FormatUint(v, 10))

	return true
}

func (att *subscribing) error(ctx context.Context, err error) {
	if att.final {
		return
	}

	att.final = true
	att.done()

	if s := att.first; s.d == nil {
		s.end(ctx, failureEnd(err))
	}
}

// RFC 6665 section 4.1.2.4
func (att *subscribing) noNotify(ctx context.Context) {
	att.done()

	if s := att.first; s.d == nil {
		s.end(ctx, End{Cause: EndNoNotify})
	}
}

func (att *subscribing) done() {
	att.timerN.stop()
	att.a.removeSubscribing(att)
}

// RFC 6665 section 4.4.1
func (att *subscribing) notifyFor(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	a := att.a
	first := att.first

	ss, code := checkNotify(req, first)
	if code != 0 {
		a.reply(tx, code)
		return
	}

	from, _ := req.Header.From()
	tag := from.Tag()

	if s := att.subs[tag]; s != nil || first.ended {
		a.reply(tx, 481)
		return
	}

	if ss.State == sip.SubscriptionTerminated {
		if first.d != nil {
			a.reply(tx, 200)
			return
		}

		att.done()

		first.state = SubscriptionTerminated
		first.notified(ctx, tx, req, func() { first.end(ctx, End{Cause: EndTerminated, Remote: true, Request: req, State: ss}) })

		return
	}

	d, err := dialog.NewFromNotify(att.tmpl, req)
	if err != nil {
		a.reply(tx, 400)
		return
	}

	if first.d == nil {
		first.bind(ctx, d, req)
		first.receiveNotify(ctx, tx, req, ss)

		return
	}

	fork := &Subscription{a: a, st: att.st, sh: first.sh, att: att, event: first.event}
	fork.bind(ctx, d, req)

	att.st.emit(func() {
		keep := fork.sh.Forked != nil && fork.sh.Forked(ctx, fork, req)

		_ = att.st.do(func() error {
			if !keep || fork.ended {
				fork.ended = true
				fork.unbind()
				a.reply(tx, 481)

				return nil
			}

			fork.receiveNotify(ctx, tx, req, ss)

			return nil
		})
	})
}

// RFC 6665 section 4.1.3
// RFC 6665 section 8.2.1
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

// RFC 6665 section 8.2.1
// RFC 3515 section 2.4.6
func (s *Subscription) matches(ev sip.Event) bool {
	if s.att.refer {
		return ev.Type() == "refer" && (ev.ID == "" || ev.ID == s.event.ID)
	}

	return s.event.Matches(ev)
}

// RFC 3515 section 2.4.6
func (s *Subscription) bind(ctx context.Context, d *dialog.Dialog, notify *sip.Request) {
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
		s.granted(ctx, *s.att.okExpires)
	}
}

func (s *Subscription) unbind() {
	if s.a.removeSubscription(s) {
		s.d.Terminate()
	}
}

// RFC 5057 section 5.3
func (s *Subscription) receive(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	switch {
	case s.ended || s.state == SubscriptionTerminated:
		s.a.reply(tx, 481)
		return
	case req.Method != "NOTIFY":
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

	s.receiveNotify(ctx, tx, req, ss)
}

// RFC 6665 section 4.1.3
func (s *Subscription) receiveNotify(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request, ss sip.SubscriptionState) {
	s.timerN.stop()
	s.timerN, s.nOwner = nil, nil

	if ss.State == sip.SubscriptionTerminated {
		s.state = SubscriptionTerminated
		s.notified(ctx, tx, req, func() { s.end(ctx, End{Cause: EndTerminated, Remote: true, Request: req, State: ss}) })

		return
	}

	if ss.State == sip.SubscriptionPending {
		s.state = SubscriptionPending
	} else {
		s.state = SubscriptionActive
	}

	if ss.Expires != nil && !s.unsubscribing {
		s.notifiedExpires(ctx, *ss.Expires)
	}

	s.notified(ctx, tx, req, nil)
}

func (s *Subscription) notified(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request, then func()) {
	r := newRequest(ctx, nil, tx, req)
	r.a, r.st, r.sub = s.a, s.st, s

	s.st.emit(func() {
		if h := s.sh.Notify; h != nil {
			h(ctx, s, r)
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

// RFC 6665 section 4.2.2
// RFC 5057 section 5.1
func (s *Subscription) answerNotify(r *Request, rp Reply) error {
	if r.final {
		return transaction.ErrFinalResponseSent
	}

	res, err := r.build(rp)
	if err != nil {
		return err
	}

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
		s.a.destroyDialog(r.ctx, s.d.ID(), nil, nil)
	case destroysUsage:
		s.end(r.ctx, End{Cause: EndDropped, Response: res})
	}

	return nil
}

func (s *Subscription) emit(f func(h SubscriptionHandler)) {
	s.st.emit(func() { f(s.sh) })
}

// TS 24.229 clause 5.1.1.3
func refreshLead(d time.Duration) time.Duration {
	if d > 1200*time.Second {
		return 600 * time.Second
	}

	return d / 2
}

// RFC 6665 section 4.1.2.1
func (s *Subscription) granted(ctx context.Context, seconds uint32) {
	if s.ended {
		return
	}

	s.duration = time.Duration(seconds) * time.Second

	if seconds == 0 {
		s.expireNow(ctx)
		return
	}

	s.lead = refreshLead(s.duration)
	s.setExpiry(ctx, s.a.clock.Now().Add(s.duration))
	s.schedule(ctx, s.expiresAt.Add(-s.lead), true)
}

// RFC 6665 section 4.1.3
func (s *Subscription) notifiedExpires(ctx context.Context, seconds uint32) {
	if seconds == 0 {
		s.expireNow(ctx)
		return
	}

	now := s.a.clock.Now()
	d := time.Duration(seconds) * time.Second
	s.duration = d

	if s.lead == 0 {
		s.lead = refreshLead(d)
	}

	s.setExpiry(ctx, now.Add(d))

	at := s.expiresAt.Add(-s.lead)
	if !at.After(now) {
		at = now.Add(d / 2)
	}

	s.schedule(ctx, at, false)
}

func (s *Subscription) setExpiry(ctx context.Context, at time.Time) {
	s.expiresAt = at
	s.expiry.stop()
	s.expiry = s.st.detached(ctx, at.Sub(s.a.clock.Now()), "subscription expiry", func(ctx context.Context) {
		s.end(ctx, End{Cause: EndExpired})
	})
}

func (s *Subscription) schedule(ctx context.Context, at time.Time, force bool) {
	if s.d == nil || s.unsubscribing || s.ended {
		return
	}

	if !force && !s.refreshAt.IsZero() && s.refresh != nil && !s.refresh.stopped && s.refreshAt.Before(at) {
		return
	}

	s.refreshAt = at
	s.refresh.stop()
	s.refresh = s.st.detached(ctx, max(at.Sub(s.a.clock.Now()), 0), "subscription refresh", s.autoRefresh)
}

// RFC 6665 section 4.1.3
func (s *Subscription) expireNow(ctx context.Context) {
	s.refresh.stop()
	s.expiry.stop()
	s.refresh, s.expiry = nil, nil

	if s.guard == nil {
		s.guard = s.st.later(ctx, 64*s.a.layer.T1(), func(ctx context.Context) { s.end(ctx, End{Cause: EndExpired}) })
	}
}

func (s *Subscription) autoRefresh(ctx context.Context) {
	if err := s.subscribe(ctx, false, Change{}, nil); err != nil {
		s.logger().Debug("subscription refresh failed", slog.Any("error", err))
		s.retryRefresh(ctx)
	}
}

func (s *Subscription) retryRefresh(ctx context.Context) {
	if s.unsubscribing || s.ended || s.expiresAt.IsZero() {
		return
	}

	left := s.expiresAt.Sub(s.a.clock.Now())
	if left < 2*64*s.a.layer.T1() {
		return
	}

	s.schedule(ctx, s.a.clock.Now().Add(left/2), true)
}

// RFC 5057 section 4
func (s *Subscription) end(ctx context.Context, e End) {
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

	if s.closing {
		s.closing = false
		e.Cause = EndClosed

		s.a.await(-1)
	}

	s.emit(func(h SubscriptionHandler) {
		if h.Ended != nil {
			h.Ended(ctx, s, e)
		}
	})
}

func (s *Subscription) Event() sip.Event {
	return s.event
}

func (s *Subscription) State() SubscriptionState {
	return get(s.st, func() SubscriptionState { return s.state })
}

func (s *Subscription) Duration() time.Duration {
	return get(s.st, func() time.Duration { return s.duration })
}

func (s *Subscription) Expires() time.Time {
	return get(s.st, func() time.Time { return s.expiresAt })
}

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

func (s *Subscription) Value() any {
	return get(s.st, func() any { return s.value })
}

func (s *Subscription) SetValue(v any) {
	_ = s.st.do(func() error {
		s.value = v
		return nil
	})
}

// RFC 6665 section 4.1.2.2
func (s *Subscription) Refresh(ctx context.Context, c Change, done func(res *sip.Response, err error)) error {
	return s.st.do(func() error { return s.subscribe(ctx, false, c, done) })
}

// RFC 6665 section 4.1.2.3
// RFC 6665 section 4.2.2
func (s *Subscription) Unsubscribe(ctx context.Context, c Change) error {
	return s.st.do(func() error {
		switch {
		case s.ended:
			return ErrState
		case s.unsubscribing:
			return nil
		case s.d == nil:
			s.end(ctx, End{Cause: EndUnsubscribed})
			return nil
		}

		if err := s.subscribe(ctx, true, c, nil); err != nil {
			s.end(ctx, End{Cause: EndUnsubscribed, Err: err})
		}

		return nil
	})
}

// TS 24.229 clause 5.1.1.7
// RFC 6665 section 4.2.2
func (s *Subscription) Drop(ctx context.Context) {
	_ = s.st.do(func() error {
		s.end(ctx, End{Cause: EndDropped})
		return nil
	})
}

func (s *Subscription) subscribe(ctx context.Context, unsubscribe bool, ch Change, done func(*sip.Response, error)) error {
	if s.d == nil || s.ended || s.unsubscribing {
		return ErrState
	}

	c := &subscribeRequest{s: s, change: ch, done: done, unsubscribe: unsubscribe}

	req, err := c.build()
	if err != nil {
		return err
	}

	c.req = req

	if err := c.send(ctx); err != nil {
		return err
	}

	if unsubscribe {
		s.unsubscribing = true
		s.refresh.stop()
		s.refresh = nil
		s.guard.stop()
		s.guard = s.st.later(ctx, 64*s.a.layer.T1(), func(ctx context.Context) { s.end(ctx, End{Cause: EndUnsubscribed}) })
	}

	return nil
}

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

func (c *subscribeRequest) send(ctx context.Context) error {
	s := c.s

	if _, err := s.a.prepareAndSend(ctx, c.req, s.st.client(c.response, c.error)); err != nil {
		return err
	}

	if s.inflight == nil {
		s.inflight = map[*subscribeRequest]struct{}{}
	}

	s.inflight[c] = struct{}{}

	if !c.unsubscribe {
		s.timerN.stop()
		s.timerN, s.nOwner = s.st.later(ctx, 64*s.a.layer.T1(), func(ctx context.Context) { s.end(ctx, End{Cause: EndNoNotify}) }), c
	}

	return nil
}

// RFC 6665 section 4.1.2.2
// RFC 5057 section 5.1 table 2
func refreshEnds(res *sip.Response) effect {
	if res == nil || res.StatusCode == 408 {
		return affectsTransaction
	}

	return failureOf("SUBSCRIBE", res, nil)
}

func (c *subscribeRequest) response(ctx context.Context, res *sip.Response) {
	s := c.s

	if res.IsProvisional() || c.final || s.ended {
		return
	}

	_ = s.d.ReceiveResponse(res)

	if next, ok := c.retry(res); ok {
		c.req = next

		if err := c.send(ctx); err != nil {
			c.error(ctx, err)
		}

		return
	}

	c.final = true
	delete(s.inflight, c)

	switch {
	case res.IsSuccess():
		if res.StatusCode == 204 && s.nOwner == c {
			s.timerN.stop()
			s.timerN, s.nOwner = nil, nil
		}

		if !c.unsubscribe {
			if v, err := res.Header.Expires(); err == nil {
				s.granted(ctx, v)
			} else if v, err := s.att.sub.Header.Expires(); err == nil {
				s.granted(ctx, v)
			}
		}
	case c.unsubscribe:
		s.end(ctx, End{Cause: EndUnsubscribed, Remote: true, Response: res})
	default:
		switch refreshEnds(res) {
		case destroysDialog:
			s.a.destroyDialog(ctx, s.d.ID(), res, nil)
		case destroysUsage:
			s.end(ctx, End{Cause: EndRejected, Remote: true, Response: res})
		default:
			if s.nOwner == c {
				s.timerN.stop()
				s.timerN, s.nOwner = nil, nil
			}

			s.retryRefresh(ctx)
		}
	}

	s.emitDone(c.done, res, nil)
}

// RFC 3261 section 22.2
// RFC 6665 section 4.2.1.1
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

func (c *subscribeRequest) error(ctx context.Context, err error) {
	s := c.s

	if c.final {
		return
	}

	c.final = true
	delete(s.inflight, c)

	switch {
	case s.ended:
	case c.unsubscribe:
		s.end(ctx, End{Cause: EndUnsubscribed, Err: err})
	default:
		if s.nOwner == c {
			s.timerN.stop()
			s.timerN, s.nOwner = nil, nil
		}

		s.retryRefresh(ctx)
	}

	s.emitDone(c.done, nil, err)
}

func (s *Subscription) emitDone(done func(*sip.Response, error), res *sip.Response, err error) {
	if done != nil {
		s.st.emit(func() { done(res, err) })
	}
}

func (s *Subscription) close(ctx context.Context) {
	if s.ended || s.closing {
		return
	}

	if s.d == nil || s.unsubscribing {
		s.end(ctx, End{Cause: EndClosed})
		return
	}

	if err := s.subscribe(ctx, true, Change{}, nil); err != nil {
		s.end(ctx, End{Cause: EndClosed, Err: err})
		return
	}

	s.closing = true
	s.a.await(1)
}
