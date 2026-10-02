package testue

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
)

const DefaultSubscriptionExpires = 600000 * time.Second

type Subscription struct {
	Active  bool
	Expires time.Time
	ID      dialog.ID
}

type subscription struct {
	req     *sip.Request
	d       *dialog.Dialog
	expires time.Time
	timer   *time.Timer
}

func (s *subscription) matches(req *sip.Request) bool {
	id := dialog.RequestID(req)
	if s.d != nil {
		return id == s.d.ID()
	}

	from, err := s.req.Header.From()

	return err == nil && id.CallID == s.req.Header.CallID() && id.LocalTag == from.Tag()
}

func (u *UE) Subscription() Subscription {
	u.mu.Lock()
	defer u.mu.Unlock()

	s := u.sub
	if s == nil || s.d == nil {
		return Subscription{}
	}

	return Subscription{Active: true, Expires: s.expires, ID: s.d.ID()}
}

func (u *UE) Resubscribe(ctx context.Context) error {
	u.op.Lock()
	defer u.op.Unlock()

	return u.resubscribe(ctx)
}

func (u *UE) resubscribe(ctx context.Context) error {
	u.mu.Lock()
	s := u.sub
	u.mu.Unlock()

	if s == nil || s.d == nil {
		return u.subscribe(ctx)
	}

	req, err := s.d.NewRequest("SUBSCRIBE")
	if err != nil {
		return u.subscribe(ctx)
	}

	if err := u.subscribeHeaders(req); err != nil {
		return err
	}

	res, err := u.request(ctx, req)
	if err != nil {
		return err
	}

	switch {
	case res.IsSuccess():
		_ = s.d.ReceiveResponse(res)

		u.mu.Lock()
		defer u.mu.Unlock()

		if u.sub == s {
			u.subscribedLocked(s, res)
		}

		return nil
	case res.StatusCode == 481:
		u.endSubscription(s)
		return u.subscribe(ctx)
	default:
		return &ResponseError{Response: res}
	}
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

	for _, r := range route {
		req.Header.Add("Route", r)
	}

	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<"+impu+">;tag="+sip.NewTag())
	req.Header.Add("To", "<"+impu+">")
	req.Header.Add("Call-ID", timeUUID()+"@"+sip.FormatHost(u.cfg.Local))
	req.Header.Add("CSeq", "1 SUBSCRIBE")

	if err := u.subscribeHeaders(req); err != nil {
		return err
	}

	s := &subscription{req: req}

	u.mu.Lock()
	if old := u.sub; old != nil {
		u.stopSubscriptionLocked(old)
	}

	u.sub = s
	u.mu.Unlock()

	res, err := u.request(ctx, req)
	if err != nil {
		u.endSubscription(s)
		return err
	}

	if !res.IsSuccess() {
		u.endSubscription(s)
		return &ResponseError{Response: res}
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	if u.sub != s {
		return nil
	}

	if s.d == nil {
		d, err := dialog.NewUAC(req, res)
		if err != nil {
			u.stopSubscriptionLocked(s)
			return fmt.Errorf("testue: SUBSCRIBE: %w", err)
		}

		s.d = d
	} else {
		_ = s.d.ReceiveResponse(res)
	}

	u.subscribedLocked(s, res)

	return nil
}

func (u *UE) subscribeHeaders(req *sip.Request) error {
	flow, verify, err := u.requestFlow()
	if err != nil {
		return err
	}

	req.Flow = flow

	via := sip.NewVia(flow.Transport, netip.AddrPortFrom(u.cfg.Local, u.port(!u.cfg.Plain)))
	via.Params.Set("rport", "")

	req.Header.Prepend("Via", via.String())

	contact := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	req.Header.Add("Contact", "<"+contact.String()+">")
	req.Header.Add("Event", "reg")
	req.Header.Add("Accept", regevent.ContentType)
	req.Header.Add("Expires", strconv.FormatInt(int64(DefaultSubscriptionExpires/time.Second), 10))

	if !u.cfg.Plain {
		for _, v := range verify {
			req.Header.Add("Security-Verify", v)
		}

		req.Header.Add("Require", "sec-agree")
		req.Header.Add("Proxy-Require", "sec-agree")
		req.Header.Add("P-Access-Network-Info", u.cfg.AccessNetworkInfo)
	}

	return nil
}

func (u *UE) requestFlow() (sip.Flow, []string, error) {
	if u.cfg.Plain {
		return u.unprotectedFlow(), nil, nil
	}

	est := u.established()
	if est == nil {
		return sip.Flow{}, nil, fmt.Errorf("%w: no established SAs", ErrNotRegistered)
	}

	return protectedFlow(est, u.cfg.Transport), est.server, nil
}

func (u *UE) subscribedLocked(s *subscription, res *sip.Response) {
	if s.expires.IsZero() || res.Header.Has("Expires") {
		if v, err := res.Header.Expires(); err == nil {
			s.expires = time.Now().Add(time.Duration(v) * time.Second)
		} else if s.expires.IsZero() {
			s.expires = time.Now().Add(DefaultSubscriptionExpires)
		}
	}

	u.scheduleRefreshLocked(s)
}

func (u *UE) scheduleRefreshLocked(s *subscription) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}

	if u.closed || s.d == nil {
		return
	}

	s.timer = time.AfterFunc(reregisterIn(time.Until(s.expires)), func() {
		err := u.Resubscribe(context.Background())
		if err != nil && !errors.Is(err, ErrNotRegistered) && !errors.Is(err, ErrClosed) && !errors.Is(err, transaction.ErrClosed) {
			u.event(Event{Err: err})
		}
	})
}

func (u *UE) endSubscription(s *subscription) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.sub == s {
		u.stopSubscriptionLocked(s)
	}
}

func (u *UE) stopSubscriptionLocked(s *subscription) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}

	if s.d != nil {
		s.d.Terminate()
	}

	if u.sub == s {
		u.sub = nil
	}
}

func (u *UE) notify(tx *transaction.ServerTransaction, req *sip.Request) {
	event, _, err := sip.ParseTokenParams(req.Header.Get("Event"))
	if err != nil || !strings.EqualFold(event, "reg") {
		u.event(Event{Request: req})
		_ = tx.Respond(sip.NewResponse(req, 489, ""))

		return
	}

	subState, params, err := sip.ParseTokenParams(req.Header.Get("Subscription-State"))
	if err != nil {
		u.event(Event{Request: req})
		_ = tx.Respond(sip.NewResponse(req, 400, "Bad Subscription-State"))

		return
	}

	terminated := strings.EqualFold(subState, "terminated")

	u.mu.Lock()

	s := u.sub
	if s == nil || !s.matches(req) {
		u.mu.Unlock()
		u.event(Event{Request: req})
		_ = tx.Respond(sip.NewResponse(req, 481, ""))

		return
	}

	if s.d == nil && !terminated {
		d, err := dialog.NewFromNotify(s.req, req)
		if err != nil {
			u.mu.Unlock()
			u.event(Event{Request: req})
			_ = tx.Respond(sip.NewResponse(req, 400, ""))

			return
		}

		s.d = d
	} else if s.d != nil {
		if err := s.d.ReceiveRequest(req); err != nil {
			u.mu.Unlock()
			u.event(Event{Request: req})

			code := 400
			if serr, ok := errors.AsType[*sip.StatusError](err); ok {
				code = serr.StatusCode
			}

			_ = tx.Respond(sip.NewResponse(req, code, ""))

			return
		}
	}

	if v, ok := params.Get("expires"); ok && !terminated {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			s.expires = time.Now().Add(time.Duration(n) * time.Second)
			u.scheduleRefreshLocked(s)
		}
	}

	if terminated {
		u.stopSubscriptionLocked(s)
	}

	var (
		info    regevent.Reginfo
		infoErr error
	)

	if mediaType(req.Header.ContentType()) == regevent.ContentType {
		info, infoErr = regevent.Decode(req.Body)
	} else {
		infoErr = fmt.Errorf("testue: NOTIFY body of type %q", req.Header.ContentType())
	}

	var a action
	if infoErr == nil {
		a = u.applyLocked(info, terminated)
	}

	if terminated && !a.teardown {
		reason, _ := params.Get("reason")
		a.resubscribe = u.state.Registered && (reason == "deactivated" || reason == "timeout")
	}

	u.mu.Unlock()

	e := Event{Request: req, Err: infoErr}
	if infoErr == nil {
		e.Reginfo = &info
	}

	u.event(e)

	_ = tx.Respond(sip.NewResponse(req, 200, ""))

	if a.teardown || a.resubscribe {
		go u.act(tx, a)
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

	return ok && a.Unmap() == u.cfg.Local.Unmap() && uri.Port == u.port(!u.cfg.Plain)
}

func (u *UE) applyLocked(info regevent.Reginfo, subTerminated bool) action {
	var (
		shortened             *uint32
		mine, gone            int
		deactivated, rejected bool
		registered, removed   []string
		allClosed             = len(info.Registrations) > 0
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
		u.forgetLocked()

		return action{teardown: true, reregister: deactivated && !rejected}
	}

	if !u.state.Registered {
		return action{}
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

	return action{}
}

func (u *UE) forgetLocked() {
	u.stopTimersLocked()
	u.state = State{}
	u.auth = ""

	if u.sub != nil {
		u.stopSubscriptionLocked(u.sub)
	}
}

func (u *UE) act(tx *transaction.ServerTransaction, a action) {
	if a.teardown {
		<-tx.Done()

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
