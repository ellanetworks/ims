// Package testue is a test UE that registers with the IMS as an IR.92 phone
// does: IMS-AKA with its own Milenage, sec-agree and IPsec with real SAs,
// re-registration and deregistration (TS 24.229 §5.1.1, TS 33.203).
package testue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

const (
	DefaultExpires = 600000 * time.Second

	// RegAwaitAuth is the lifetime of the temporary SAs (TS 24.229
	// §5.1.1.5.1).
	RegAwaitAuth = 4 * time.Minute

	saMargin = 30 * time.Second

	// The UE answers at most two consecutive invalid challenges (TS 24.229
	// §5.1.1.5.12).
	maxInvalidChallenges = 2

	maxChallenges = 4
)

var (
	ErrNotRegistered = errors.New("testue: not registered")

	ErrNoSecurityServer = errors.New("testue: 401 without Security-Server")

	ErrNoAcceptableOffer = errors.New("testue: no acceptable Security-Server mechanism")

	ErrNetworkAuthentication = errors.New("testue: network authentication failed (MAC failure)")

	ErrTooManyChallenges = errors.New("testue: too many challenges")

	ErrClosed = errors.New("testue: closed")
)

// ResponseError is a final response that ended a procedure.
type ResponseError struct {
	Response *sip.Response
}

func (e *ResponseError) Error() string {
	return "testue: REGISTER answered " + e.Response.StartLine()
}

type Offer struct {
	Integrity  ipsec.Integrity
	Encryption ipsec.Encryption
}

func DefaultOffers() []Offer {
	return []Offer{
		{ipsec.HMACSHA196, ipsec.EncryptionNull},
		{ipsec.HMACMD596, ipsec.EncryptionNull},
	}
}

type Kernel interface {
	Install(s ipsec.Set, k ipsec.Keys) error
	Remove(s ipsec.Set) error
}

type Config struct {
	// IMSI and the length of its MNC (2 by default) give the USIM-only
	// identities. IMPI, IMPU and HomeDomain, from an ISIM, override them.
	IMSI       string
	MNCLength  int
	IMPI       string
	IMPU       string
	HomeDomain string

	K   []byte
	OPc []byte

	// SQN is the initial SQN_MS: the highest sequence number accepted.
	SQN uint64

	IMEI string

	// PCSCF is the P-CSCF's address and unprotected port.
	PCSCF     netip.AddrPort
	Local     netip.Addr
	Transport sip.Transport

	// Offers are the Security-Client mechanisms; DefaultOffers when empty.
	Offers []Offer

	// Plain sends no Security-Client, for the plain-SIP path of the P-CSCF.
	Plain bool

	// AccessNetworkInfo is the P-Access-Network-Info of protected requests.
	AccessNetworkInfo string

	Expires time.Duration

	// Kernel installs the SAs; required unless Plain.
	Kernel Kernel

	// Do runs f in the UE's network namespace; the UE creates its sockets
	// and dials inside it. Nil runs f directly.
	Do func(f func())

	Logger *slog.Logger
}

type SAState int

const (
	Temporary SAState = iota
	Established
	Old
)

func (s SAState) String() string {
	switch s {
	case Temporary:
		return "temporary"
	case Established:
		return "established"
	case Old:
		return "old"
	}

	return fmt.Sprintf("SAState(%d)", int(s))
}

type SA struct {
	Set     ipsec.Set
	Keys    ipsec.Keys
	State   SAState
	Expires time.Time
}

type State struct {
	Registered     bool
	Expires        time.Time
	DefaultIMPU    string
	AssociatedURIs []string
	ServiceRoute   []string

	// Barred is whether the registered IMPU is absent from the
	// P-Associated-URI.
	Barred bool
}

// Event is a request or a response the UE received, or an error of an
// automatic re-registration.
type Event struct {
	Request  *sip.Request
	Response *sip.Response
	Err      error
}

type saSet struct {
	set     ipsec.Set
	keys    ipsec.Keys
	state   SAState
	expires time.Time
	timer   *time.Timer
	server  []string
	removed bool
}

// client is the UE's side of a Security-Client: port_us is the UE's.
type client struct {
	portC      uint16
	spiC, spiS uint32
}

type UE struct {
	cfg      Config
	id       identities
	instance string
	user     string
	offers   []Offer
	log      *slog.Logger

	layer       *transaction.Layer
	unprotected netip.AddrPort
	portS       uint16
	spis        *ipsec.SPIs
	events      chan Event

	op sync.Mutex

	mu          sync.Mutex
	closed      bool
	sqn         uint64
	expires     time.Duration
	callID      string
	fromTag     string
	cseq        uint32
	auth        string
	state       State
	sets        []*saSet
	auto        bool
	reregTimer  *time.Timer
	expiryTimer *time.Timer
}

func New(cfg Config) (*UE, error) {
	id, err := deriveIdentities(cfg)
	if err != nil {
		return nil, err
	}

	instance, err := instanceID(cfg.IMEI)
	if err != nil {
		return nil, err
	}

	if !cfg.PCSCF.IsValid() || !cfg.Local.IsValid() || cfg.PCSCF.Addr().Is4() != cfg.Local.Is4() {
		return nil, fmt.Errorf("testue: P-CSCF %s and local address %s", cfg.PCSCF, cfg.Local)
	}

	if !cfg.Plain && cfg.Kernel == nil {
		return nil, errors.New("testue: no Kernel to install the SAs")
	}

	if cfg.Transport == "" {
		cfg.Transport = sip.UDP
	}

	if cfg.Expires <= 0 {
		cfg.Expires = DefaultExpires
	}

	if cfg.AccessNetworkInfo == "" {
		cfg.AccessNetworkInfo = accessNetworkInfo(id.mcc, id.mnc)
	}

	if cfg.Do == nil {
		cfg.Do = func(f func()) { f() }
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	u := &UE{
		cfg:      cfg,
		id:       id,
		instance: instance,
		user:     timeUUID(),
		offers:   cfg.Offers,
		log:      cfg.Logger,
		spis:     ipsec.NewSPIs(),
		events:   make(chan Event, 1024),
		sqn:      cfg.SQN,
		expires:  cfg.Expires,
		auto:     true,
	}

	if len(u.offers) == 0 {
		u.offers = DefaultOffers()
	}

	do := cfg.Do

	u.layer = transaction.New(transaction.Config{
		Handler: u,
		Logger:  cfg.Logger,
		Filter:  u.filter,
		Transport: transport.Config{
			Dial: func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
				var (
					c   net.Conn
					err error
				)

				do(func() { c, err = d.DialContext(ctx, network, address) })

				return c, err
			},
		},
	})

	if u.unprotected, err = u.listen(0); err != nil {
		_ = u.layer.Close()
		return nil, err
	}

	return u, nil
}

func (u *UE) listen(port uint16) (netip.AddrPort, error) {
	var (
		bound netip.AddrPort
		err   error
	)

	for range 16 {
		u.cfg.Do(func() { bound, err = u.layer.Listen(context.Background(), netip.AddrPortFrom(u.cfg.Local, port)) })

		if err != nil || port != 0 || (bound.Port() != 5060 && bound.Port() != 5061) {
			break
		}
	}

	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("testue: %w", err)
	}

	return bound, nil
}

func (u *UE) IMPI() string       { return u.id.impi }
func (u *UE) IMPU() string       { return u.id.impu }
func (u *UE) HomeDomain() string { return u.id.domain }

// Unprotected is the address of the UE's unprotected socket.
func (u *UE) Unprotected() netip.AddrPort { return u.unprotected }

func (u *UE) Events() <-chan Event { return u.events }

func (u *UE) event(e Event) {
	select {
	case u.events <- e:
	default:
	}
}

func (u *UE) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()

	s := u.state
	s.AssociatedURIs = slices.Clone(s.AssociatedURIs)
	s.ServiceRoute = slices.Clone(s.ServiceRoute)

	return s
}

// SQN is the UE's SQN_MS.
func (u *UE) SQN() uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.sqn
}

// SAs are the SA sets the UE has installed.
func (u *UE) SAs() []SA {
	u.mu.Lock()
	defer u.mu.Unlock()

	out := make([]SA, 0, len(u.sets))
	for _, s := range u.sets {
		out = append(out, SA{Set: s.set, Keys: s.keys, State: s.state, Expires: s.expires})
	}

	return out
}

// SetAutoReregister turns the re-registration timer on or off; it is on by
// default.
func (u *UE) SetAutoReregister(on bool) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.auto = on

	if !on && u.reregTimer != nil {
		u.reregTimer.Stop()
		u.reregTimer = nil
	}

	if on && u.state.Registered {
		u.scheduleLocked(time.Until(u.state.Expires))
	}
}

// Close removes the SAs and closes the sockets, without deregistering.
func (u *UE) Close() error {
	u.mu.Lock()

	if u.closed {
		u.mu.Unlock()
		return nil
	}

	u.closed = true
	u.stopTimersLocked()

	for _, s := range slices.Clone(u.sets) {
		u.dropLocked(s)
	}

	u.mu.Unlock()

	return u.layer.Close()
}

func (u *UE) stopTimersLocked() {
	for _, t := range []**time.Timer{&u.reregTimer, &u.expiryTimer} {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}
}

func (u *UE) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	u.event(Event{Request: req})

	code := 200
	if req.Method == "INVITE" {
		code = 486
	}

	_ = tx.Respond(sip.NewResponse(req, code, ""))
}

func (u *UE) HandleCancel(*transaction.ServerTransaction, *sip.Request) {}

func (u *UE) HandleAck(*sip.Request) {}

func (u *UE) HandleTransactionError(*transaction.ServerTransaction, error) {}

// filter deletes the old SAs once a message arrives on the established SAs
// that replaced them (TS 33.203 §7.4.1a).
func (u *UE) filter(m sip.Message) error {
	f := m.Env().Flow

	u.mu.Lock()
	defer u.mu.Unlock()

	var est *saSet

	for _, s := range u.sets {
		if s.state == Established && inbound(s.set, f) {
			est = s
		}
	}

	if est == nil {
		return nil
	}

	for _, s := range u.sets {
		if s.state == Old {
			u.dropLocked(s)
		}
	}

	return nil
}

// inbound is whether a message on f arrived through the set.
func inbound(s ipsec.Set, f sip.Flow) bool {
	local, remote := f.Local.Port(), f.Remote.Port()
	if f.Remote.Addr().Unmap() != s.Remote.Addr {
		return false
	}

	return (local == s.Local.PortS && remote == s.Remote.PortC) || (local == s.Local.PortC && remote == s.Remote.PortS)
}

type waiter struct {
	u     *UE
	final chan *sip.Response
	err   chan error
}

func (w *waiter) HandleResponse(res *sip.Response) {
	w.u.event(Event{Response: res})

	if res.StatusCode >= 200 {
		select {
		case w.final <- res:
		default:
		}
	}
}

func (w *waiter) HandleError(err error) {
	select {
	case w.err <- err:
	default:
	}
}

func (u *UE) request(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	w := &waiter{u: u, final: make(chan *sip.Response, 1), err: make(chan error, 1)}

	if _, err := u.layer.Request(req, w); err != nil {
		return nil, fmt.Errorf("testue: send REGISTER: %w", err)
	}

	select {
	case res := <-w.final:
		return res, nil
	case err := <-w.err:
		return nil, fmt.Errorf("testue: REGISTER: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (u *UE) newClient() (client, error) {
	bound, err := u.listen(0)
	if err != nil {
		return client{}, err
	}

	spiC, spiS, err := u.spis.Allocate()
	if err != nil {
		return client{}, fmt.Errorf("testue: %w", err)
	}

	return client{portC: bound.Port(), spiC: spiC, spiS: spiS}, nil
}

func (u *UE) securityClient(c client) []string {
	out := make([]string, 0, len(u.offers))

	for _, o := range u.offers {
		out = append(out, sip.SecurityMechanism{Name: ipsec.Mechanism, Params: sip.Params{
			{Name: "prot", Value: "esp"},
			{Name: "mod", Value: "trans"},
			{Name: "spi-c", Value: fmt.Sprint(c.spiC)},
			{Name: "spi-s", Value: fmt.Sprint(c.spiS)},
			{Name: "port-c", Value: fmt.Sprint(c.portC)},
			{Name: "port-s", Value: fmt.Sprint(u.portS)},
			{Name: "alg", Value: string(o.Integrity)},
			{Name: "ealg", Value: string(o.Encryption)},
		}}.String())
	}

	return out
}

// selectServer takes the first Security-Server mechanism the UE offered
// (TS 33.203 §7.2).
func (u *UE) selectServer(res *sip.Response) (ipsec.Offer, []string, error) {
	values := res.Header.Values("Security-Server")
	if len(values) == 0 {
		return ipsec.Offer{}, nil, ErrNoSecurityServer
	}

	ms, err := res.Header.SecurityMechanisms("Security-Server")
	if err != nil {
		return ipsec.Offer{}, nil, fmt.Errorf("testue: %w", err)
	}

	for _, m := range ms {
		o, err := ipsec.ParseOffer(m)
		if err != nil {
			continue
		}

		if slices.Contains(u.offers, Offer{o.Integrity, o.Encryption}) {
			return o, values, nil
		}
	}

	return ipsec.Offer{}, nil, ErrNoAcceptableOffer
}

func (u *UE) install(c client, o ipsec.Offer, keys ipsec.Keys, server []string) (*saSet, error) {
	set := ipsec.Set{
		Local: ipsec.Endpoint{
			Addr: u.cfg.Local, PortC: c.portC, PortS: u.portS, SPIC: c.spiC, SPIS: c.spiS,
		},
		Remote:     o.Endpoint,
		Integrity:  o.Integrity,
		Encryption: o.Encryption,
	}
	set.Remote.Addr = u.cfg.PCSCF.Addr()

	if err := u.cfg.Kernel.Install(set, keys); err != nil {
		return nil, fmt.Errorf("testue: %w", err)
	}

	s := &saSet{set: set, keys: keys, state: Temporary, server: server}

	u.mu.Lock()
	defer u.mu.Unlock()

	u.sets = append(u.sets, s)
	u.lifetimeLocked(s, time.Now().Add(RegAwaitAuth))

	return s, nil
}

// lifetimeLocked sets the time the set is deleted at.
func (u *UE) lifetimeLocked(s *saSet, at time.Time) {
	s.expires = at

	if s.timer != nil {
		s.timer.Stop()
	}

	s.timer = time.AfterFunc(time.Until(at), func() {
		u.mu.Lock()
		defer u.mu.Unlock()

		if s.expires.Equal(at) {
			u.dropLocked(s)
		}
	})
}

// dropLocked forgets the set and deletes it from the kernel.
func (u *UE) dropLocked(s *saSet) {
	if s.removed {
		return
	}

	s.removed = true

	if s.timer != nil {
		s.timer.Stop()
	}

	u.sets = slices.DeleteFunc(u.sets, func(x *saSet) bool { return x == s })

	if err := u.removeSet(s); err != nil {
		u.log.Warn("failed to delete SAs", slog.String("set", s.set.String()), slog.Any("error", err))
	}
}

func (u *UE) drop(s *saSet) {
	if s == nil {
		return
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	u.dropLocked(s)
}

func (u *UE) removeSet(s *saSet) error {
	u.spis.Release(s.set.Local.SPIC, s.set.Local.SPIS)

	if u.cfg.Transport == sip.TCP {
		go u.layer.CloseFlow(protectedFlow(s, sip.TCP))
	}

	return u.cfg.Kernel.Remove(s.set)
}

func protectedFlow(s *saSet, tr sip.Transport) sip.Flow {
	return sip.Flow{
		Transport: tr,
		Local:     netip.AddrPortFrom(s.set.Local.Addr, s.set.Local.PortC),
		Remote:    netip.AddrPortFrom(s.set.Remote.Addr, s.set.Remote.PortS),
	}
}

func (u *UE) established() *saSet {
	u.mu.Lock()
	defer u.mu.Unlock()

	for _, s := range u.sets {
		if s.state == Established {
			return s
		}
	}

	return nil
}
