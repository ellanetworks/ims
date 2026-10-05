package pcscf

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	subscriptionExpires = 600000 * time.Second

	refreshAhead     = 600 * time.Second
	refreshAheadFrom = 1200 * time.Second
	minRefresh       = time.Second
)

type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) transaction.Timer
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) transaction.Timer {
	return time.AfterFunc(d, f)
}

type subscription struct {
	impi      string
	impu      string
	callID    string
	localTag  string
	subscribe *sip.Request
	dialog    *dialog.Dialog
	version   int64
	expires   time.Time
	timer     transaction.Timer
	removed   bool
}

type subscriptions struct {
	p *PCSCF

	mu     sync.Mutex
	closed bool
	byIMPI map[string]*subscription
}

func newSubscriptions(p *PCSCF) *subscriptions {
	return &subscriptions{p: p, byIMPI: make(map[string]*subscription)}
}

func (ss *subscriptions) restore(ctx context.Context) error {
	store := ss.p.cfg.Registrations
	if store == nil {
		return nil
	}

	subs, err := store.ListPCSCFSubscriptions(ctx)
	if err != nil {
		return err
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()

	now := ss.p.clock.Now()

	for _, rec := range subs {
		d, err := restoreDialog(rec.Dialog)
		if err != nil || !rec.ExpiresAt.After(now) {
			if err := store.DeletePCSCFSubscription(ctx, rec.IMPI); err != nil && !errors.Is(err, db.ErrNotFound) {
				return err
			}

			continue
		}

		s := &subscription{
			impi: rec.IMPI, impu: rec.IMPU, callID: rec.CallID, localTag: rec.LocalTag,
			dialog: d, version: rec.Version, expires: rec.ExpiresAt,
		}
		ss.byIMPI[s.impi] = s
		ss.schedule(s)
	}

	return nil
}

func restoreDialog(b []byte) (*dialog.Dialog, error) {
	if len(b) == 0 {
		return nil, errors.New("no dialog")
	}

	var snap dialog.Snapshot

	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}

	return dialog.Restore(snap)
}

func (ss *subscriptions) close() {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.closed = true

	for _, s := range ss.byIMPI {
		if s.timer != nil {
			s.timer.Stop()
		}
	}
}

func (ss *subscriptions) has(impi string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	_, ok := ss.byIMPI[impi]

	return ok
}

func (ss *subscriptions) start(impi string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if _, ok := ss.byIMPI[impi]; ok || ss.closed {
		return
	}

	reg, ok := ss.p.anyRegistration(impi)
	if !ok {
		return
	}

	impu := defaultIdentity(reg.AssociatedURIs)

	target, err := sip.ParseURI(impu)
	if err != nil {
		ss.p.log.Debug("no identity to subscribe to", slog.String("impi", impi), slog.Any("error", err))
		return
	}

	local := netip.AddrPortFrom(reg.PCSCFAddress, ss.p.cfg.Port)
	contact := ss.p.contactURI(local)

	s := &subscription{
		impi: impi, impu: impu, callID: sip.NewTag() + "@" + sip.FormatHost(local.Addr()), localTag: sip.NewTag(),
		version: -1,
	}

	req := sip.NewRequest("SUBSCRIBE", target)
	req.Header = nil
	req.Header.Add("Via", sip.NewVia(sip.UDP, local).String())
	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("To", "<"+impu+">")
	req.Header.Add("From", contact+";tag="+s.localTag)
	req.Header.Add("Call-ID", s.callID)
	req.Header.Add("CSeq", "1 SUBSCRIBE")
	req.Header.Add("Contact", contact)
	ss.p.subscribeHeaders(req, reg)
	req.Header.Add("Content-Length", "0")
	req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: netip.AddrPortFrom(local.Addr(), ss.p.cfg.ICSCFPort)}

	s.subscribe = req
	ss.byIMPI[impi] = s

	ss.p.log.Debug("subscribing to reg event", slog.String("impi", impi), slog.String("impu", impu))

	if _, err := ss.p.cfg.Layer.Request(req, &subscribeTransaction{ss: ss, s: s, req: req}); err != nil {
		ss.p.log.Warn("SUBSCRIBE failed", slog.String("impi", impi), slog.Any("error", err))
		ss.removeLocked(s)
	}
}

func (p *PCSCF) contactURI(local netip.AddrPort) string {
	u := sip.URI{Scheme: "sip", Host: sip.FormatHost(local.Addr().Unmap()), Port: local.Port()}

	return "<" + u.String() + ">"
}

func (p *PCSCF) subscribeHeaders(req *sip.Request, reg db.PCSCFRegistration) {
	req.Header.Add("P-Asserted-Identity", "<"+pathURI(reg.FlowToken, reg.PCSCFAddress, p.cfg.Port, false).String()+">")
	req.Header.Add("Event", "reg")
	req.Header.Add("Accept", regevent.ContentType)
	req.Header.Add("Expires", strconv.Itoa(int(subscriptionExpires/time.Second)))
}

type subscribeTransaction struct {
	ss  *subscriptions
	s   *subscription
	req *sip.Request
}

func (t *subscribeTransaction) HandleResponse(res *sip.Response) {
	if res.StatusCode >= 200 {
		t.ss.answered(t.s, t.req, res)
	}
}

func (t *subscribeTransaction) HandleError(err error) {
	t.ss.p.log.Warn("SUBSCRIBE failed", slog.String("impi", t.s.impi), slog.Any("error", err))
	t.ss.failed(t.s, t.req, 408)
}

func (ss *subscriptions) answered(s *subscription, req *sip.Request, res *sip.Response) {
	if !res.IsSuccess() {
		ss.p.log.Warn("SUBSCRIBE refused", slog.String("impi", s.impi), slog.Int("code", res.StatusCode))
		ss.failed(s, req, res.StatusCode)

		return
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()

	if s.removed {
		return
	}

	if s.dialog == nil {
		d, err := dialog.NewUAC(req, res)
		if err != nil {
			ss.p.log.Warn("SUBSCRIBE answered without a dialog", slog.String("impi", s.impi), slog.Any("error", err))
			ss.removeLocked(s)

			return
		}

		s.dialog = d
	} else if err := s.dialog.ReceiveResponse(res); err != nil {
		ss.p.log.Debug("SUBSCRIBE response outside its dialog", slog.String("impi", s.impi), slog.Any("error", err))
	}

	if v, err := res.Header.Expires(); err == nil {
		s.expires = ss.p.clock.Now().Add(time.Duration(v) * time.Second)
	}

	ss.saveLocked(s)
	ss.schedule(s)
}

func (ss *subscriptions) failed(s *subscription, req *sip.Request, code int) {
	ss.mu.Lock()

	if s.removed {
		ss.mu.Unlock()
		return
	}

	initial := !req.Header.Has("To") || toTag(req) == ""

	switch {
	case initial:
		ss.removeLocked(s)
	case code == 481:
		ss.removeLocked(s)
		ss.mu.Unlock()
		ss.start(s.impi)

		return
	case !s.expires.After(ss.p.clock.Now()):
		ss.removeLocked(s)
		ss.mu.Unlock()
		ss.start(s.impi)

		return
	default:
		ss.schedule(s)
	}

	ss.mu.Unlock()
}

func (ss *subscriptions) stop(impi string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if s, ok := ss.byIMPI[impi]; ok {
		ss.removeLocked(s)
	}
}

func toTag(req *sip.Request) string {
	to, err := req.Header.To()
	if err != nil {
		return ""
	}

	return to.Tag()
}

func (ss *subscriptions) schedule(s *subscription) {
	if s.timer != nil {
		s.timer.Stop()
	}

	if ss.closed || s.expires.IsZero() {
		return
	}

	left := s.expires.Sub(ss.p.clock.Now())

	at := left / 2
	if left > refreshAheadFrom {
		at = left - refreshAhead
	}

	s.timer = ss.p.clock.AfterFunc(max(at, minRefresh), func() { ss.refresh(s) })
}

func (ss *subscriptions) refresh(s *subscription) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if s.removed || ss.closed {
		return
	}

	now := ss.p.clock.Now()

	reg, ok := ss.p.anyRegistration(s.impi)
	if !ok || s.dialog == nil {
		if !s.expires.After(now) {
			ss.removeLocked(s)
			return
		}

		s.timer = ss.p.clock.AfterFunc(s.expires.Sub(now), func() { ss.refresh(s) })

		return
	}

	req, err := s.dialog.NewRequest("SUBSCRIBE")
	if err != nil {
		ss.removeLocked(s)
		return
	}

	tr, dest, err := sip.NextHop(req)
	if err != nil {
		ss.p.log.Warn("no destination for the re-SUBSCRIBE", slog.String("impi", s.impi), slog.Any("error", err))
		ss.removeLocked(s)

		return
	}

	local := netip.AddrPortFrom(reg.PCSCFAddress, ss.p.cfg.Port)
	req.Header.Prepend("Via", sip.NewVia(tr, local).String())
	req.Header.Add("Contact", ss.p.contactURI(local))
	ss.p.subscribeHeaders(req, reg)
	req.Flow = sip.Flow{Transport: tr, Local: local, Remote: dest}

	ss.saveLocked(s)

	if _, err := ss.p.cfg.Layer.Request(req, &subscribeTransaction{ss: ss, s: s, req: req}); err != nil {
		ss.p.log.Warn("re-SUBSCRIBE failed", slog.String("impi", s.impi), slog.Any("error", err))
		ss.schedule(s)
	}
}

func (ss *subscriptions) removeLocked(s *subscription) {
	if s.removed {
		return
	}

	s.removed = true

	if s.timer != nil {
		s.timer.Stop()
	}

	if ss.byIMPI[s.impi] == s {
		delete(ss.byIMPI, s.impi)
	}

	if store := ss.p.cfg.Registrations; store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		if err := store.DeletePCSCFSubscription(ctx, s.impi); err != nil && !errors.Is(err, db.ErrNotFound) {
			ss.p.log.Error("deleting the subscription failed", slog.String("impi", s.impi), slog.Any("error", err))
		}

		cancel()
	}
}

func (ss *subscriptions) saveLocked(s *subscription) {
	store := ss.p.cfg.Registrations
	if store == nil || s.dialog == nil {
		return
	}

	snap, err := json.Marshal(s.dialog.Snapshot())
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	if _, err := store.SavePCSCFSubscription(ctx, db.PCSCFSubscription{
		IMPI: s.impi, IMPU: s.impu, CallID: s.callID, LocalTag: s.localTag, Dialog: snap, Version: s.version,
		ExpiresAt: s.expires,
	}); err != nil {
		ss.p.log.Error("saving the subscription failed", slog.String("impi", s.impi), slog.Any("error", err))
	}
}

func (ss *subscriptions) match(req *sip.Request) *subscription {
	id := dialog.RequestID(req)

	ss.mu.Lock()
	defer ss.mu.Unlock()

	for _, s := range ss.byIMPI {
		if s.callID == id.CallID && s.localTag == id.LocalTag {
			return s
		}
	}

	return nil
}

func (ss *subscriptions) notified(s *subscription, req *sip.Request) *sip.Response {
	state, params, err := sip.ParseTokenParams(req.Header.Get("Subscription-State"))
	if err != nil {
		return sip.NewResponse(req, 400, "Bad Subscription-State")
	}

	terminated := strings.EqualFold(state, "terminated")

	var info *regevent.Reginfo

	if len(req.Body) > 0 {
		if !strings.EqualFold(mediaType(req.Header.ContentType()), regevent.ContentType) {
			return sip.NewResponse(req, 415, "")
		}

		i, err := regevent.Decode(req.Body)
		if err != nil {
			return sip.NewResponse(req, 400, "Bad reginfo")
		}

		info = &i
	}

	ss.mu.Lock()

	if s.removed {
		ss.mu.Unlock()
		return sip.NewResponse(req, 481, "")
	}

	switch {
	case s.dialog != nil:
		if err := s.dialog.ReceiveRequest(req); err != nil {
			ss.mu.Unlock()

			code := 400
			if serr, ok := errors.AsType[*sip.StatusError](err); ok {
				code = serr.StatusCode
			}

			return sip.NewResponse(req, code, "")
		}
	case s.subscribe != nil:
		d, err := dialog.NewFromNotify(s.subscribe, req)
		if err != nil && !terminated {
			ss.mu.Unlock()
			return sip.NewResponse(req, 481, "")
		}

		s.dialog = d
	}

	if info != nil && int64(info.Version) < s.version {
		info = nil
	}

	if info != nil {
		s.version = int64(info.Version)
	}

	if terminated {
		ss.removeLocked(s)
	} else {
		if v, ok := params.Get("expires"); ok {
			if n, err := strconv.ParseUint(v, 10, 32); err == nil {
				s.expires = ss.p.clock.Now().Add(time.Duration(n) * time.Second)
			}
		}

		ss.saveLocked(s)
		ss.schedule(s)
	}

	ss.mu.Unlock()

	if info != nil {
		ss.p.apply(s.impi, *info)
	}

	return sip.NewResponse(req, 200, "")
}

func mediaType(ct string) string {
	mt, _, _ := strings.Cut(ct, ";")

	return strings.TrimSpace(mt)
}

func (p *PCSCF) apply(impi string, info regevent.Reginfo) {
	now := p.clock.Now()
	ended := map[netip.Addr]regevent.Event{}

	removed := p.regs.update(impi, func(r *db.PCSCFRegistration) bool {
		active := map[string]bool{}
		mentioned := map[string]bool{}

		var (
			longest time.Duration
			unknown bool
		)

		for _, reg := range info.Registrations {
			here := false

			for _, c := range reg.Contacts {
				uri, ok := matchURI(r.Contacts, c.URI)
				if !ok {
					if c.State == regevent.Active && reg.State != regevent.Terminated && contactAt(c.URI, r.UEAddress.Addr()) {
						here, unknown = true, true
					}

					continue
				}

				mentioned[uri] = true

				if c.State != regevent.Active || reg.State == regevent.Terminated {
					ended[r.UEAddress.Addr()] = graver(ended[r.UEAddress.Addr()], c.Event)
					continue
				}

				active[uri], here = true, true

				if c.Expires != nil {
					longest = max(longest, time.Duration(*c.Expires)*time.Second)
				}
			}

			aor, listed := matchURI(r.AssociatedURIs, reg.AOR)

			switch {
			case here && !listed:
				r.AssociatedURIs = append(r.AssociatedURIs, reg.AOR)
			case !here && listed:
				r.AssociatedURIs = slices.DeleteFunc(r.AssociatedURIs, func(a string) bool { return a == aor })

				for k, set := range r.Sets {
					if set = slices.DeleteFunc(slices.Clone(set), func(a string) bool { return a == aor }); len(set) > 0 {
						r.Sets[k] = set
					} else {
						delete(r.Sets, k)
					}
				}
			}
		}

		r.Contacts = slices.DeleteFunc(r.Contacts, func(c string) bool { return mentioned[c] && !active[c] })

		if longest > 0 {
			r.ExpiresAt = now.Add(longest)
		}

		return (len(r.Contacts) > 0 || unknown) && len(r.AssociatedURIs) > 0
	})

	for _, r := range removed {
		p.log.Debug("registration ended by the network", slog.String("impi", impi), slog.String("ue", r.UEAddress.Addr().String()))

		if p.sas != nil {
			p.sas.deregistered(impi, r.UEAddress.Addr())
		}

		p.endPolicy(r, terminationCause(ended[r.UEAddress.Addr()]), 0)
	}

	if len(removed) > 0 {
		p.unsubscribeIfIdle(impi)
	}
}

func contactAt(contact string, ue netip.Addr) bool {
	u, err := sip.ParseURI(contact)
	if err != nil {
		return false
	}

	a, ok := u.Addr()

	return ok && a.Unmap() == ue.Unmap()
}

func matchURI(list []string, s string) (string, bool) {
	u, err := sip.ParseURI(s)

	for _, l := range list {
		if l == s {
			return l, true
		}

		if v, verr := sip.ParseURI(l); err == nil && verr == nil && v.Equivalent(u) {
			return l, true
		}
	}

	return "", false
}
