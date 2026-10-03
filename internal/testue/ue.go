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
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

const (
	DefaultExpires = 600000 * time.Second

	RegAwaitAuth = 4 * time.Minute

	saMargin = 30 * time.Second

	maxInvalidChallenges = 2

	maxChallenges = 4
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

	Plain bool

	AccessNetworkInfo string

	Expires time.Duration

	NoRegEvent bool

	AcceptCalls bool

	T1 time.Duration

	Kernel Kernel

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
	timer   *time.Timer
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
	reregTimer  *time.Timer
	expiryTimer *time.Timer
	sub         *subscription

	calls struct {
		active   map[callKey]*Call
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

	u.calls.active = make(map[callKey]*Call)
	u.calls.incoming = make(chan *Call, 16)

	do := cfg.Do

	u.layer = transaction.New(transaction.Config{
		Handler:      u,
		Logger:       cfg.Logger,
		Filter:       u.filter,
		ResponseFlow: u.responseFlow,
		T1:           cfg.T1,
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
		u.scheduleLocked(time.Until(u.state.Expires))
	}
}

func (u *UE) Close() error {
	u.mu.Lock()

	if u.closed {
		u.mu.Unlock()
		return nil
	}

	u.closed = true
	u.stopTimersLocked()

	if u.sub != nil {
		u.stopSubscriptionLocked(u.sub)
	}

	calls := make([]*Call, 0, len(u.calls.active))
	for _, c := range u.calls.active {
		calls = append(calls, c)
	}

	u.mu.Unlock()

	for _, c := range calls {
		c.terminate(Closed)
	}

	err := u.layer.Close()

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
	for _, t := range []**time.Timer{&u.reregTimer, &u.expiryTimer} {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}
}

func (u *UE) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if req.Method == "NOTIFY" {
		u.notify(tx, req)
		return
	}

	u.event(Event{Request: req})

	switch req.Method {
	case "INVITE", "PRACK", "UPDATE", "BYE":
		if u.callRequest(tx, req) {
			return
		}
	}

	code := 200
	if req.Method == "INVITE" {
		code = 486
	}

	_ = tx.Respond(sip.NewResponse(req, code, ""))
}

func (u *UE) callRequest(tx *transaction.ServerTransaction, req *sip.Request) bool {
	to, err := req.Header.To()
	if err != nil {
		return false
	}

	if to.Tag() == "" {
		if req.Method != "INVITE" || !u.cfg.AcceptCalls {
			return false
		}

		u.incomingCall(tx, req)

		return true
	}

	if c := u.call(req.Header.CallID(), to.Tag()); c != nil {
		c.requestReceived(tx, req)
	} else {
		_ = tx.Respond(sip.NewResponse(req, 481, ""))
	}

	return true
}

func (u *UE) call(callID, tag string) *Call {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.calls.active[callKey{callID: callID, tag: tag}]
}

func (u *UE) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	if c := u.call(tx.Request().Header.CallID(), tx.ToTag()); c != nil {
		c.cancelReceived(cancel)
	}
}

func (u *UE) HandleAck(ack *sip.Request) {
	to, err := ack.Header.To()
	if err != nil {
		return
	}

	if c := u.call(ack.Header.CallID(), to.Tag()); c != nil {
		c.ackReceived(ack)
	}
}

func (u *UE) HandleTransactionError(*transaction.ServerTransaction, error) {}

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

func (u *UE) responseFlow(req *sip.Request, _ *sip.Response) (sip.Flow, bool, error) {
	f := req.Flow
	if u.cfg.Plain || f.Transport != sip.UDP {
		return sip.Flow{}, false, nil
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	for _, s := range u.sets {
		if inbound(s.set, f) {
			return sip.Flow{
				Transport: sip.UDP,
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
	u.lifetimeLocked(s, time.Now().Add(regAwaitAuth))

	return s, nil
}

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
