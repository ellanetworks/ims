package pcscf

import (
	"context"
	"crypto/rand"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
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

	mu      sync.Mutex
	byKey   map[regKey]*db.PCSCFRegistration
	byToken map[string]*db.PCSCFRegistration
	retired map[string]retired
	pending map[regKey]retired
}

func newRegistrations(store RegistrationStore, clock Clock, grace time.Duration, logger *slog.Logger) *registrations {
	return &registrations{
		store:   store,
		log:     logger,
		clock:   clock,
		grace:   grace,
		byKey:   make(map[regKey]*db.PCSCFRegistration),
		byToken: make(map[string]*db.PCSCFRegistration),
		retired: make(map[string]retired),
		pending: make(map[regKey]retired),
	}
}

func (rs *registrations) restore(ctx context.Context) error {
	if rs.store == nil {
		return nil
	}

	regs, err := rs.store.ListPCSCFRegistrations(ctx)
	if err != nil {
		return err
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()

	now := rs.clock.Now()

	for _, r := range regs {
		if !r.ExpiresAt.After(now) {
			if err := rs.store.DeletePCSCFRegistration(ctx, r.ID); err != nil {
				return err
			}

			continue
		}

		rs.add(&r)
	}

	return nil
}

func (rs *registrations) add(r *db.PCSCFRegistration) {
	delete(rs.pending, regKey{r.IMPI, r.UEAddress.Addr()})
	rs.byKey[regKey{r.IMPI, r.UEAddress.Addr()}] = r
	rs.byToken[r.FlowToken] = r
	delete(rs.retired, r.FlowToken)
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
	r.UEAddress = netip.AddrPortFrom(r.UEAddress.Addr().Unmap(), r.UEAddress.Port())

	rs.mu.Lock()
	defer rs.mu.Unlock()

	if old, ok := rs.byKey[regKey{r.IMPI, r.UEAddress.Addr()}]; ok {
		r.ID = old.ID

		if old.FlowToken != r.FlowToken {
			delete(rs.byToken, old.FlowToken)
		}
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

func (rs *registrations) remove(impi string, ue netip.Addr) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	_, ok := rs.removeLocked(regKey{impi, ue.Unmap()})

	return ok
}

func (rs *registrations) removeLocked(k regKey) (db.PCSCFRegistration, bool) {
	r, ok := rs.byKey[k]
	if !ok {
		return db.PCSCFRegistration{}, false
	}

	delete(rs.byKey, k)
	delete(rs.byToken, r.FlowToken)
	rs.retired[r.FlowToken] = retired{f: flowOf(r), expires: rs.clock.Now().Add(rs.grace)}

	if rs.store != nil && r.ID != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		if err := rs.store.DeletePCSCFRegistration(ctx, r.ID); err != nil {
			rs.log.Error("deleting the registration failed", slog.String("impi", r.IMPI), slog.Any("error", err))
		}

		cancel()
	}

	return *r, true
}

func (rs *registrations) update(impi string, f func(r *db.PCSCFRegistration) bool) []db.PCSCFRegistration {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	var removed []db.PCSCFRegistration

	for k, r := range rs.byKey {
		if k.impi != impi {
			continue
		}

		c := *r
		c.Contacts = slices.Clone(r.Contacts)
		c.AssociatedURIs = slices.Clone(r.AssociatedURIs)
		c.Sets = maps.Clone(r.Sets)

		if !f(&c) {
			if old, ok := rs.removeLocked(k); ok {
				removed = append(removed, old)
			}

			continue
		}

		*r = c

		if rs.store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
			if _, err := rs.store.SavePCSCFRegistration(ctx, c); err != nil {
				rs.log.Error("saving the registration failed", slog.String("impi", impi), slog.Any("error", err))
			}

			cancel()
		}
	}

	return removed
}

func (rs *registrations) get(impi string, ue netip.Addr) (db.PCSCFRegistration, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	r, ok := rs.byKey[regKey{impi, ue.Unmap()}]
	if !ok || !r.ExpiresAt.After(rs.clock.Now()) {
		return db.PCSCFRegistration{}, false
	}

	return *r, true
}

func (rs *registrations) fromSource(src netip.AddrPort) (db.PCSCFRegistration, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
	now := rs.clock.Now()

	for _, r := range rs.byKey {
		if !r.Protected && r.UEAddress == src && r.ExpiresAt.After(now) {
			return *r, true
		}
	}

	return db.PCSCFRegistration{}, false
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

	return *best, true
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
	for _, a := range associated {
		if u, err := sip.ParseURI(a); err == nil && u.IsSIP() {
			return a
		}
	}

	if len(associated) > 0 {
		return associated[0]
	}

	return ""
}
