// Package hsstest is a fake HSS for tests: a passive Diameter node answering
// the Cx requests of the IMS from a table of subscribers, with Milenage
// vectors.
package hsstest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/milenage"
)

const imsPeer = "ims"

// The AMF on the IMS path (ims_integration).
var amf = []byte{0, 0}

type Config struct {
	Host  string
	Realm string

	// Address the HSS listens on, on a random port; loopback by default.
	Address netip.Addr

	// IMSHost and IMSRealm are the Origin-Host and Origin-Realm of the IMS,
	// which connects to the HSS. IMSRealm defaults to Realm.
	IMSHost  string
	IMSRealm string

	Logger *slog.Logger
}

type Subscriber struct {
	IMPI string
	IMSI string

	K   []byte
	OPc []byte
	SQN uint64

	// IMPUs is the implicit registration set.
	IMPUs []cx.ProfileIdentity

	ServerName string
	State      State
}

// State is the registration state of the IMPI's implicit set (TS 29.228
// §6.1.2): a stored ServerName with NotRegistered is an authentication
// pending.
type State int

const (
	NotRegistered State = iota
	Registered
	Unregistered
)

func (s Subscriber) has(impu string) bool {
	for _, p := range s.IMPUs {
		if strings.EqualFold(p.Identity, impu) {
			return true
		}
	}

	return false
}

func (s Subscriber) clone() Subscriber {
	s.K = append([]byte(nil), s.K...)
	s.OPc = append([]byte(nil), s.OPc...)
	s.IMPUs = append([]cx.ProfileIdentity(nil), s.IMPUs...)

	return s
}

// Request is one Cx request the HSS received; exactly one field is set.
type Request struct {
	UAR *cx.UserAuthorizationRequest
	MAR *cx.MultimediaAuthRequest
	SAR *cx.ServerAssignmentRequest
	LIR *cx.LocationInfoRequest
}

func (r Request) String() string {
	switch {
	case r.UAR != nil:
		return fmt.Sprintf("UAR %+v", *r.UAR)
	case r.MAR != nil:
		return fmt.Sprintf("MAR %+v", *r.MAR)
	case r.SAR != nil:
		return fmt.Sprintf("SAR %+v", *r.SAR)
	case r.LIR != nil:
		return fmt.Sprintf("LIR %+v", *r.LIR)
	}

	return "empty request"
}

type HSS struct {
	cfg  Config
	node *diameter.Node
	addr netip.AddrPort

	requests chan Request

	mu          sync.Mutex
	subscribers map[string]*Subscriber
}

func New(t testing.TB, cfg Config) *HSS {
	t.Helper()

	if !cfg.Address.IsValid() {
		cfg.Address = netip.MustParseAddr("127.0.0.1")
	}

	if cfg.Host == "" {
		cfg.Host = "hss." + cfg.Realm
	}

	if cfg.IMSRealm == "" {
		cfg.IMSRealm = cfg.Realm
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	h := &HSS{
		cfg:         cfg,
		requests:    make(chan Request, 1024),
		subscribers: make(map[string]*Subscriber),
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", netip.AddrPortFrom(cfg.Address, 0).String())
	if err != nil {
		t.Fatalf("hsstest: listen: %v", err)
	}

	h.addr = ln.Addr().(*net.TCPAddr).AddrPort()

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandUserAuthorization, diameter.HandlerFunc(h.userAuthorization))
	mux.Handle(cx.ApplicationID, cx.CommandMultimediaAuth, diameter.HandlerFunc(h.multimediaAuth))
	mux.Handle(cx.ApplicationID, cx.CommandServerAssignment, diameter.HandlerFunc(h.serverAssignment))
	mux.Handle(cx.ApplicationID, cx.CommandLocationInfo, diameter.HandlerFunc(h.locationInfo))

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      cfg.Host,
			OriginRealm:     cfg.Realm,
			HostIPAddresses: []netip.Addr{cfg.Address},
			ProductName:     "hsstest",
		},
		Handler: mux,
		Logger:  cfg.Logger,
	})
	if err != nil {
		t.Fatalf("hsstest: %v", err)
	}

	if err := node.SetPeers([]diameter.Peer{{
		ID:           imsPeer,
		Host:         cfg.IMSHost,
		Addresses:    []netip.Addr{cfg.Address},
		Transport:    diameter.TransportTCP,
		Applications: []diameter.Application{{ID: cx.ApplicationID, VendorID: tgpp.VendorID}},
		Passive:      true,
	}}); err != nil {
		t.Fatalf("hsstest: %v", err)
	}

	go func() { _ = node.Serve(diameter.NewTCPListener(ln.(*net.TCPListener))) }()

	h.node = node

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	return h
}

func (h *HSS) Host() string {
	return h.cfg.Host
}

func (h *HSS) Realm() string {
	return h.cfg.Realm
}

func (h *HSS) Addr() netip.AddrPort {
	return h.addr
}

// WaitConnected waits for the IMS to connect.
func (h *HSS) WaitConnected(t testing.TB) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for p, _ := h.node.Peer(imsPeer); p.State != diameter.PeerOpen; p, _ = h.node.Peer(imsPeer) {
		if time.Now().After(deadline) {
			t.Fatal("hsstest: timed out waiting for the IMS to connect")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (h *HSS) Add(s Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := s.clone()
	h.subscribers[s.IMPI] = &c
}

func (h *HSS) Subscriber(impi string) (Subscriber, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	s, ok := h.subscribers[impi]
	if !ok {
		return Subscriber{}, false
	}

	return s.clone(), true
}

func (h *HSS) Update(impi string, f func(*Subscriber)) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if s, ok := h.subscribers[impi]; ok {
		f(s)
	}
}

// Requests delivers the Cx requests in the order they arrived. Requests
// beyond its buffer are dropped.
func (h *HSS) Requests() <-chan Request {
	return h.requests
}

func (h *HSS) record(r Request) {
	select {
	case h.requests <- r:
	default:
	}
}

// Next waits for the next request.
func (h *HSS) Next(t testing.TB) Request {
	t.Helper()

	select {
	case r := <-h.requests:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("hsstest: timed out waiting for a Cx request")
	}

	return Request{}
}

// Drain discards the requests received so far.
func (h *HSS) Drain() {
	for {
		select {
		case <-h.requests:
		default:
			return
		}
	}
}

func experimental(req *diameter.Message, id diameter.Identity, code uint32) *diameter.Message {
	return cx.NewAnswer(req, id, tgpp.Experimental(code), 0)
}

func must(ans *diameter.Message, err error) *diameter.Message {
	if err != nil {
		panic(fmt.Sprintf("hsstest: build answer: %v", err))
	}

	return ans
}

// lookup finds the subscriber, answering USER_UNKNOWN or
// IDENTITIES_DONT_MATCH when it fails. Called with h.mu held.
func (h *HSS) lookup(req *diameter.Message, id diameter.Identity, impi, impu string) (*Subscriber, *diameter.Message) {
	s, ok := h.subscribers[impi]
	if !ok {
		return nil, experimental(req, id, tgpp.ResultErrorUserUnknown)
	}

	if impu != "" && !s.has(impu) {
		return nil, experimental(req, id, tgpp.ResultErrorIdentitiesDontMatch)
	}

	return s, nil
}

// userAuthorization follows TS 29.228 §6.1.1.1.
func (h *HSS) userAuthorization(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	uar, err := cx.ParseUserAuthorizationRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.record(Request{UAR: &uar})

	h.mu.Lock()
	defer h.mu.Unlock()

	s, ans := h.lookup(req, c.LocalIdentity(), uar.PrivateIdentity, uar.PublicIdentity)
	if ans != nil {
		return ans
	}

	var a cx.UserAuthorization

	switch {
	case uar.AuthorizationType == cx.AuthorizationDeregistration && s.State == NotRegistered:
		return experimental(req, c.LocalIdentity(), tgpp.ResultErrorIdentityNotRegistered)
	case uar.AuthorizationType == cx.AuthorizationDeregistration:
		a.ServerName = s.ServerName
	case s.ServerName != "":
		a.Result, a.ServerName = tgpp.Experimental(tgpp.ResultSubsequentRegistration), s.ServerName
	default:
		a.Result = tgpp.Experimental(tgpp.ResultFirstRegistration)
	}

	return must(cx.NewUserAuthorizationAnswer(req, c.LocalIdentity(), a))
}

// multimediaAuth follows TS 29.228 §6.3.1 and, for resynchronisation, TS
// 33.102 §6.3.5.
func (h *HSS) multimediaAuth(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	mar, err := cx.ParseMultimediaAuthRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.record(Request{MAR: &mar})

	h.mu.Lock()
	defer h.mu.Unlock()

	s, ans := h.lookup(req, c.LocalIdentity(), mar.PrivateIdentity, mar.PublicIdentity)
	if ans != nil {
		return ans
	}

	if mar.Scheme != cx.SchemeDigestAKAv1MD5 && mar.Scheme != cx.SchemeUnknown {
		return experimental(req, c.LocalIdentity(), tgpp.ResultErrorAuthSchemeNotSupported)
	}

	if mar.Resync != nil {
		if sqn, err := milenage.Resync(s.K, s.OPc, mar.Resync.RAND, mar.Resync.AUTS); err == nil {
			s.SQN = sqn
		}
	}

	r := make([]byte, milenage.RANDLen)
	_, _ = rand.Read(r)

	s.SQN = (s.SQN + 1) & milenage.MaxSQN

	v, err := milenage.GenerateVector(s.K, s.OPc, r, s.SQN, amf)
	if err != nil {
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	if mar.ServerName != "" && s.State == NotRegistered {
		s.ServerName = mar.ServerName
	}

	return must(cx.NewMultimediaAuthAnswer(req, c.LocalIdentity(), cx.MultimediaAuth{
		PublicIdentity: mar.PublicIdentity,
		Items: []cx.AuthItem{{
			ItemNumber: 1,
			Scheme:     cx.SchemeDigestAKAv1MD5,
			AKA:        &cx.AKAVector{RAND: v.RAND, AUTN: v.AUTN, XRES: v.XRES, CK: v.CK, IK: v.IK},
		}},
	}))
}

// serverAssignment follows TS 29.228 §6.1.2.1.
func (h *HSS) serverAssignment(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	sar, err := cx.ParseServerAssignmentRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.record(Request{SAR: &sar})

	h.mu.Lock()
	defer h.mu.Unlock()

	impu := ""
	if len(sar.PublicIdentities) > 0 {
		impu = sar.PublicIdentities[0]
	}

	s, ans := h.lookup(req, c.LocalIdentity(), sar.PrivateIdentity, impu)
	if ans != nil {
		return ans
	}

	userData := false

	switch sar.Type {
	case cx.AssignmentRegistration, cx.AssignmentReRegistration:
		s.ServerName, s.State, userData = sar.ServerName, Registered, !sar.UserDataAlreadyAvailable
	case cx.AssignmentUnregisteredUser:
		s.ServerName, s.State, userData = sar.ServerName, Unregistered, true
	case cx.AssignmentNoAssignment:
		userData = true
	case cx.AssignmentTimeoutDeregistrationStoreServer, cx.AssignmentUserDeregistrationStoreServer:
		s.State = Unregistered
	case cx.AssignmentAuthenticationFailure, cx.AssignmentAuthenticationTimeout:
		if s.State == NotRegistered {
			s.ServerName = ""
		}
	default:
		s.ServerName, s.State = "", NotRegistered
	}

	a := cx.ServerAssignment{PrivateIdentity: s.IMPI}

	if userData {
		if a.UserData, err = cx.MarshalUserData(s.subscription()); err != nil {
			panic(fmt.Sprintf("hsstest: user data for %s: %v", s.IMPI, err))
		}
	}

	return must(cx.NewServerAssignmentAnswer(req, c.LocalIdentity(), a))
}

func (s *Subscriber) subscription() cx.IMSSubscription {
	return cx.IMSSubscription{
		PrivateIdentity: s.IMPI,
		IMSI:            s.IMSI,
		ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: append([]cx.ProfileIdentity(nil), s.IMPUs...)}},
	}
}

// locationInfo follows TS 29.228 §6.1.4.1.
func (h *HSS) locationInfo(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	lir, err := cx.ParseLocationInfoRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.record(Request{LIR: &lir})

	h.mu.Lock()
	defer h.mu.Unlock()

	var s *Subscriber

	for _, sub := range h.subscribers {
		if sub.has(lir.PublicIdentity) {
			s = sub
			break
		}
	}

	switch {
	case s == nil:
		return experimental(req, c.LocalIdentity(), tgpp.ResultErrorUserUnknown)
	case s.State == Registered:
		return must(cx.NewLocationInfoAnswer(req, c.LocalIdentity(), cx.LocationInfo{ServerName: s.ServerName}))
	case s.State == Unregistered:
		return must(cx.NewLocationInfoAnswer(req, c.LocalIdentity(), cx.LocationInfo{
			Result: tgpp.Experimental(tgpp.ResultUnregisteredService), ServerName: s.ServerName,
		}))
	}

	return experimental(req, c.LocalIdentity(), tgpp.ResultErrorIdentityNotRegistered)
}

var ErrNotConnected = errors.New("hsstest: the IMS is not connected")

// RTR sends a Registration-Termination-Request for the private identity, or
// only the given public identities of it (TS 29.228 §6.1.3). On success
// without public identities, the subscriber is no longer registered.
func (h *HSS) RTR(ctx context.Context, reason cx.DeregistrationReason, impi string, impus ...string) (cx.RegistrationTermination, error) {
	if p, ok := h.node.Peer(imsPeer); !ok || p.State != diameter.PeerOpen {
		return cx.RegistrationTermination{}, ErrNotConnected
	}

	req, err := cx.NewRegistrationTerminationRequest(tgpp.Envelope{
		SessionID:        h.node.NewSessionID(),
		Origin:           h.node.Identity(),
		DestinationHost:  h.cfg.IMSHost,
		DestinationRealm: h.cfg.IMSRealm,
	}, cx.RegistrationTerminationRequest{
		PrivateIdentity:  impi,
		PublicIdentities: impus,
		Reason:           reason,
	})
	if err != nil {
		return cx.RegistrationTermination{}, err
	}

	ans, err := h.node.Do(ctx, imsPeer, req)
	if err != nil {
		return cx.RegistrationTermination{}, err
	}

	rta, err := cx.ParseRegistrationTerminationAnswer(ans)
	if err != nil {
		return rta, err
	}

	if len(impus) == 0 {
		h.Update(impi, func(s *Subscriber) { s.ServerName, s.State = "", NotRegistered })
	}

	return rta, nil
}
