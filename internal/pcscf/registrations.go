package pcscf

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type RegistrationStore interface {
	SavePCSCFRegistration(context.Context, db.PCSCFRegistration) (db.PCSCFRegistration, error)
	DeletePCSCFRegistration(context.Context, int64) error
	ListPCSCFRegistrations(context.Context) ([]db.PCSCFRegistration, error)
	SavePCSCFSubscription(context.Context, db.PCSCFSubscription) (db.PCSCFSubscription, error)
	DeletePCSCFSubscription(context.Context, string) error
	ListPCSCFSubscriptions(context.Context) ([]db.PCSCFSubscription, error)
}

type regKey struct {
	impi string
	ue   netip.Addr
}

type flow struct {
	impi      string
	transport sip.Transport
	protected bool
	ue        netip.AddrPort
	local     netip.Addr
}

type retired struct {
	f       flow
	token   string
	expires time.Time
}

const pendingToken = DefaultAwaitAuth

type registrations struct {
	store RegistrationStore
	log   *slog.Logger
	clock Clock
	grace time.Duration

	expired func(db.PCSCFRegistration)

	lost atomic.Int64

	mu       sync.Mutex
	closed   bool
	byKey    map[regKey]*db.PCSCFRegistration
	byToken  map[string]*db.PCSCFRegistration
	bySource map[netip.AddrPort]map[*db.PCSCFRegistration]struct{}
	timers   map[regKey]transaction.Timer
	retired  map[string]retired
	pending  map[regKey]retired
}

func newRegistrations(store RegistrationStore, clock Clock, grace time.Duration, logger *slog.Logger) *registrations {
	return &registrations{
		store:    store,
		log:      logger,
		clock:    clock,
		grace:    grace,
		byKey:    make(map[regKey]*db.PCSCFRegistration),
		byToken:  make(map[string]*db.PCSCFRegistration),
		bySource: make(map[netip.AddrPort]map[*db.PCSCFRegistration]struct{}),
		timers:   make(map[regKey]transaction.Timer),
		retired:  make(map[string]retired),
		pending:  make(map[regKey]retired),
	}
}

func (rs *registrations) restore(ctx context.Context) ([]db.PCSCFRegistration, error) {
	if rs.store == nil {
		return nil, nil
	}

	regs, err := rs.store.ListPCSCFRegistrations(ctx)
	if err != nil {
		return nil, err
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()

	now := rs.clock.Now()

	var expired []db.PCSCFRegistration

	for _, r := range regs {
		if !r.ExpiresAt.After(now) {
			if err := rs.store.DeletePCSCFRegistration(ctx, r.ID); err != nil {
				return nil, err
			}

			expired = append(expired, r)

			continue
		}

		rs.add(&r)
	}

	return expired, nil
}

func clone(r *db.PCSCFRegistration) db.PCSCFRegistration {
	c := *r
	c.Contacts = slices.Clone(r.Contacts)
	c.AssociatedURIs = slices.Clone(r.AssociatedURIs)
	c.ServiceRoute = slices.Clone(r.ServiceRoute)
	c.RxClass = slices.Clone(r.RxClass)

	if r.Sets != nil {
		c.Sets = make(map[string][]string, len(r.Sets))
		for k, v := range r.Sets {
			c.Sets[k] = slices.Clone(v)
		}
	}

	return c
}

func (rs *registrations) add(r *db.PCSCFRegistration) {
	k := regKey{r.IMPI, r.UEAddress.Addr()}

	var was bool
	if old, ok := rs.byKey[k]; ok {
		was = old.SignallingLost
		rs.unindex(old)
	}

	rs.countLost(was, r.SignallingLost)
	rs.index(r)

	delete(rs.pending, k)
	rs.byKey[k] = r
	rs.byToken[r.FlowToken] = r
	delete(rs.retired, r.FlowToken)
	rs.armLocked(k, r.ExpiresAt)
}

func (rs *registrations) index(r *db.PCSCFRegistration) {
	if r.Protected {
		return
	}

	m, ok := rs.bySource[r.UEAddress]
	if !ok {
		m = make(map[*db.PCSCFRegistration]struct{})
		rs.bySource[r.UEAddress] = m
	}

	m[r] = struct{}{}
}

func (rs *registrations) unindex(r *db.PCSCFRegistration) {
	m := rs.bySource[r.UEAddress]
	delete(m, r)

	if len(m) == 0 {
		delete(rs.bySource, r.UEAddress)
	}
}

func (rs *registrations) countLost(was, is bool) {
	switch {
	case is && !was:
		rs.lost.Add(1)
	case was && !is:
		rs.lost.Add(-1)
	}
}

func (rs *registrations) armLocked(k regKey, at time.Time) {
	if t, ok := rs.timers[k]; ok {
		t.Stop()
	}

	if rs.closed {
		return
	}

	rs.timers[k] = rs.clock.AfterFunc(at.Sub(rs.clock.Now()), func() { rs.expire(k) })
}

func (rs *registrations) expire(k regKey) {
	rs.mu.Lock()

	r, ok := rs.byKey[k]
	if rs.closed || !ok {
		rs.mu.Unlock()
		return
	}

	if r.ExpiresAt.After(rs.clock.Now()) {
		rs.armLocked(k, r.ExpiresAt)
		rs.mu.Unlock()

		return
	}

	old, _ := rs.removeLocked(k)
	rs.mu.Unlock()

	rs.log.Debug("registration expired", slog.String("impi", k.impi), slog.String("ue", k.ue.String()))

	if rs.expired != nil {
		rs.expired(old)
	}
}

func (rs *registrations) all() []db.PCSCFRegistration {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	out := make([]db.PCSCFRegistration, 0, len(rs.byKey))
	for _, r := range rs.byKey {
		out = append(out, *r)
	}

	return out
}

func (rs *registrations) close() {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	rs.closed = true

	for k, t := range rs.timers {
		t.Stop()
		delete(rs.timers, k)
	}
}

func (rs *registrations) token(impi string, ue netip.Addr) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	k := regKey{impi, ue.Unmap()}

	if r, ok := rs.byKey[k]; ok {
		return r.FlowToken
	}

	now := rs.clock.Now()

	for pk, p := range rs.pending {
		if !p.expires.After(now) {
			delete(rs.pending, pk)
		}
	}

	if p, ok := rs.pending[k]; ok {
		return p.token
	}

	token := strings.ToLower(rand.Text())
	rs.pending[k] = retired{token: token, expires: now.Add(pendingToken)}

	return token
}

func (rs *registrations) save(r db.PCSCFRegistration) {
	r = clone(&r)
	r.UEAddress = netip.AddrPortFrom(r.UEAddress.Addr().Unmap(), r.UEAddress.Port())

	rs.mu.Lock()
	defer rs.mu.Unlock()

	if old, ok := rs.byKey[regKey{r.IMPI, r.UEAddress.Addr()}]; ok {
		r.ID = old.ID
		r.RxSessionID, r.RxClass, r.SignallingLost = old.RxSessionID, old.RxClass, old.SignallingLost

		if old.FlowToken != r.FlowToken {
			delete(rs.byToken, old.FlowToken)
		}
	} else {
		r.RxSessionID, r.RxClass, r.SignallingLost = "", nil, false
	}

	if rs.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		saved, err := rs.store.SavePCSCFRegistration(ctx, r)

		cancel()

		if err != nil {
			rs.log.Error("saving the registration failed", slog.String("impi", r.IMPI), slog.Any("error", err))
		} else {
			r = saved
		}
	}

	rs.add(&r)
}

func (rs *registrations) remove(impi string, ue netip.Addr) (db.PCSCFRegistration, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	return rs.removeLocked(regKey{impi, ue.Unmap()})
}

func (rs *registrations) removeLocked(k regKey) (db.PCSCFRegistration, bool) {
	r, ok := rs.byKey[k]
	if !ok {
		return db.PCSCFRegistration{}, false
	}

	delete(rs.byKey, k)
	delete(rs.byToken, r.FlowToken)
	rs.unindex(r)
	rs.countLost(r.SignallingLost, false)

	if t, ok := rs.timers[k]; ok {
		t.Stop()
		delete(rs.timers, k)
	}

	rs.retired[r.FlowToken] = retired{f: flowOf(r), expires: rs.clock.Now().Add(rs.grace)}

	if rs.store != nil && r.ID != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		if err := rs.store.DeletePCSCFRegistration(ctx, r.ID); err != nil {
			rs.log.Error("deleting the registration failed", slog.String("impi", r.IMPI), slog.Any("error", err))
		}

		cancel()
	}

	return clone(r), true
}

func (rs *registrations) update(impi string, f func(r *db.PCSCFRegistration) bool) []db.PCSCFRegistration {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	var removed []db.PCSCFRegistration

	for k, r := range rs.byKey {
		if k.impi != impi {
			continue
		}

		c := clone(r)

		if !f(&c) {
			if old, ok := rs.removeLocked(k); ok {
				removed = append(removed, old)
			}

			continue
		}

		if !c.ExpiresAt.Equal(r.ExpiresAt) {
			rs.armLocked(k, c.ExpiresAt)
		}

		rs.countLost(r.SignallingLost, c.SignallingLost)
		rs.unindex(r)
		*r = c
		rs.index(r)
		rs.storeLocked(r)
	}

	return removed
}

func (rs *registrations) edit(k regKey, f func(r *db.PCSCFRegistration) bool) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	r, ok := rs.byKey[k]
	if !ok {
		return false
	}

	was := r.SignallingLost

	if !f(r) {
		return false
	}

	rs.countLost(was, r.SignallingLost)
	rs.storeLocked(r)

	return true
}

func (rs *registrations) restoreSignalling(k regKey) bool {
	if rs.lost.Load() == 0 {
		return false
	}

	return rs.edit(k, func(r *db.PCSCFRegistration) bool {
		if !r.SignallingLost {
			return false
		}

		r.SignallingLost = false

		return true
	})
}

func (rs *registrations) storeLocked(r *db.PCSCFRegistration) {
	if rs.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	if _, err := rs.store.SavePCSCFRegistration(ctx, *r); err != nil {
		rs.log.Error("saving the registration failed", slog.String("impi", r.IMPI), slog.Any("error", err))
	}
}

func (rs *registrations) get(impi string, ue netip.Addr) (db.PCSCFRegistration, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	r, ok := rs.byKey[regKey{impi, ue.Unmap()}]
	if !ok || !r.ExpiresAt.After(rs.clock.Now()) {
		return db.PCSCFRegistration{}, false
	}

	return clone(r), true
}

func (rs *registrations) signallingLost(token string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	r, ok := rs.byToken[token]

	return ok && r.SignallingLost
}

func (rs *registrations) withoutRx(impi string, ue netip.Addr) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	r, ok := rs.byKey[regKey{impi, ue.Unmap()}]

	return ok && r.RxSessionID == ""
}

func (rs *registrations) fromSource(src netip.AddrPort) (db.PCSCFRegistration, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if r := rs.fromSourceLocked(src); r != nil {
		return clone(r), true
	}

	return db.PCSCFRegistration{}, false
}

func (rs *registrations) sourceKey(src netip.AddrPort) (regKey, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if r := rs.fromSourceLocked(src); r != nil {
		return regKey{r.IMPI, r.UEAddress.Addr()}, true
	}

	return regKey{}, false
}

func (rs *registrations) fromSourceLocked(src netip.AddrPort) *db.PCSCFRegistration {
	src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
	now := rs.clock.Now()

	for r := range rs.bySource[src] {
		if r.ExpiresAt.After(now) {
			return r
		}
	}

	return nil
}

func (rs *registrations) forIMPI(impi string) (db.PCSCFRegistration, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	var best *db.PCSCFRegistration

	for k, r := range rs.byKey {
		if k.impi == impi && r.ExpiresAt.After(rs.clock.Now()) && (best == nil || r.ExpiresAt.After(best.ExpiresAt)) {
			best = r
		}
	}

	if best == nil {
		return db.PCSCFRegistration{}, false
	}

	return clone(best), true
}

func (rs *registrations) flow(token string) (flow, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if r, ok := rs.byToken[token]; ok && r.ExpiresAt.Add(rs.grace).After(rs.clock.Now()) {
		return flowOf(r), true
	}

	ret, ok := rs.retired[token]
	if !ok {
		return flow{}, false
	}

	if !ret.expires.After(rs.clock.Now()) {
		delete(rs.retired, token)
		return flow{}, false
	}

	return ret.f, true
}

func flowOf(r *db.PCSCFRegistration) flow {
	return flow{
		impi: r.IMPI, transport: sip.Transport(r.Transport), protected: r.Protected, ue: r.UEAddress, local: r.PCSCFAddress,
	}
}

func defaultIdentity(associated []string) string {
	if len(associated) == 0 {
		return ""
	}

	return associated[0]
}
