// Package ue is an IMS UE (TS 24.229 §5.1): its registration with IMS AKA and the ipsec-3gpp security agreement
// (TS 33.203), its reg event subscription (TS 24.229 §5.1.1.3), and the IMS communication services it hosts on its
// agent, such as an MCPTT client (TS 24.379). The platform gives it the AKA of the UICC (Authenticator), its IPsec,
// and a hook on its sockets.
package ue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
	"github.com/ellanetworks/ims/sip/ua"
)

const (
	DefaultExpires = 600000 * time.Second

	RegAwaitAuth = 4 * time.Minute

	saMargin = 30 * time.Second

	maxInvalidChallenges = 2

	maxChallenges = 4

	// closeWait is how long Close waits for the agent to end the sessions of the services.
	closeWait = 500 * time.Millisecond
)

var regAwaitAuth = RegAwaitAuth

var (
	ErrNotRegistered = errors.New("ue: not registered")

	ErrNoSecurityServer = errors.New("ue: 401 without Security-Server")

	ErrNoAcceptableOffer = errors.New("ue: no acceptable Security-Server mechanism")

	ErrNetworkAuthentication = errors.New("ue: network authentication failed (MAC failure)")

	ErrTooManyChallenges = errors.New("ue: too many challenges")

	// ErrSynchronisation is the error of a registration whose challenges the UICC found out of sequence more often than
	// it resynchronised (TS 33.102 §6.3.5).
	ErrSynchronisation = errors.New("ue: AKA synchronisation failure")

	ErrClosed = errors.New("ue: closed")
)

type ResponseError struct {
	Response *sip.Response
}

func (e *ResponseError) Error() string {
	cseq, _ := e.Response.Header.CSeq()

	return "ue: " + cseq.Method + " answered " + e.Response.StartLine()
}

type Config struct {
	Identities Identities

	// InstanceID identifies the UE in its Contact (TS 24.229 §5.1.1.2.1, RFC 5626 §4.1): IMEIInstanceID for a UE with
	// an IMEI.
	InstanceID string

	// AKA runs the challenges of the registrations (TS 33.203 §6.1).
	AKA Authenticator

	// PCSCF is the P-CSCF the UE registers through (TS 24.229 §9.2.1), Local its address, Transport the transport of
	// its requests (UDP by default).
	PCSCF     netip.AddrPort
	Local     netip.Addr
	Transport sip.Transport

	Offers []Offer

	// ContactParams are added to the Contact the UE registers: a q-value (RFC 3261 §10.2.1.2)
	// or feature tags (RFC 3840 §9), other than those the UE sets itself: its instance, its reg-id, and the feature
	// tags of its services.
	ContactParams sip.Params

	// RegID, when not zero, makes the UE's registration flow RegID of its instance (RFC 5626 §4.2):
	// its Contact has the reg-id, and its REGISTER outbound in Supported (TS 24.229 §5.1.1.2.1). UEs
	// with the same IMPI and IMEI and other RegIDs are the device's other flows, each with its own
	// security associations (TS 33.203 §6.1).
	RegID int64

	// Plain is a UE without the security agreement: no SAs, all its requests unprotected.
	Plain bool

	// AccessNetworkInfo is the P-Access-Network-Info of the UE's requests and responses (TS 24.229 §5.1.1.2.1,
	// §7.2A.4): its access type and cell. Required without Plain.
	AccessNetworkInfo string

	Expires time.Duration

	NoRegEvent bool

	// Services are the services the UE hosts (Service): of the services an initial request prefers alike, the first its
	// Match takes gets it, else the first.
	Services []Service

	LargeUDP bool

	TCPResponsesFromClientPort bool

	T1 time.Duration

	// IPsec is where the UE installs the SAs of its registrations. Required without Plain.
	IPsec IPsec

	// Control runs on each socket of the UE before it is bound or connects, as net.ListenConfig's and net.Dialer's: to
	// bind it to a network, or protect it with SAs. address is the local address the socket is to be bound to, its
	// port 0 for any.
	Control func(network, address string, c syscall.RawConn) error

	// Clock runs the timers of the UE, of its registration and reg event subscription, and of the sessions of its
	// services: the system clock when nil.
	Clock transaction.Clock

	// Do runs the creation of the UE's sockets: in a network namespace.
	Do func(f func())

	Trace func(m sip.Message, f sip.Flow, sent bool)

	// OnEvent receives the events of the UE: the requests it receives and the responses to its own, the reg event
	// NOTIFYs, the errors of its automatic re-registrations and subscriptions. It must not block.
	OnEvent func(Event)

	Logger *slog.Logger
}

// reservedContactParams are the Contact parameters the UE sets whatever its services: its instance and reg-id, and
// the media type feature tags (RFC 3840 §10.1–§10.6).
var reservedContactParams = sip.Params{
	{Name: "+sip.instance"},
	{Name: "reg-id"},
	{Name: "audio"},
	{Name: "application"},
	{Name: "data"},
	{Name: "control"},
	{Name: "video"},
	{Name: "text"},
}

type State struct {
	Registered bool

	// Outbound is set when the registrar registered the UE's flow: its 200 required outbound
	// (RFC 5626 §6).
	Outbound bool

	Expires        time.Time
	DefaultIMPU    string
	AssociatedURIs []string
	ServiceRoute   []string

	Barred bool

	IMPUs []string
}

type Event struct {
	Request  *sip.Request
	Response *sip.Response
	Reginfo  *regevent.Reginfo
	Err      error
}

type saSet struct {
	set     SASet
	keys    Keys
	state   SAState
	expires time.Time
	timer   transaction.Timer
	server  []string
	removed bool

	ownsSPIs bool

	keepFlows bool
}

type client struct {
	portC      uint16
	spiC, spiS uint32
}

type UE struct {
	cfg    Config
	id     Identities
	user   string
	offers []Offer
	log    *slog.Logger

	agent       *ua.Agent
	layer       *transaction.Layer
	unprotected netip.AddrPort
	portS       uint16
	spis        *ipsec.SPIs

	op sync.Mutex

	mu          sync.Mutex
	closed      bool
	pcscf       netip.AddrPort
	clientPorts []uint16
	retired     []uint16
	pending     int
	newSAUsed   bool
	expires     time.Duration
	callID      string
	fromTag     string
	cseq        uint32
	auth        string
	state       State
	sets        []*saSet
	auto        bool
	reregTimer  transaction.Timer
	expiryTimer transaction.Timer
	sub         *subscription

	services []Service

	// serviceTags are the option tags of the services, which the agent accepts for any of them.
	serviceTags []string

	// The lock guards disabled, the services disabled now.
	disabled map[string]bool
	allow    []string
}

func New(cfg Config) (*UE, error) {
	if err := cfg.Identities.check(); err != nil {
		return nil, err
	}

	if cfg.InstanceID == "" {
		return nil, errors.New("ue: no instance ID")
	}

	if cfg.AKA == nil {
		return nil, errors.New("ue: no Authenticator")
	}

	if !cfg.PCSCF.IsValid() || !cfg.Local.IsValid() || cfg.PCSCF.Addr().Is4() != cfg.Local.Is4() {
		return nil, fmt.Errorf("ue: P-CSCF %s and local address %s", cfg.PCSCF, cfg.Local)
	}

	if !cfg.Plain && cfg.IPsec == nil {
		return nil, errors.New("ue: no IPsec to install the SAs")
	}

	if cfg.Transport == "" {
		cfg.Transport = sip.UDP
	}

	cfg.ContactParams = cfg.ContactParams.Clone()

	if cfg.Expires <= 0 {
		cfg.Expires = DefaultExpires
	}

	if cfg.AccessNetworkInfo == "" && !cfg.Plain {
		return nil, errors.New("ue: no AccessNetworkInfo")
	}

	if cfg.Do == nil {
		cfg.Do = func(f func()) { f() }
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	u := &UE{
		cfg:     cfg,
		id:      cfg.Identities,
		user:    timeUUID(),
		offers:  cfg.Offers,
		log:     cfg.Logger,
		spis:    ipsec.NewSPIs(),
		expires: cfg.Expires,
		auto:    true,
		pcscf:   cfg.PCSCF,
	}

	if len(u.offers) == 0 {
		u.offers = DefaultOffers()
	}

	u.services = slices.Clone(cfg.Services)

	if err := checkServices(u.services); err != nil {
		return nil, err
	}

	u.disabled = make(map[string]bool)
	for _, s := range u.services {
		u.disabled[s.Name] = s.Disabled
	}

	u.serviceTags = union(union(nil, u.services, func(s *Service) []string { return s.Supported }), u.services,
		func(s *Service) []string { return s.Understood })

	// The UE sets these itself (RFC 3840 §9, RFC 5626 §4.2): its instance, its reg-id, and the feature tags of its
	// services, their media types included, so that ContactParams claim no medium no service takes.
	all := make([]*Service, len(u.services))
	for i := range u.services {
		all[i] = &u.services[i]
	}

	own, err := featureSet(all).Params()
	if err != nil {
		return nil, fmt.Errorf("ue: %w", err)
	}

	for _, p := range cfg.ContactParams {
		if slices.ContainsFunc(append(own, reservedContactParams...), func(o sip.Param) bool { return strings.EqualFold(o.Name, p.Name) }) {
			return nil, fmt.Errorf("ue: ContactParams has %s, which the UE sets itself", p.Name)
		}
	}

	u.allow = union(ownAllow, u.services, func(s *Service) []string { return s.Allow })

	do := cfg.Do

	u.agent, err = ua.New(ua.Config{
		Transaction: transaction.Config{
			Logger:       cfg.Logger,
			Filter:       u.filter,
			ResponseFlow: u.responseFlow,
			T1:           cfg.T1,
			Clock:        cfg.Clock,
			Transport: transport.Config{
				Trace:    u.trace,
				LargeUDP: cfg.LargeUDP,
				Control:  cfg.Control,
				Dial: func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
					var (
						c   net.Conn
						err error
					)

					do(func() { c, err = d.DialContext(ctx, network, address) })

					return c, err
				},
			},
		},
		Prepare: u.prepare,
		Respond: u.respondHeaders,
		Policy:  ua.ClientPolicy,
		Allow:   u.allow,
		// The option tags of the services are accepted for all, then checked for the one a request is for; each
		// service advertises its own (Originate).
		Understood: u.serviceTags,
		// TS 24.229 §5.1.4.1: the UE as UAS lets the caller refresh.
		SessionTimer: ua.SessionTimer{Refresher: sip.RefresherUAC},
		OnInvite:     u.incomingInvite,
		OnRequest:    u.requestReceived,
		Logger:       cfg.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("ue: %w", err)
	}

	u.layer = u.agent.Layer()

	if u.unprotected, err = u.listen(0); err != nil {
		_ = u.agent.Shutdown(context.Background())
		return nil, err
	}

	return u, nil
}

// trace follows the messages of the UE: the received ones of a call are its events.
func (u *UE) trace(m sip.Message, f sip.Flow, sent bool) {
	if u.cfg.Trace != nil {
		u.cfg.Trace(m, f, sent)
	}
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
		return netip.AddrPort{}, fmt.Errorf("ue: %w", err)
	}

	return bound, nil
}

func (u *UE) IMPI() string       { return u.id.IMPI }
func (u *UE) IMPU() string       { return u.id.IMPU }
func (u *UE) HomeDomain() string { return u.id.HomeDomain }

// Local is the address of the UE.
func (u *UE) Local() netip.Addr { return u.cfg.Local }

func (u *UE) Unprotected() netip.AddrPort { return u.unprotected }

// SetPCSCF changes the P-CSCF of the UE's next initial registration (TS 24.229 §5.1.1.2.1): one of the P-CSCFs it
// discovered (§9.2.1) when the current one fails. The SAs and the requests of the current registration stay with the
// P-CSCF they were set up with.
func (u *UE) SetPCSCF(p netip.AddrPort) error {
	if !p.IsValid() || p.Addr().Is4() != u.cfg.Local.Is4() {
		return fmt.Errorf("ue: P-CSCF %s for local address %s", p, u.cfg.Local)
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	u.pcscf = p

	return nil
}

func (u *UE) pcscfAddr() netip.AddrPort {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.pcscf
}

// NewCallID is a new Call-ID of the UE (RFC 3261 §8.1.1.4): a time UUID at its address.
func (u *UE) NewCallID() string {
	return timeUUID() + "@" + sip.FormatHost(u.cfg.Local)
}

func (u *UE) event(e Event) {
	if u.cfg.OnEvent != nil {
		u.cfg.OnEvent(e)
	}
}

func (u *UE) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()

	s := u.state
	s.AssociatedURIs = slices.Clone(s.AssociatedURIs)
	s.ServiceRoute = slices.Clone(s.ServiceRoute)
	s.IMPUs = slices.Clone(s.IMPUs)

	return s
}

func (u *UE) SAs() []SA {
	u.mu.Lock()
	defer u.mu.Unlock()

	out := make([]SA, 0, len(u.sets))
	for _, s := range u.sets {
		out = append(out, SA{Set: s.set, Keys: s.keys, State: s.state, Expires: s.expires})
	}

	return out
}

func (u *UE) SetAutoReregister(on bool) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.auto = on

	if !on && u.reregTimer != nil {
		u.reregTimer.Stop()
		u.reregTimer = nil
	}

	if on && u.state.Registered {
		u.scheduleLocked(u.state.Expires.Sub(u.now()))
	}
}

// Close makes the UE vanish, as a UE losing its radio: the sessions of its services and its subscription end locally,
// nothing reaches the network. Shutdown ends them first.
func (u *UE) Close() error {
	if !u.close() {
		return nil
	}

	// The transport goes first: the agent ends the usages without signalling.
	_ = u.agent.Layer().Close()

	ctx, cancel := context.WithTimeout(context.Background(), closeWait)
	defer cancel()

	return u.release(u.agent.Shutdown(ctx))
}

// Shutdown ends the sessions of the services and the subscription of the UE with the network, a BYE, a CANCEL or an unsubscribe, waits
// for them until ctx is done, then closes it.
func (u *UE) Shutdown(ctx context.Context) error {
	if !u.close() {
		return nil
	}

	return u.release(u.agent.Shutdown(ctx))
}

func (u *UE) close() bool {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.closed {
		return false
	}

	u.closed = true
	u.stopTimersLocked()
	u.sub = nil

	return true
}

// release removes the SAs of the UE once its agent closed.
func (u *UE) release(err error) error {
	u.op.Lock()
	defer u.op.Unlock()

	u.mu.Lock()
	defer u.mu.Unlock()

	for _, s := range slices.Clone(u.sets) {
		u.dropLocked(s)
	}

	return err
}

func (u *UE) stopTimersLocked() {
	for _, t := range []*transaction.Timer{&u.reregTimer, &u.expiryTimer} {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}
}

var (
	errUnprotected = errors.New("ue: request on the unprotected port")
	errNoSA        = errors.New("ue: message on a protected port outside the SAs")
)

func (u *UE) filter(m sip.Message) error {
	if u.cfg.Plain {
		return nil
	}

	f := m.Env().Flow

	if f.Local.Port() == u.unprotected.Port() {
		if _, ok := m.(*sip.Request); ok {
			return errUnprotected
		}

		return nil
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	i := slices.IndexFunc(u.sets, func(s *saSet) bool { return inbound(s.set, f) })
	if i < 0 {
		return errNoSA
	}

	if u.sets[i].state == Established {
		u.newSAUsed = true
		u.dropOldLocked()
	}

	return nil
}

// TS 33.203 §7.1
func (u *UE) responseFlow(req *sip.Request, _ *sip.Response) (sip.Flow, bool, error) {
	f := req.Flow
	if u.cfg.Plain || f.Transport != sip.UDP && !u.cfg.TCPResponsesFromClientPort {
		return sip.Flow{}, false, nil
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	for _, s := range u.sets {
		if inbound(s.set, f) {
			return sip.Flow{
				Transport: f.Transport,
				Local:     netip.AddrPortFrom(f.Local.Addr(), s.set.Local.PortC),
				Remote:    netip.AddrPortFrom(s.set.Remote.Addr, s.set.Remote.PortS),
			}, true, nil
		}
	}

	return sip.Flow{}, false, nil
}

func (u *UE) dropOldLocked() {
	if !u.newSAUsed || u.pending > 0 {
		return
	}

	for _, s := range slices.Clone(u.sets) {
		if s.state == Old {
			u.dropLocked(s)
		}
	}
}

func inbound(s SASet, f sip.Flow) bool {
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
	u.mu.Lock()
	u.pending++
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		defer u.mu.Unlock()

		u.pending--
		u.dropOldLocked()
	}()

	w := &waiter{u: u, final: make(chan *sip.Response, 1), err: make(chan error, 1)}

	if _, err := u.layer.Request(req, w); err != nil {
		return nil, fmt.Errorf("ue: send %s: %w", req.Method, err)
	}

	select {
	case res := <-w.final:
		return res, nil
	case err := <-w.err:
		return nil, fmt.Errorf("ue: %s: %w", req.Method, err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (u *UE) newClient(last uint16) (client, error) {
	u.mu.Lock()

	port := uint16(0)

	for _, p := range u.clientPorts {
		if p != last && !slices.Contains(u.retired, p) && !slices.ContainsFunc(u.sets, func(s *saSet) bool { return s.set.Local.PortC == p }) {
			port = p
			break
		}
	}

	u.mu.Unlock()

	if port == 0 {
		bound, err := u.listen(0)
		if err != nil {
			return client{}, err
		}

		port = bound.Port()

		u.mu.Lock()
		u.clientPorts = append(u.clientPorts, port)
		u.mu.Unlock()
	}

	spiC, spiS, err := u.spis.Allocate()
	if err != nil {
		return client{}, fmt.Errorf("ue: %w", err)
	}

	return client{portC: port, spiC: spiC, spiS: spiS}, nil
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

func (u *UE) selectServer(res *sip.Response) (ipsec.Offer, []string, error) {
	values := res.Header.Values("Security-Server")
	if len(values) == 0 {
		return ipsec.Offer{}, nil, ErrNoSecurityServer
	}

	ms, err := res.Header.SecurityMechanisms("Security-Server")
	if err != nil {
		return ipsec.Offer{}, nil, fmt.Errorf("%w: %w", ErrNoSecurityServer, err)
	}

	usable := false

	for _, m := range ms {
		o, err := ipsec.ParseOffer(m)
		if errors.Is(err, ipsec.ErrUnsupportedOffer) {
			usable = true
		}

		if err != nil {
			continue
		}

		usable = true

		if slices.Contains(u.offers, Offer{Integrity(o.Integrity), Encryption(o.Encryption)}) {
			return o, values, nil
		}
	}

	if !usable {
		return ipsec.Offer{}, nil, fmt.Errorf("%w: no ipsec-3gpp mechanism with its parameters", ErrNoSecurityServer)
	}

	return ipsec.Offer{}, nil, ErrNoAcceptableOffer
}

func (u *UE) install(c client, o ipsec.Offer, keys Keys, server []string) (*saSet, error) {
	set := SASet{
		Local:      Endpoint{Addr: u.cfg.Local, PortC: c.portC, PortS: u.portS, SPIC: c.spiC, SPIS: c.spiS},
		Remote:     endpoint(o.Endpoint),
		Integrity:  Integrity(o.Integrity),
		Encryption: Encryption(o.Encryption),
	}
	set.Remote.Addr = u.pcscfAddr().Addr()

	u.mu.Lock()
	defer u.mu.Unlock()

	if u.closed {
		return nil, ErrClosed
	}

	if err := u.cfg.IPsec.Install(set, keys); err != nil {
		return nil, fmt.Errorf("ue: %w", err)
	}

	s := &saSet{set: set, keys: keys, state: Temporary, server: server}

	u.sets = append(u.sets, s)
	u.lifetimeLocked(s, u.now().Add(regAwaitAuth))

	return s, nil
}

func (u *UE) lifetimeLocked(s *saSet, at time.Time) {
	s.expires = at

	if s.timer != nil {
		s.timer.Stop()
	}

	s.timer = u.layer.Clock().AfterFunc(at.Sub(u.now()), func() {
		u.mu.Lock()
		defer u.mu.Unlock()

		if s.expires.Equal(at) {
			u.dropLocked(s)
		}
	})
}

func (u *UE) dropLocked(s *saSet) {
	if s.removed {
		return
	}

	s.removed = true

	if s.timer != nil {
		s.timer.Stop()
	}

	u.sets = slices.DeleteFunc(u.sets, func(x *saSet) bool { return x == s })
	u.retired = append(u.retired, s.set.Local.PortC)

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
	if s.ownsSPIs {
		u.spis.Release(s.set.Local.SPIC, s.set.Local.SPIS)
	}

	inbound := sip.Flow{
		Transport: sip.TCP,
		Local:     netip.AddrPortFrom(s.set.Local.Addr, s.set.Local.PortS),
		Remote:    netip.AddrPortFrom(s.set.Remote.Addr, s.set.Remote.PortC),
	}

	if !s.keepFlows {
		for _, f := range []sip.Flow{protectedFlow(s, sip.TCP), inbound} {
			go u.layer.CloseFlow(f)
		}
	}

	return u.cfg.IPsec.Remove(s.set)
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

// now is the time on the clock of the UE, the one of its agent and of all its timers.
func (u *UE) now() time.Time {
	return u.layer.Clock().Now()
}
