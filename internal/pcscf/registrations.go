package pcscf

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// RegistrationStore keeps the P-CSCF's registrations and its own reg event
// subscriptions across restarts.
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

// flow is where requests to a registered UE go: over its security
// associations when it has some, else to the address it registered from.
type flow struct {
	impi      string
	transport sip.Transport
	ue        netip.AddrPort
	local     netip.Addr
}

type retired struct {
	f       flow
	expires time.Time
}

// registrations holds the UEs registered through this P-CSCF (TS 24.229
// §5.2.2.1). A removed registration's flow token keeps mapping to its flow
// for the grace, so that the NOTIFY reporting the removal still reaches the
// UE.
type registrations struct {
	store RegistrationStore
	log   *slog.Logger
	clock Clock
	grace time.Duration

	mu      sync.Mutex
	byKey   map[regKey]*db.PCSCFRegistration
	byToken map[string]*db.PCSCFRegistration
	retired map[string]retired
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
	rs.byKey[regKey{r.IMPI, r.UEAddress.Addr()}] = r
	rs.byToken[r.FlowToken] = r
	delete(rs.retired, r.FlowToken)
}

// token returns the flow token of the UE's registration, or a new one.
func (rs *registrations) token(impi string, ue netip.Addr) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if r, ok := rs.byKey[regKey{impi, ue.Unmap()}]; ok {
		return r.FlowToken
	}

	return strings.ToLower(rand.Text())
}

// save stores the registration a 200 to a REGISTER created or refreshed.
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

// remove deletes a registration; its flow token stays usable for the grace.
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

// update applies f to the private identity's registrations, stores the ones
// it keeps and removes the ones it rejects. It returns the removed ones.
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

// fromAddress returns a registration of the UE at an address.
func (rs *registrations) fromAddress(ue netip.Addr) (db.PCSCFRegistration, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	ue = ue.Unmap()
	now := rs.clock.Now()

	for k, r := range rs.byKey {
		if k.ue == ue && r.ExpiresAt.After(now) {
			return *r, true
		}
	}

	return db.PCSCFRegistration{}, false
}

// forIMPI returns the private identity's registration that lasts longest.
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

// flow returns the flow of a flow token, current or retired.
func (rs *registrations) flow(token string) (flow, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if r, ok := rs.byToken[token]; ok {
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
	return flow{impi: r.IMPI, transport: sip.Transport(r.Transport), ue: r.UEAddress, local: r.PCSCFAddress}
}

// defaultIdentity is the first SIP URI of the P-Associated-URI list, else its
// first entry (TS 24.229 §5.2.2.1).
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
