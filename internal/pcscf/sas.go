package pcscf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
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
	maxSetsPerFlow     = 3

	// maxSetsPerUE bounds the sets of a private identity and address over its flows: those of four
	// flows, the most the S-CSCF registers for a UE instance.
	maxSetsPerUE = 4 * maxSetsPerFlow
	storeTimeout = 5 * time.Second
)

var (
	errSAConflict = errors.New("the UE's protected ports or SPIs are in use by another set of security associations")
	errTooManySAs = errors.New("too many sets of security associations for the private identity and address")
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
	dbID    int64
	set     ipsec.Set
	impi    string
	flow    flowID
	state   saState
	expires time.Time
	timer   *time.Timer
	inUse   bool
	removed bool

	// answered reports that a protected REGISTER, the UE's answer to the challenge, came over the temporary set.
	answered bool

	client  []sip.SecurityMechanism
	server  sip.SecurityMechanism
	origin  *saSet
	initial bool
}

type view struct {
	s      *saSet
	state  saState
	impi   string
	flow   flowID
	client []sip.SecurityMechanism
	server sip.SecurityMechanism
}

// sameUE reports whether two sets protect the same registered contact: the same UE and registration
// flow (TS 33.203 §6.1 NOTE 2).
func (s *saSet) sameUE(o *saSet) bool {
	return s.impi == o.impi && s.set.Remote.Addr == o.set.Remote.Addr && s.flow == o.flow
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

	closeFlow func(sip.Flow)
	writes    chan storeOp
	written   chan struct{}

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

	a := &associations{
		cfg:    cfg,
		log:    logger,
		spis:   ipsec.NewSPIs(),
		sets:   make(map[*saSet]struct{}),
		byFlow: make(map[flowKey]*saSet),
	}

	if cfg.Store != nil {
		a.writes = make(chan storeOp, 1024)
		a.written = make(chan struct{})

		go a.write()
	}

	return a
}

type storeOp struct {
	s       *saSet
	rec     db.SecurityAssociation
	delete  bool
	barrier chan struct{}
}

func (a *associations) write() {
	defer close(a.written)

	for op := range a.writes {
		if op.barrier != nil {
			close(op.barrier)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)

		switch {
		case op.delete && op.s.dbID != 0:
			if err := a.cfg.Store.DeleteSecurityAssociation(ctx, op.s.dbID); err != nil {
				a.log.Error("deleting security associations failed", slog.Int64("id", op.s.dbID), slog.Any("error", err))
			}
		case !op.delete:
			op.rec.ID = op.s.dbID

			saved, err := a.cfg.Store.SaveSecurityAssociation(ctx, op.rec)
			if err != nil {
				a.log.Error("saving security associations failed", slog.String("impi", op.s.impi), slog.Any("error", err))
			} else {
				op.s.dbID = saved.ID
			}
		}

		cancel()
	}
}

func (a *associations) enqueue(op storeOp) {
	if a.writes != nil && !a.closed {
		a.writes <- op
	}
}

func (a *associations) sync() {
	a.mu.Lock()

	if a.writes == nil || a.closed {
		a.mu.Unlock()
		return
	}

	done := make(chan struct{})
	a.writes <- storeOp{barrier: done}
	a.mu.Unlock()

	<-done
}

func (a *associations) protected(port uint16) bool {
	return port == a.cfg.ServerPort || port == a.cfg.ClientPorts[0] || port == a.cfg.ClientPorts[1]
}

func (a *associations) lookup(f sip.Flow) (view, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	s := a.byFlow[keyOf(f)]
	if s == nil {
		return view{}, false
	}

	return view{s: s, state: s.state, impi: s.impi, flow: s.flow, client: s.client, server: s.server}, true
}

func (a *associations) responseFlow(in sip.Flow, res *sip.Response) (sip.Flow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	s := a.byFlow[keyOf(in)]
	if s == nil {
		return sip.Flow{}, errNoSecurityAssociation
	}

	if in.Transport != sip.UDP {
		return in, nil
	}

	if s.state == temporary && s.origin != nil && !s.origin.removed && res.StatusCode >= 300 && res.StatusCode != 401 {
		s = s.origin
	}

	return sip.Flow{
		Transport: sip.UDP,
		Local:     netip.AddrPortFrom(in.Local.Addr(), s.set.Local.PortC),
		Remote:    netip.AddrPortFrom(s.set.Remote.Addr, s.set.Remote.PortS),
	}, nil
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
	flow   flowID
	local  netip.Addr
	ue     netip.Addr
	offer  ipsec.Offer
	client []sip.SecurityMechanism
	origin *saSet
}

// clientPort is the P-CSCF's protected client port for a new set: one its flow's set being
// re-authenticated does not use (TS 33.203 §7.4), nor another flow's set toward the same protected
// server port of the UE, which stays fixed for the UE (TS 33.203 §7.1 NOTE 10).
func (a *associations) clientPort(c challenge) (uint16, bool) {
	used := func(port uint16) bool {
		for s := range a.sets {
			if s.state == temporary || s.impi != c.impi || s.set.Remote.Addr != c.ue || s.set.Local.PortC != port {
				continue
			}

			if s.flow == c.flow && (c.origin == nil || s == c.origin) || s.flow != c.flow && s.set.Remote.PortS == c.offer.Endpoint.PortS {
				return true
			}
		}

		return false
	}

	for _, port := range a.cfg.ClientPorts {
		if !used(port) {
			return port, true
		}
	}

	return 0, false
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

func (a *associations) challenged(c challenge, keys ipsec.Keys) (sip.SecurityMechanism, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return sip.SecurityMechanism{}, errors.New("P-CSCF closed")
	}

	// A REGISTER for another flow over these associations is not their re-authentication.
	if c.origin != nil && (c.origin.removed || c.origin.state == temporary || c.origin.flow != c.flow) {
		c.origin = nil
	}

	// TS 24.229 §5.2.2.2: a new challenge deletes any temporary set toward the UE, whatever its flow.
	for s := range a.sets {
		if s.state == temporary && s.impi == c.impi {
			a.unanswered(s)
			a.remove(s)
		}
	}

	portC, ok := a.clientPort(c)
	if !ok {
		return sip.SecurityMechanism{}, errSAConflict
	}

	set := ipsec.Set{
		Local:      ipsec.Endpoint{Addr: c.local, PortC: portC, PortS: a.cfg.ServerPort},
		Remote:     c.offer.Endpoint,
		Integrity:  c.offer.Integrity,
		Encryption: c.offer.Encryption,
	}
	set.Remote.Addr = c.ue

	for s := range a.sets {
		if s.set.Remote.Addr != c.ue || !conflicts(s.set, set) {
			continue
		}

		// Another flow's associations are in use, not left over (TS 33.203 §6.1 NOTE 2).
		if s == c.origin || s.impi == c.impi && s.flow != c.flow && s.state != temporary {
			return sip.SecurityMechanism{}, errSAConflict
		}

		a.log.Debug("replacing security associations the UE no longer uses", slog.String("impi", s.impi),
			slog.String("set", s.set.String()))
		a.remove(s)
	}

	n, all := 0, 0

	for s := range a.sets {
		if s.impi == c.impi && s.set.Remote.Addr == c.ue {
			all++

			if s.flow == c.flow {
				n++
			}
		}
	}

	if n >= maxSetsPerFlow || all >= maxSetsPerUE {
		return sip.SecurityMechanism{}, errTooManySAs
	}

	spiC, spiS, err := a.spis.Allocate(set.Remote.SPIC, set.Remote.SPIS)
	if err != nil {
		return sip.SecurityMechanism{}, err
	}

	set.Local.SPIC, set.Local.SPIS = spiC, spiS

	if err := a.cfg.Kernel.Install(set, keys); err != nil {
		a.spis.Release(spiC, spiS)
		return sip.SecurityMechanism{}, err
	}

	s := &saSet{
		set:     set,
		impi:    c.impi,
		flow:    c.flow,
		state:   temporary,
		expires: time.Now().Add(a.cfg.AwaitAuth),
		client:  c.client,
		server:  set.Server(),
		origin:  c.origin,
		initial: c.origin == nil,
	}

	a.add(s)

	return s.server, nil
}

func (a *associations) registered(s *saSet, o outcome) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if s.removed {
		return
	}

	if o.dereg {
		for x := range a.sets {
			if x.sameUE(s) {
				a.shorten(x, a.cfg.Grace)
			}
		}

		return
	}

	expires := s.expires
	if o.lifetime > 0 {
		expires = time.Now().Add(o.lifetime + registrationMargin)
	}

	if s.state != temporary {
		a.extend(s, expires)
		return
	}

	s.state = established
	s.client = nil

	if s.origin != nil && !s.origin.removed && s.origin.expires.After(expires) {
		expires = s.origin.expires
	}

	for x := range a.sets {
		switch {
		case x == s:
		case x == s.origin && !x.removed:
			x.state = old
			a.save(x)
		case x.sameUE(s):
			a.remove(x)
		case s.initial && s.flow == flowID{} && x.impi == s.impi && x.state != temporary:
			// TS 24.229 §5.4.1.2.2 step 4A: without the multiple registration mechanism, the new
			// contact replaces the others of the private identity.
			a.shorten(x, a.cfg.Grace)
		}
	}

	s.origin = nil
	a.extend(s, expires)
}

// TS 33.203 §7.4.2a, TS 24.229 Table 5.2.2-1
func (a *associations) requestFlow(impi string, ue netip.Addr, id flowID, tr sip.Transport) (sip.Flow, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	expiring := time.Now().Add(a.cfg.Grace)

	rank := func(s *saSet) int {
		switch {
		case s.state == established && s.inUse:
			return 3
		case s.state == old && s.expires.After(expiring):
			return 2
		case s.state == old:
			return 0
		default:
			return 1
		}
	}

	var best *saSet

	for s := range a.sets {
		if s.impi != impi || s.flow != id || s.set.Remote.Addr != ue.Unmap() || s.state == temporary {
			continue
		}

		if best == nil || rank(s) > rank(best) || rank(s) == rank(best) && s.expires.After(best.expires) {
			best = s
		}
	}

	if best == nil {
		return sip.Flow{}, false
	}

	return sip.Flow{
		Transport: tr,
		Local:     netip.AddrPortFrom(best.set.Local.Addr, best.set.Local.PortC),
		Remote:    netip.AddrPortFrom(best.set.Remote.Addr, best.set.Remote.PortS),
	}, true
}

func (a *associations) deregistered(k regKey) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for s := range a.sets {
		if s.impi == k.impi && s.flow == k.flow && s.set.Remote.Addr == k.ue {
			a.shorten(s, a.cfg.Grace)
		}
	}
}

func (a *associations) failed(s *saSet) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !s.removed && s.state == temporary {
		a.shorten(s, a.cfg.Grace)
	}
}

// challengeAnswered records that a protected REGISTER came over the set.
func (a *associations) challengeAnswered(s *saSet) {
	a.mu.Lock()
	defer a.mu.Unlock()

	s.answered = true
}

// unanswered logs a temporary set no protected REGISTER came over before it went: the UE did not answer its
// challenge, or its answer did not reach the P-CSCF, as when ESP is dropped or NATed on the way.
func (a *associations) unanswered(s *saSet) {
	if s.state == temporary && !s.answered {
		a.log.Warn("no protected REGISTER came over the security associations of a challenge", slog.String("impi", s.impi),
			slog.String("ue", s.set.Remote.Addr.String()), slog.String("set", s.set.String()))
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

	a.unanswered(s)
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

	if a.closeFlow != nil {
		for _, k := range flowKeys(s.set) {
			go a.closeFlow(sip.Flow{Transport: sip.TCP, Local: k.local, Remote: k.remote})
		}
	}

	a.spis.Release(s.set.Local.SPIC, s.set.Local.SPIS)
	a.enqueue(storeOp{s: s, delete: true})

	a.log.Debug("security associations removed", slog.String("impi", s.impi), slog.String("state", s.state.String()),
		slog.String("set", s.set.String()))
}

func (a *associations) save(s *saSet) {
	if s.state != temporary {
		a.enqueue(storeOp{s: s, rec: record(s)})
	}
}

func record(s *saSet) db.SecurityAssociation {
	state := db.SecurityAssociationEstablished
	if s.state == old {
		state = db.SecurityAssociationOld
	}

	return db.SecurityAssociation{
		IMPI:         s.impi,
		State:        state,
		PCSCFAddress: s.set.Local.Addr,
		UEAddress:    s.set.Remote.Addr,
		Instance:     s.flow.instance,
		RegID:        s.flow.regID,
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
		dbID:  r.ID,
		impi:  r.IMPI,
		flow:  flowID{r.Instance, r.RegID},
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
		if s.state == established && slices.ContainsFunc(sets, func(o *saSet) bool { return o.state == old && o.sameUE(s) }) {
			s.inUse = false
		}

		if lost[s.set] {
			a.log.Warn("security associations lost by the kernel; the UE must register again",
				slog.String("impi", s.impi), slog.String("set", s.set.String()))

			_ = a.cfg.Kernel.Remove(s.set)

			if err := a.cfg.Store.DeleteSecurityAssociation(ctx, s.dbID); err != nil {
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

	if a.closed {
		a.mu.Unlock()
		return
	}

	a.closed = true

	for s := range a.sets {
		s.timer.Stop()
	}

	if a.writes != nil {
		close(a.writes)
	}

	a.mu.Unlock()

	if a.written != nil {
		<-a.written
	}
}
