package pcscf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/sip"
)

const (
	DefaultAwaitAuth = 4 * time.Minute
	DefaultGrace     = 128 * time.Second

	registrationMargin = 30 * time.Second
	maxSetsPerIMPI     = 3
	storeTimeout       = 5 * time.Second
)

var (
	errSAConflict = errors.New("the UE's protected ports or SPIs are in use by another set of security associations")
	errTooManySAs = errors.New("too many sets of security associations for the private identity")
)

type Kernel interface {
	Install(ipsec.Set, ipsec.Keys) error
	Remove(ipsec.Set) error
	Reconcile([]ipsec.Set) ([]ipsec.Set, error)
}

type Store interface {
	SaveSecurityAssociation(context.Context, db.SecurityAssociation) (db.SecurityAssociation, error)
	DeleteSecurityAssociation(context.Context, int64) error
	ListSecurityAssociations(context.Context) ([]db.SecurityAssociation, error)
}

type IPsec struct {
	Kernel      Kernel
	Store       Store
	Policy      ipsec.Policy
	ServerPort  uint16
	ClientPorts [2]uint16
	AwaitAuth   time.Duration
	Grace       time.Duration
}

type saState int

const (
	temporary saState = iota
	established
	old
)

func (s saState) String() string {
	switch s {
	case temporary:
		return "temporary"
	case established:
		return "established"
	default:
		return "old"
	}
}

type saSet struct {
	id      int64
	set     ipsec.Set
	impi    string
	state   saState
	expires time.Time
	timer   *time.Timer
	inUse   bool
	removed bool

	client []sip.SecurityMechanism
	server sip.SecurityMechanism
	origin *saSet
}

func (s *saSet) sameUE(o *saSet) bool {
	return s.impi == o.impi && s.set.Remote.Addr == o.set.Remote.Addr
}

type flowKey struct {
	remote, local netip.AddrPort
}

func flowKeys(s ipsec.Set) [2]flowKey {
	return [2]flowKey{
		{netip.AddrPortFrom(s.Remote.Addr, s.Remote.PortC), netip.AddrPortFrom(s.Local.Addr, s.Local.PortS)},
		{netip.AddrPortFrom(s.Remote.Addr, s.Remote.PortS), netip.AddrPortFrom(s.Local.Addr, s.Local.PortC)},
	}
}

func keyOf(f sip.Flow) flowKey {
	return flowKey{
		netip.AddrPortFrom(f.Remote.Addr().Unmap(), f.Remote.Port()),
		netip.AddrPortFrom(f.Local.Addr().Unmap(), f.Local.Port()),
	}
}

type associations struct {
	cfg  IPsec
	log  *slog.Logger
	spis *ipsec.SPIs

	mu     sync.Mutex
	closed bool
	sets   map[*saSet]struct{}
	byFlow map[flowKey]*saSet
}

func newAssociations(cfg IPsec, logger *slog.Logger) *associations {
	if cfg.AwaitAuth <= 0 {
		cfg.AwaitAuth = DefaultAwaitAuth
	}

	if cfg.Grace <= 0 {
		cfg.Grace = DefaultGrace
	}

	return &associations{
		cfg:    cfg,
		log:    logger,
		spis:   ipsec.NewSPIs(),
		sets:   make(map[*saSet]struct{}),
		byFlow: make(map[flowKey]*saSet),
	}
}

func (a *associations) protected(port uint16) bool {
	return port == a.cfg.ServerPort || port == a.cfg.ClientPorts[0] || port == a.cfg.ClientPorts[1]
}

func (a *associations) lookup(f sip.Flow) *saSet {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.byFlow[keyOf(f)]
}

func (a *associations) received(s *saSet) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if s.removed || s.state != established || s.inUse {
		return
	}

	s.inUse = true

	for o := range a.sets {
		if o.state == old && o.sameUE(s) {
			a.shorten(o, a.cfg.Grace)
		}
	}
}

func (a *associations) hasEstablished(ue netip.Addr) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	for s := range a.sets {
		if s.state != temporary && s.set.Remote.Addr == ue {
			return true
		}
	}

	return false
}

type challenge struct {
	impi   string
	local  netip.Addr
	ue     netip.Addr
	offer  ipsec.Offer
	client []sip.SecurityMechanism
	origin *saSet
}

func (a *associations) clientPort(c challenge) uint16 {
	for s := range a.sets {
		if s.state != temporary && s.impi == c.impi && s.set.Remote.Addr == c.ue && s.set.Local.PortC == a.cfg.ClientPorts[0] &&
			(c.origin == nil || s == c.origin) {
			return a.cfg.ClientPorts[1]
		}
	}

	return a.cfg.ClientPorts[0]
}

func conflicts(a, b ipsec.Set) bool {
	ka, kb := flowKeys(a), flowKeys(b)
	if ka[0] == kb[0] || ka[0] == kb[1] || ka[1] == kb[0] || ka[1] == kb[1] {
		return true
	}

	for _, x := range []uint32{a.Remote.SPIC, a.Remote.SPIS} {
		if x == b.Remote.SPIC || x == b.Remote.SPIS {
			return true
		}
	}

	return false
}

func (a *associations) challenged(c challenge, keys ipsec.Keys) (*saSet, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return nil, errors.New("P-CSCF closed")
	}

	for s := range a.sets {
		if s.state == temporary && s.impi == c.impi {
			a.remove(s)
		}
	}

	set := ipsec.Set{
		Local:      ipsec.Endpoint{Addr: c.local, PortC: a.clientPort(c), PortS: a.cfg.ServerPort},
		Remote:     c.offer.Endpoint,
		Integrity:  c.offer.Integrity,
		Encryption: c.offer.Encryption,
	}
	set.Remote.Addr = c.ue

	for s := range a.sets {
		if s.set.Remote.Addr != c.ue || !conflicts(s.set, set) {
			continue
		}

		if s == c.origin {
			return nil, errSAConflict
		}

		a.log.Info("replacing security associations the UE no longer uses", slog.String("impi", s.impi),
			slog.String("set", s.set.String()))
		a.remove(s)
	}

	n := 0

	for s := range a.sets {
		if s.impi == c.impi {
			n++
		}
	}

	if n >= maxSetsPerIMPI {
		return nil, errTooManySAs
	}

	spiC, spiS, err := a.spis.Allocate(set.Remote.SPIC, set.Remote.SPIS)
	if err != nil {
		return nil, err
	}

	set.Local.SPIC, set.Local.SPIS = spiC, spiS

	if err := a.cfg.Kernel.Install(set, keys); err != nil {
		a.spis.Release(spiC, spiS)
		return nil, err
	}

	s := &saSet{
		set:     set,
		impi:    c.impi,
		state:   temporary,
		expires: time.Now().Add(a.cfg.AwaitAuth),
		client:  c.client,
		server:  set.Server(),
		origin:  c.origin,
	}

	a.add(s)

	return s, nil
}

func (a *associations) registered(s *saSet, lifetime time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if s.removed {
		return
	}

	if lifetime <= 0 {
		for o := range a.sets {
			if o.sameUE(s) && o.state != temporary {
				a.shorten(o, a.cfg.Grace)
			}
		}

		return
	}

	expires := time.Now().Add(lifetime + registrationMargin)

	if s.state != temporary {
		a.extend(s, expires)
		return
	}

	s.state = established
	s.client = nil

	for o := range a.sets {
		switch {
		case o == s || !o.sameUE(s):
		case o == s.origin:
			o.state = old
			a.save(o)
		default:
			a.remove(o)
		}
	}

	s.origin = nil
	a.extend(s, expires)
}

func (a *associations) failed(s *saSet) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !s.removed && s.state == temporary {
		a.shorten(s, a.cfg.Grace)
	}
}

func (a *associations) extend(s *saSet, expires time.Time) {
	if expires.After(s.expires) {
		s.expires = expires
		s.timer.Reset(time.Until(expires))
	}

	a.save(s)
}

func (a *associations) shorten(s *saSet, d time.Duration) {
	expires := time.Now().Add(d)
	if expires.Before(s.expires) {
		s.expires = expires
		s.timer.Reset(d)
		a.save(s)
	}
}

func (a *associations) add(s *saSet) {
	a.sets[s] = struct{}{}

	for _, k := range flowKeys(s.set) {
		a.byFlow[k] = s
	}

	s.timer = time.AfterFunc(time.Until(s.expires), func() { a.expire(s) })
}

func (a *associations) expire(s *saSet) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if s.removed || a.closed {
		return
	}

	if d := time.Until(s.expires); d > 0 {
		s.timer.Reset(d)
		return
	}

	a.remove(s)
}

func (a *associations) remove(s *saSet) {
	if s.removed {
		return
	}

	s.removed = true
	s.timer.Stop()
	delete(a.sets, s)

	for _, k := range flowKeys(s.set) {
		if a.byFlow[k] == s {
			delete(a.byFlow, k)
		}
	}

	if err := a.cfg.Kernel.Remove(s.set); err != nil {
		a.log.Error("removing security associations failed", slog.String("set", s.set.String()), slog.Any("error", err))
	}

	a.spis.Release(s.set.Local.SPIC, s.set.Local.SPIS)

	if s.id != 0 && a.cfg.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		defer cancel()

		if err := a.cfg.Store.DeleteSecurityAssociation(ctx, s.id); err != nil {
			a.log.Error("deleting security associations failed", slog.Int64("id", s.id), slog.Any("error", err))
		}
	}

	a.log.Debug("security associations removed", slog.String("impi", s.impi), slog.String("state", s.state.String()),
		slog.String("set", s.set.String()))
}

func (a *associations) save(s *saSet) {
	if s.state == temporary || a.cfg.Store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	saved, err := a.cfg.Store.SaveSecurityAssociation(ctx, record(s))
	if err != nil {
		a.log.Error("saving security associations failed", slog.String("impi", s.impi), slog.Any("error", err))
		return
	}

	s.id = saved.ID
}

func record(s *saSet) db.SecurityAssociation {
	state := db.SecurityAssociationEstablished
	if s.state == old {
		state = db.SecurityAssociationOld
	}

	return db.SecurityAssociation{
		ID:           s.id,
		IMPI:         s.impi,
		State:        state,
		PCSCFAddress: s.set.Local.Addr,
		UEAddress:    s.set.Remote.Addr,
		PCSCFPortC:   s.set.Local.PortC,
		PCSCFPortS:   s.set.Local.PortS,
		UEPortC:      s.set.Remote.PortC,
		UEPortS:      s.set.Remote.PortS,
		SPIPC:        s.set.Local.SPIC,
		SPIPS:        s.set.Local.SPIS,
		SPIUC:        s.set.Remote.SPIC,
		SPIUS:        s.set.Remote.SPIS,
		Integrity:    string(s.set.Integrity),
		Encryption:   string(s.set.Encryption),
		ExpiresAt:    s.expires,
	}
}

func fromRecord(r db.SecurityAssociation) *saSet {
	state := established
	if r.State == db.SecurityAssociationOld {
		state = old
	}

	return &saSet{
		id:    r.ID,
		impi:  r.IMPI,
		state: state,
		inUse: true,
		set: ipsec.Set{
			Local: ipsec.Endpoint{
				Addr: r.PCSCFAddress, PortC: r.PCSCFPortC, PortS: r.PCSCFPortS, SPIC: r.SPIPC, SPIS: r.SPIPS,
			},
			Remote: ipsec.Endpoint{
				Addr: r.UEAddress, PortC: r.UEPortC, PortS: r.UEPortS, SPIC: r.SPIUC, SPIS: r.SPIUS,
			},
			Integrity:  ipsec.Integrity(r.Integrity),
			Encryption: ipsec.Encryption(r.Encryption),
		},
		expires: r.ExpiresAt,
	}
}

func (a *associations) restore(ctx context.Context) error {
	if a.cfg.Store == nil {
		_, err := a.cfg.Kernel.Reconcile(nil)
		return err
	}

	records, err := a.cfg.Store.ListSecurityAssociations(ctx)
	if err != nil {
		return err
	}

	var (
		keep []ipsec.Set
		sets []*saSet
	)

	now := time.Now()

	for _, r := range records {
		s := fromRecord(r)
		if !s.expires.After(now) || !a.protected(s.set.Local.PortS) || !a.protected(s.set.Local.PortC) {
			if err := a.cfg.Store.DeleteSecurityAssociation(ctx, r.ID); err != nil {
				return err
			}

			continue
		}

		keep = append(keep, s.set)
		sets = append(sets, s)
	}

	missing, err := a.cfg.Kernel.Reconcile(keep)
	if err != nil {
		return fmt.Errorf("reconcile security associations: %w", err)
	}

	lost := make(map[ipsec.Set]bool, len(missing))
	for _, m := range missing {
		lost[m] = true
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, s := range sets {
		if lost[s.set] {
			a.log.Info("security associations lost by the kernel; the UE must register again",
				slog.String("impi", s.impi), slog.String("set", s.set.String()))

			_ = a.cfg.Kernel.Remove(s.set)

			if err := a.cfg.Store.DeleteSecurityAssociation(ctx, s.id); err != nil {
				return err
			}

			continue
		}

		a.spis.Reserve(s.set.Local.SPIC, s.set.Local.SPIS)
		a.add(s)
	}

	return nil
}

func (a *associations) close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closed = true

	for s := range a.sets {
		s.timer.Stop()
	}
}
