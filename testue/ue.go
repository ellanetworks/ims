package testue

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

	// closeWait is how long Close waits for the BYEs of the calls.
	closeWait = 500 * time.Millisecond
)

var regAwaitAuth = RegAwaitAuth

var (
	ErrNotRegistered = errors.New("testue: not registered")

	ErrNoSecurityServer = errors.New("testue: 401 without Security-Server")

	ErrNoAcceptableOffer = errors.New("testue: no acceptable Security-Server mechanism")

	ErrNetworkAuthentication = errors.New("testue: network authentication failed (MAC failure)")

	ErrTooManyChallenges = errors.New("testue: too many challenges")

	ErrClosed = errors.New("testue: closed")
)

type ResponseError struct {
	Response *sip.Response
}

func (e *ResponseError) Error() string {
	cseq, _ := e.Response.Header.CSeq()

	return "testue: " + cseq.Method + " answered " + e.Response.StartLine()
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
	IMSI       string
	MNCLength  int
	IMPI       string
	IMPU       string
	HomeDomain string

	K   []byte
	OPc []byte

	SQN uint64

	IMEI string

	PCSCF     netip.AddrPort
	Local     netip.Addr
	Transport sip.Transport

	Offers []Offer

	// ContactParams are added to the Contact the UE registers: a q-value (RFC 3261 §10.2.1.2)
	// or feature tags (RFC 3840 §9), other than those the UE sets itself: its instance, ICSI, SMS and media
	// feature tags, and its reg-id.
	ContactParams sip.Params

	// Video makes the UE video capable (IR.94): it registers with the video feature tag, takes the video streams it
	// is offered, and can make video calls (CallOptions.Video) and add video to a call (Call.AddVideo). Without it,
	// the UE is voice only and declines video with port 0 (RFC 3264 §6).
	Video bool

	// RegID, when not zero, makes the UE's registration flow RegID of its instance (RFC 5626 §4.2):
	// its Contact has the reg-id, and its REGISTER outbound in Supported (TS 24.229 §5.1.1.2.1). UEs
	// with the same IMPI and IMEI and other RegIDs are the device's other flows, each with its own
	// security associations (TS 33.203 §6.1).
	RegID int64

	Plain bool

	AccessNetworkInfo string

	Expires time.Duration

	NoRegEvent bool

	AcceptCalls bool

	// NoPreconditions is a UE with preconditions disabled (TS 24.229 §5.1.5A): an INVITE requiring them gets a 420,
	// and its calls run without them.
	NoPreconditions bool

	EarlyMedia string

	LargeUDP bool

	TCPResponsesFromClientPort bool

	T1 time.Duration

	Kernel Kernel

	// Clock runs the timers of the calls and of the reg event subscription, the system clock when nil: tests
	// advance a fake one.
	Clock transaction.Clock

	Do func(f func())

	Trace func(m sip.Message, f sip.Flow, sent bool)

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
	set     ipsec.Set
	keys    ipsec.Keys
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
	cfg      Config
	id       identities
	instance string
	user     string
	offers   []Offer
	log      *slog.Logger

	agent       *ua.Agent
	layer       *transaction.Layer
	unprotected netip.AddrPort
	portS       uint16
	spis        *ipsec.SPIs
	events      chan Event

	op sync.Mutex

	mu          sync.Mutex
	closed      bool
	dropped     int
	clientPorts []uint16
	retired     []uint16
	pending     int
	newSAUsed   bool
	sqn         uint64
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

	calls struct {
		incoming chan *Call
		next     int
	}
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

	// The UE sets these itself: its media feature tags follow Video (RFC 3840 §9).
	for _, p := range cfg.ContactParams {
		if slices.Contains(ownContactParams, strings.ToLower(p.Name)) {
			return nil, fmt.Errorf("testue: ContactParams has %s, which the UE sets itself", p.Name)
		}
	}

	cfg.ContactParams = cfg.ContactParams.Clone()

	if cfg.Expires <= 0 {
		cfg.Expires = DefaultExpires
	}

	if cfg.AccessNetworkInfo == "" && !cfg.Plain {
		if id.mcc == "" {
			return nil, errors.New("testue: no AccessNetworkInfo, and no IMSI to derive it from")
		}

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

	u.calls.incoming = make(chan *Call, 16)

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
		Allow:   allowed,
		// TS 24.229 §5.1.3.1 b: precondition is advertised for the calls that run them.
		Understood: understood(cfg),
		// TS 24.229 §5.1.4.1: the UE as UAS lets the caller refresh.
		SessionTimer: ua.SessionTimer{Refresher: sip.RefresherUAC},
		OnInvite:     u.incomingCall,
		OnRequest:    u.requestReceived,
		Logger:       cfg.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("testue: %w", err)
	}

	u.layer = u.agent.Layer()

	if u.unprotected, err = u.listen(0); err != nil {
		_ = u.agent.Close(context.Background())
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

// requestReceived answers a request outside a dialog other than INVITE: MESSAGE, OPTIONS.
func (u *UE) requestReceived(r *ua.Request) {
	u.event(Event{Request: r.Message().Clone()})

	rp := ua.Reply{Code: 200}
	if r.Message().Method == "OPTIONS" {
		// RFC 3261 §11.2: the capabilities of the UE.
		rp.Header = u.agent.Capabilities()
	}

	if err := r.Respond(rp); err != nil {
		u.log.Debug("response failed", slog.Any("error", err))
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
		return netip.AddrPort{}, fmt.Errorf("testue: %w", err)
	}

	return bound, nil
}

func (u *UE) IMPI() string       { return u.id.impi }
func (u *UE) IMPU() string       { return u.id.impu }
func (u *UE) HomeDomain() string { return u.id.domain }

func (u *UE) Unprotected() netip.AddrPort { return u.unprotected }

func (u *UE) Events() <-chan Event { return u.events }

func (u *UE) Dropped() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.dropped
}

func (u *UE) event(e Event) {
	select {
	case u.events <- e:
	default:
		u.mu.Lock()
		u.dropped++
		u.mu.Unlock()
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

func (u *UE) SQN() uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.sqn
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

// Close makes the UE vanish, as a phone losing its radio: its calls and subscription end locally, nothing reaches the
// network. Shutdown ends them first.
func (u *UE) Close() error {
	if !u.close() {
		return nil
	}

	// The transport goes first: the agent ends the usages without signalling.
	_ = u.agent.Layer().Close()

	ctx, cancel := context.WithTimeout(context.Background(), closeWait)
	defer cancel()

	return u.release(u.agent.Close(ctx))
}

// Shutdown ends the calls and the subscription of the UE with the network, a BYE, a CANCEL or an unsubscribe, waits
// for them until ctx is done, then closes it.
func (u *UE) Shutdown(ctx context.Context) error {
	if !u.close() {
		return nil
	}

	return u.release(u.agent.Close(ctx))
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
	errUnprotected = errors.New("testue: request on the unprotected port")
	errNoSA        = errors.New("testue: message on a protected port outside the SAs")
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
		return nil, fmt.Errorf("testue: send %s: %w", req.Method, err)
	}

	select {
	case res := <-w.final:
		return res, nil
	case err := <-w.err:
		return nil, fmt.Errorf("testue: %s: %w", req.Method, err)
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
		return client{}, fmt.Errorf("testue: %w", err)
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

		if slices.Contains(u.offers, Offer{o.Integrity, o.Encryption}) {
			return o, values, nil
		}
	}

	if !usable {
		return ipsec.Offer{}, nil, fmt.Errorf("%w: no ipsec-3gpp mechanism with its parameters", ErrNoSecurityServer)
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

	u.mu.Lock()
	defer u.mu.Unlock()

	if u.closed {
		return nil, ErrClosed
	}

	if err := u.cfg.Kernel.Install(set, keys); err != nil {
		return nil, fmt.Errorf("testue: %w", err)
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

// understood are the option tags the UE accepts in Require without advertising them in every message.
func understood(cfg Config) []string {
	if cfg.NoPreconditions {
		return nil
	}

	return []string{sip.OptionTagPrecondition}
}

// now is the time on the clock of the UE, the one of its agent and of all its timers.
func (u *UE) now() time.Time {
	return u.layer.Clock().Now()
}
