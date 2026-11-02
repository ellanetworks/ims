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
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
)

const (
	DefaultExpires = 600000 * time.Second

	RegAwaitAuth = 4 * time.Minute

	saMargin = 30 * time.Second

	maxInvalidChallenges = 2

	maxChallenges = 4

	closeWait = 500 * time.Millisecond
)

var regAwaitAuth = RegAwaitAuth

var (
	ErrNotRegistered = errors.New("ue: not registered")

	ErrNoSecurityServer = errors.New("ue: 401 without Security-Server")

	ErrNoAcceptableOffer = errors.New("ue: no acceptable Security-Server mechanism")

	ErrNetworkAuthentication = errors.New("ue: network authentication failed (MAC failure)")

	ErrTooManyChallenges = errors.New("ue: too many challenges")

	// TS 33.102 clause 6.3.5
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

	InstanceID string

	AKA Authenticator

	PCSCF     netip.AddrPort
	Local     netip.Addr
	Transport sip.Transport

	Offers []Offer

	ContactParams sip.Params

	RegID int64

	Plain bool

	AccessNetworkInfo string

	Expires time.Duration

	NoRegEvent bool

	Services []Service

	LargeUDP bool

	TCPResponsesFromClientPort bool

	T1 time.Duration

	IPsec IPsec

	Control func(network, address string, c syscall.RawConn) error

	Clock transaction.Clock

	Do func(f func())

	Trace func(m sip.Message, f sip.Flow, sent bool)

	OnEvent func(Event)

	TracerProvider trace.TracerProvider

	Logger *slog.Logger
}

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

	Outbound bool

	Expires        time.Time
	DefaultIMPU    string
	AssociatedURIs []string
	ServiceRoute   []string

	Barred bool

	IMPUs []string

	UnavailablePCSCFs []netip.AddrPort
}

type Event struct {
	Request  *sip.Request
	Response *sip.Response
	Reginfo  *reginfo.Reginfo
	Err      error

	Deregistered Deregistration
	RetryAfter   time.Duration

	UnavailablePCSCF netip.AddrPort
}

// RFC 3680 section 5.1
// TS 24.229 clause 5.1.1.7
type Deregistration string

const (
	// TS 24.229 clause 5.4.1.2.2
	DeregisteredUnregistered Deregistration = "unregistered"

	// TS 24.229 clause 5.1.1.7
	DeregisteredDeactivated Deregistration = "deactivated"

	// TS 24.229 clause 5.1.1.7
	DeregisteredRejected Deregistration = "rejected"

	// RFC 3680 section 5.1
	DeregisteredProbation Deregistration = "probation"

	DeregisteredExpired Deregistration = "expired"

	// TS 24.229 clause 5.1.1.4.1
	// TS 24.229 clause 5.1.2A.1.6
	// RFC 5626 section 4.4
	DeregisteredFlowFailed Deregistration = "flow-failed"
)

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

	op sync.Mutex

	mu            sync.Mutex
	closed        bool
	pcscf         netip.AddrPort
	marks         map[netip.AddrPort]pcscfMark
	registeredVia netip.AddrPort
	ani           string
	clientPorts   []uint16
	retired       []uint16
	failed        []uint16
	pending       int
	newSAUsed     bool
	expires       time.Duration
	callID        string
	fromTag       string
	cseq          uint32
	auth          string
	state         State
	sets          []*saSet
	auto          bool
	reregTimer    transaction.Timer
	expiryTimer   transaction.Timer
	keepTimer     transaction.Timer
	flowTimer     time.Duration
	sub           *subscription

	services []Service

	serviceTags []string

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
		expires: cfg.Expires,
		auto:    true,
		pcscf:   cfg.PCSCF,
		ani:     cfg.AccessNetworkInfo,
		marks:   map[netip.AddrPort]pcscfMark{},
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
			Logger:         cfg.Logger,
			TracerProvider: cfg.TracerProvider,
			Filter:         u.filter,
			ResponseFlow:   u.responseFlow,
			T1:             cfg.T1,
			Clock:          cfg.Clock,
			Transport: transport.Config{
				Trace:    u.trace,
				LargeUDP: cfg.LargeUDP,
				Control:  u.control,
				Dial: func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
					var (
						c   net.Conn
						err error
					)

					do(func() { c, err = d.DialContext(ctx, network, address) })

					return c, err
				},
			},
			OnListenerFailed: u.listenerFailed,
			OnConnectionLost: u.connectionLost,
			OnClientError:    u.requestFailed,
		},
		Prepare:      u.prepare,
		Respond:      u.respondHeaders,
		Policy:       ua.ClientPolicy,
		Allow:        u.allow,
		Understood:   u.serviceTags,
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

func (u *UE) Local() netip.Addr { return u.cfg.Local }

func (u *UE) Unprotected() netip.AddrPort { return u.unprotected }

func (u *UE) SetPCSCF(p netip.AddrPort) error {
	if !p.IsValid() || p.Addr().Is4() != u.cfg.Local.Is4() {
		return fmt.Errorf("ue: P-CSCF %s for local address %s", p, u.cfg.Local)
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	u.pcscf = p

	return nil
}

// TS 24.229 clause 7.2A.4
func (u *UE) SetAccessNetworkInfo(ani string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.ani = ani
}

func (u *UE) accessNetworkInfo() string {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.ani
}

func (u *UE) pcscfAddr() netip.AddrPort {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.pcscf
}

// RFC 3261 section 8.1.1.4
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
	s.UnavailablePCSCFs = u.unavailableLocked()

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

func (u *UE) Close() error {
	if !u.close() {
		return nil
	}

	_ = u.agent.Layer().Close()

	ctx, cancel := context.WithTimeout(context.Background(), closeWait)
	defer cancel()

	return u.release(u.agent.Shutdown(ctx))
}

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
	u.stopKeepaliveLocked()
	u.sub = nil

	return true
}

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

// TS 33.203 clause 7.1
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

func (w *waiter) HandleResponse(_ context.Context, res *sip.Response) {
	w.u.event(Event{Response: res})

	if res.StatusCode >= 200 {
		select {
		case w.final <- res:
		default:
		}
	}
}

func (w *waiter) HandleError(_ context.Context, err error) {
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

	if _, err := u.layer.Request(ctx, req, w); err != nil {
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

	spiC, err := u.cfg.IPsec.AllocateSPI(u.cfg.Local)
	if err != nil {
		return client{}, fmt.Errorf("ue: %w", err)
	}

	spiS, err := u.cfg.IPsec.AllocateSPI(u.cfg.Local)
	if err != nil {
		u.cfg.IPsec.ReleaseSPI(spiC)
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

	for _, port := range []uint16{set.Local.PortC, set.Local.PortS} {
		if err := u.protectSockets(set, port); err != nil {
			_ = u.cfg.IPsec.Remove(set)
			return nil, fmt.Errorf("ue: %w", err)
		}
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

	err := u.cfg.IPsec.Remove(s.set)

	if s.ownsSPIs {
		u.cfg.IPsec.ReleaseSPI(s.set.Local.SPIC)
		u.cfg.IPsec.ReleaseSPI(s.set.Local.SPIS)
	}

	return err
}

func (u *UE) protectSockets(set SASet, port uint16) error {
	rcs, err := u.layer.Sockets(netip.AddrPortFrom(u.cfg.Local, port))
	if err != nil {
		return err
	}

	for _, rc := range rcs {
		if err := u.cfg.IPsec.Protect(set, port, rc); err != nil {
			return err
		}
	}

	return nil
}

// TS 33.203 clause 7.1
func (u *UE) control(network, address string, c syscall.RawConn) error {
	if u.cfg.Control != nil {
		if err := u.cfg.Control(network, address, c); err != nil {
			return err
		}
	}

	ap, err := netip.ParseAddrPort(address)
	if err != nil || ap.Port() == 0 || u.cfg.Plain {
		return nil
	}

	u.mu.Lock()

	var set *SASet

	for i := len(u.sets) - 1; i >= 0; i-- {
		if u.sets[i].set.Local.PortC == ap.Port() {
			set = &u.sets[i].set
			break
		}
	}

	u.mu.Unlock()

	if set == nil {
		return nil
	}

	return u.cfg.IPsec.Protect(*set, ap.Port(), c)
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

func (u *UE) now() time.Time {
	return u.layer.Clock().Now()
}
