package testue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

const DefaultSubscriptionExpires = 600000 * time.Second

var ErrNoNotify = errors.New("testue: no NOTIFY within Timer N")

type Subscription struct {
	Active  bool
	Expires time.Time
	ID      dialog.ID
}

// subscription is the reg event subscription of the UE (TS 24.229 §5.1.1.3), on a ua.Subscription.
//
// u.sub is set before the SUBSCRIBE goes, s once Subscribe returned: both under u.mu, which the agent is never
// called with.
type subscription struct {
	s *ua.Subscription

	// accepted receives the final response to the initial SUBSCRIBE, or the error that ended it first; dropped marks
	// a subscription the UE forgot before Subscribe returned.
	accepted chan error
	dropped  bool

	version     uint64
	haveVersion bool
}

func (u *UE) Subscription() Subscription {
	u.mu.Lock()

	var s *ua.Subscription
	if u.sub != nil {
		s = u.sub.s
	}

	u.mu.Unlock()

	if s == nil || s.State() == ua.SubscriptionTerminated {
		return Subscription{}
	}

	out := Subscription{Active: true, Expires: s.Expires()}

	if d, ok := s.Dialog(); ok {
		out.ID = d.ID
	}

	return out
}

// current is the subscription of the UE, once Subscribe returned it.
func (u *UE) current() (*subscription, *ua.Subscription) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.sub == nil {
		return nil, nil
	}

	return u.sub, u.sub.s
}

func (u *UE) Resubscribe(ctx context.Context) error {
	u.op.Lock()
	defer u.op.Unlock()

	return u.resubscribe(ctx)
}

// resubscribe refreshes the subscription, or makes a new one without it, or when the refresh ends it (RFC 6665
// §4.1.2.2).
func (u *UE) resubscribe(ctx context.Context) error {
	_, s := u.current()
	if s == nil {
		return u.subscribe(ctx)
	}

	if _, ok := s.Dialog(); !ok || s.State() == ua.SubscriptionTerminated {
		return u.subscribe(ctx)
	}

	done := make(chan result, 1)

	if err := s.Refresh(ua.Change{}, func(res *sip.Response, err error) { done <- result{res, err} }); err != nil {
		return u.subscribe(ctx)
	}

	select {
	case r := <-done:
		switch {
		case r.err != nil:
			return fmt.Errorf("testue: SUBSCRIBE: %w", r.err)
		case r.res.IsSuccess():
			return nil
		case s.State() == ua.SubscriptionTerminated:
			return u.subscribe(ctx)
		}

		return &ResponseError{Response: r.res}
	case <-ctx.Done():
		return ctx.Err()
	}
}

type result struct {
	res *sip.Response
	err error
}

func (u *UE) subscribe(ctx context.Context) error {
	u.mu.Lock()
	registered, impu, route := u.state.Registered, u.state.DefaultIMPU, slices.Clone(u.state.ServiceRoute)
	u.mu.Unlock()

	if !registered || impu == "" {
		return ErrNotRegistered
	}

	target, err := sip.ParseURI(impu)
	if err != nil {
		return fmt.Errorf("testue: default IMPU: %w", err)
	}

	req := sip.NewRequest("SUBSCRIBE", target)

	preloaded, err := u.outboundRoute()
	if err != nil {
		return err
	}

	req.Header.Add("Route", "<"+preloaded.String()+">")

	for _, r := range route {
		req.Header.Add("Route", r)
	}

	contact := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	req.Header.Add("From", "<"+impu+">")
	req.Header.Add("To", "<"+impu+">")
	req.Header.Add("Call-ID", timeUUID()+"@"+sip.FormatHost(u.cfg.Local))
	req.Header.Add("Contact", "<"+contact.String()+">")
	req.Header.Add("Event", "reg")
	req.Header.Add("Accept", regevent.ContentType)
	req.Header.Add("Expires", strconv.FormatInt(int64(DefaultSubscriptionExpires/time.Second), 10))

	sub := &subscription{accepted: make(chan error, 1)}

	u.mu.Lock()

	var old *ua.Subscription
	if u.sub != nil {
		old = u.sub.s
	}

	u.sub = sub
	u.mu.Unlock()

	if old != nil {
		_ = old.Unsubscribe(ua.Change{})
	}

	s, err := u.agent.Subscribe(req, u.regEventHandler(sub))
	if err != nil {
		u.endSubscription(sub)
		return fmt.Errorf("testue: SUBSCRIBE: %w", err)
	}

	u.mu.Lock()
	sub.s = s
	dropped := sub.dropped
	u.mu.Unlock()

	if dropped {
		s.Drop()
	}

	select {
	case err := <-sub.accepted:
		if err != nil {
			u.endSubscription(sub)
		}

		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// outboundRoute is the Route to the P-CSCF the UE preloads in its initial requests (TS 24.229 §5.1.2A.1.1), on the
// flow of its transport.
func (u *UE) outboundRoute() (sip.URI, error) {
	flow, _, err := u.requestFlow(u.cfg.Transport)
	if err != nil {
		return sip.URI{}, err
	}

	return preloadedRoute(flow), nil
}

func preloadedRoute(f sip.Flow) sip.URI {
	u := sip.URI{Scheme: "sip", Host: sip.FormatHost(f.Remote.Addr()), Port: f.Remote.Port()}
	if f.Transport != sip.UDP {
		u.Params.Set("transport", strings.ToLower(string(f.Transport)))
	}

	u.Params.Set("lr", "")

	return u
}

func (u *UE) requestFlow(tr sip.Transport) (sip.Flow, []string, error) {
	if u.cfg.Plain {
		f := u.unprotectedFlow()
		f.Transport = tr

		return f, nil, nil
	}

	est := u.established()
	if est == nil {
		return sip.Flow{}, nil, fmt.Errorf("%w: no established SAs", ErrNotRegistered)
	}

	return protectedFlow(est, tr), est.server, nil
}

func (u *UE) endSubscription(sub *subscription) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.sub == sub {
		u.sub = nil
	}
}

// stopSubscriptionLocked forgets the subscription of the UE, with its registration: no SIP procedure, the network
// ended it too (TS 24.229 §5.1.1.7 NOTE 3, NOTE 4); the agent drops it once the lock is released.
func (u *UE) stopSubscriptionLocked() func() {
	sub := u.sub
	u.sub = nil

	switch {
	case sub == nil:
		return func() {}
	case sub.s == nil:
		sub.dropped = true
		return func() {}
	}

	s := sub.s

	return s.Drop
}

// regEventHandler handles the events of the reg event subscription sub (RFC 6665, RFC 3680).
func (u *UE) regEventHandler(sub *subscription) ua.SubscriptionHandler {
	return ua.SubscriptionHandler{
		Accepted: func(*ua.Subscription, *sip.Response) {
			select {
			case sub.accepted <- nil:
			default:
			}
		},
		Notify: func(_ *ua.Subscription, r *ua.Request) { u.notify(sub, r) },
		Ended: func(s *ua.Subscription, e ua.End) {
			if e.Cause == ua.EndRejected && e.Response != nil {
				select {
				case sub.accepted <- &ResponseError{Response: e.Response}:
				default:
				}
			} else {
				select {
				case sub.accepted <- fmt.Errorf("testue: SUBSCRIBE: subscription %s", e.Cause):
				default:
				}
			}

			u.mu.Lock()

			current := u.sub == sub
			if current {
				u.sub = nil
			}

			// RFC 6665 §4.1.3: a subscription deactivated, or timed out, is made again at once.
			reason := e.State.Reason
			resubscribe := e.Cause == ua.EndTerminated && current && u.state.Registered &&
				(reason == sip.SubscriptionDeactivated || reason == sip.SubscriptionTimeout)

			u.mu.Unlock()

			switch {
			case e.Cause == ua.EndNoNotify && current:
				u.event(Event{Err: ErrNoNotify})
			case resubscribe:
				go u.act(action{resubscribe: true}, nil)
			}
		},
	}
}

// notify handles a NOTIFY of the reg event subscription, which the agent answered (RFC 3680 §5).
func (u *UE) notify(sub *subscription, r *ua.Request) {
	req := r.Message()
	ss, _, _ := req.Header.SubscriptionState()
	terminated := ss.State == sip.SubscriptionTerminated

	var (
		info    regevent.Reginfo
		infoErr error
	)

	if mediaType(req.Header.ContentType()) == regevent.ContentType {
		info, infoErr = regevent.Decode(req.Body)
	} else {
		infoErr = fmt.Errorf("testue: NOTIFY body of type %q", req.Header.ContentType())
	}

	var (
		a     action
		stale bool
		after []func()
	)

	u.mu.Lock()

	if infoErr == nil {
		stale = sub.haveVersion && info.Version <= sub.version
		gap := sub.haveVersion && info.Version > sub.version+1

		if !stale {
			sub.version, sub.haveVersion = info.Version, true
			a, after = u.applyLocked(info, terminated)
			a.resubscribe = gap && !terminated && !a.teardown
		}
	}

	u.mu.Unlock()

	for _, f := range after {
		f()
	}

	e := Event{Request: req, Err: infoErr}
	if infoErr == nil && !stale {
		e.Reginfo = &info
	}

	u.event(e)

	if a.teardown || a.resubscribe {
		go u.act(a, r.Done())
	}
}

func mediaType(ct string) string {
	mt, _, _ := strings.Cut(ct, ";")

	return strings.ToLower(strings.TrimSpace(mt))
}

type action struct {
	teardown    bool
	reregister  bool
	resubscribe bool
}

func (u *UE) ownContact(c regevent.Contact) bool {
	uri, err := sip.ParseURI(c.URI)
	if err != nil || uri.User != u.user {
		return false
	}

	a, ok := uri.Addr()
	if !ok || a.Unmap() != u.cfg.Local.Unmap() || uri.Port != u.port(!u.cfg.Plain) {
		return false
	}

	var params sip.Params
	for _, p := range c.UnknownParams {
		params = append(params, sip.Param{Name: p.Name, Value: p.Value})
	}

	return u.ownFlow(params)
}

func (u *UE) applyLocked(info regevent.Reginfo, subTerminated bool) (action, []func()) {
	var (
		shortened             *uint32
		mine, gone            int
		deactivated, rejected bool
		registered, removed   []string
		allClosed             = info.State == regevent.Full && len(info.Registrations) > 0
	)

	for _, reg := range info.Registrations {
		allClosed = allClosed && reg.State == regevent.Terminated

		own, ended := 0, 0

		for _, c := range reg.Contacts {
			if !u.ownContact(c) {
				continue
			}

			own++

			if c.State == regevent.Terminated {
				ended++
				deactivated = deactivated || c.Event == regevent.Deactivated
				rejected = rejected || c.Event == regevent.Rejected

				continue
			}

			if c.Event == regevent.Shortened && reg.State == regevent.Active && c.Expires != nil &&
				(shortened == nil || *c.Expires < *shortened) {
				shortened = c.Expires
			}
		}

		if own == 0 {
			continue
		}

		mine++

		switch {
		case ended == own:
			gone++

			removed = append(removed, reg.AOR)
		case reg.State == regevent.Active:
			registered = append(registered, reg.AOR)
		}
	}

	if (mine > 0 && gone == mine) || (allClosed && subTerminated) {
		after := u.forgetLocked()

		return action{teardown: true, reregister: deactivated && !rejected}, []func(){after}
	}

	if !u.state.Registered {
		return action{}, nil
	}

	if info.State == regevent.Full {
		u.state.IMPUs = nil
	}

	for _, aor := range registered {
		if !slices.ContainsFunc(u.state.IMPUs, func(x string) bool { return sameURI(x, aor) }) {
			u.state.IMPUs = append(u.state.IMPUs, aor)
		}
	}

	for _, aor := range removed {
		drop := func(x string) bool { return sameURI(x, aor) }

		u.state.IMPUs = slices.DeleteFunc(u.state.IMPUs, drop)
		u.state.AssociatedURIs = slices.DeleteFunc(u.state.AssociatedURIs, drop)

		if drop(u.state.DefaultIMPU) {
			u.state.DefaultIMPU = ""
			if len(u.state.AssociatedURIs) > 0 {
				u.state.DefaultIMPU = u.state.AssociatedURIs[0]
			}
		}
	}

	if shortened != nil {
		remaining := time.Duration(*shortened) * time.Second
		if expires := time.Now().Add(remaining); expires.Before(u.state.Expires) {
			u.state.Expires = expires
			u.scheduleLocked(remaining)
		}
	}

	return action{}, nil
}

// forgetLocked forgets the registration; the subscription it returns to unsubscribe from goes once the lock is
// released.
func (u *UE) forgetLocked() func() {
	u.stopTimersLocked()
	u.state = State{}
	u.auth = ""

	return u.stopSubscriptionLocked()
}

// act tears the registration down, once the NOTIFY that told it is answered, over the SAs it deletes; then registers
// or subscribes again.
func (u *UE) act(a action, answered <-chan struct{}) {
	if a.teardown {
		if answered != nil {
			<-answered
		}

		u.op.Lock()
		u.mu.Lock()

		if !u.state.Registered {
			for _, s := range slices.Clone(u.sets) {
				u.dropLocked(s)
			}
		}

		u.mu.Unlock()
		u.op.Unlock()
	}

	var err error

	switch {
	case a.teardown && a.reregister:
		err = u.Register(context.Background())
	case a.resubscribe:
		err = u.Resubscribe(context.Background())
	}

	if err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, transaction.ErrClosed) {
		u.event(Event{Err: err})
	}
}
