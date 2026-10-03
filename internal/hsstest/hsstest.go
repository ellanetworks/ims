package hsstest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/sip"
)

const imsPeer = "ims"

var amf = []byte{0, 0}

type Config struct {
	Host  string
	Realm string

	Address netip.Addr

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

	IMPUs []cx.ProfileIdentity

	UnregisteredServices bool

	ServerName string
	State      State

	AuthPending bool

	ReassignPending bool
}

type State int

const (
	NotRegistered State = iota
	Registered

	Unregistered
)

func (s State) String() string {
	switch s {
	case NotRegistered:
		return "not registered"
	case Registered:
		return "registered"
	case Unregistered:
		return "unregistered"
	}

	return fmt.Sprintf("State(%d)", int(s))
}

func (s Subscriber) has(impu string) bool {
	key := identityKey(impu)
	return slices.ContainsFunc(s.IMPUs, func(p cx.ProfileIdentity) bool { return identityKey(p.Identity) == key })
}

// identityKey compares public identities the way an HSS serving numbers does:
// a global number is the same identity as a tel URI and as a SIP URI with
// user=phone, whatever its visual separators. A real HSS may match only the
// identities it was provisioned with (the LIR contract with Ella Core in
// ims_integration.md), which this fake is more lenient than.
func identityKey(s string) string {
	u, err := sip.ParseURI(s)
	if err != nil {
		return strings.ToLower(s)
	}

	number := u.User
	if user, _ := u.Params.Get("user"); u.IsSIP() && strings.EqualFold(user, "phone") {
		number, _, _ = strings.Cut(number, ";")
	} else if !u.IsTel() {
		number = ""
	}

	number = strings.Map(func(c rune) rune {
		if strings.ContainsRune("-.()", c) {
			return -1
		}

		return c
	}, number)

	if strings.HasPrefix(number, "+") {
		return "tel:" + number
	}

	return strings.ToLower(s)
}

func (s Subscriber) allBarred() bool {
	return !slices.ContainsFunc(s.IMPUs, func(p cx.ProfileIdentity) bool { return !p.Barred })
}

func (s Subscriber) clone() Subscriber {
	s.K = append([]byte(nil), s.K...)
	s.OPc = append([]byte(nil), s.OPc...)
	s.IMPUs = append([]cx.ProfileIdentity(nil), s.IMPUs...)

	return s
}

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
	order       []string
	dropped     int
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
		cfg.Logger = slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	h := &HSS{
		cfg:         cfg,
		requests:    make(chan Request, 1024),
		subscribers: make(map[string]*Subscriber),
	}

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandUserAuthorization, diameter.HandlerFunc(h.userAuthorization))
	mux.Handle(cx.ApplicationID, cx.CommandMultimediaAuth, diameter.HandlerFunc(h.multimediaAuth))
	mux.Handle(cx.ApplicationID, cx.CommandServerAssignment, diameter.HandlerFunc(h.serverAssignment))
	mux.Handle(cx.ApplicationID, cx.CommandLocationInfo, diameter.HandlerFunc(h.locationInfo))

	h.node, h.addr = diametertest.Listen(t, diametertest.Config{
		Identity: diameter.Identity{
			OriginHost:      cfg.Host,
			OriginRealm:     cfg.Realm,
			HostIPAddresses: []netip.Addr{cfg.Address},
			ProductName:     "hsstest",
		},
		Peer: diameter.Peer{
			ID:           imsPeer,
			Host:         cfg.IMSHost,
			Applications: []diameter.Application{{ID: cx.ApplicationID, VendorID: tgpp.VendorID}},
		},
		Handler: mux,
		Logger:  cfg.Logger,
	})

	t.Cleanup(func() {
		if n := h.Dropped(); n > 0 {
			t.Errorf("hsstest: %d Cx requests dropped from the full request channel", n)
		}
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

func (h *HSS) WaitConnected(t testing.TB) {
	t.Helper()

	diametertest.WaitOpen(t, h.node, imsPeer)
}

func (h *HSS) Add(s Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := s.clone()

	if _, ok := h.subscribers[s.IMPI]; !ok {
		h.order = append(h.order, s.IMPI)
	}

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

func (h *HSS) Requests() <-chan Request {
	return h.requests
}

func (h *HSS) Dropped() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.dropped
}

func (h *HSS) recordLocked(r Request) {
	select {
	case h.requests <- r:
	default:
		h.dropped++
	}
}

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

func base(req *diameter.Message, id diameter.Identity, code uint32) *diameter.Message {
	return cx.NewAnswer(req, id, tgpp.Result{Code: code}, 0)
}

func must(ans *diameter.Message, err error) *diameter.Message {
	if err != nil {
		panic(fmt.Sprintf("hsstest: build answer: %v", err))
	}

	return ans
}

func (h *HSS) knownIMPU(impu string) bool {
	return slices.ContainsFunc(h.order, func(impi string) bool { return h.subscribers[impi].has(impu) })
}

func (h *HSS) lookup(req *diameter.Message, id diameter.Identity, impi, impu string) (*Subscriber, *diameter.Message) {
	s, ok := h.subscribers[impi]
	if !ok || impu != "" && !h.knownIMPU(impu) {
		return nil, experimental(req, id, tgpp.ResultErrorUserUnknown)
	}

	if impu != "" && !s.has(impu) {
		return nil, experimental(req, id, tgpp.ResultErrorIdentitiesDontMatch)
	}

	return s, nil
}

func (h *HSS) userAuthorization(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	uar, err := cx.ParseUserAuthorizationRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.recordLocked(Request{UAR: &uar})

	id := c.LocalIdentity()

	s, ans := h.lookup(req, id, uar.PrivateIdentity, uar.PublicIdentity)
	if ans != nil {
		return ans
	}

	if s.allBarred() {
		return base(req, id, diameter.ResultAuthorizationRejected)
	}

	var a cx.UserAuthorization

	switch {
	case uar.AuthorizationType == cx.AuthorizationRegistrationAndCapabilities:
		s.ReassignPending = s.ServerName != ""
	case uar.AuthorizationType == cx.AuthorizationDeregistration && s.State == NotRegistered && !s.AuthPending:
		return experimental(req, id, tgpp.ResultErrorIdentityNotRegistered)
	case uar.AuthorizationType == cx.AuthorizationDeregistration:
		a.ServerName = s.ServerName
	case s.ServerName != "":
		a.Result, a.ServerName = tgpp.Experimental(tgpp.ResultSubsequentRegistration), s.ServerName
	default:
		a.Result = tgpp.Experimental(tgpp.ResultFirstRegistration)
	}

	return must(cx.NewUserAuthorizationAnswer(req, id, a))
}

func (h *HSS) multimediaAuth(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	mar, err := cx.ParseMultimediaAuthRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.recordLocked(Request{MAR: &mar})

	id := c.LocalIdentity()

	s, ans := h.lookup(req, id, mar.PrivateIdentity, mar.PublicIdentity)
	if ans != nil {
		return ans
	}

	if mar.Scheme != cx.SchemeDigestAKAv1MD5 {
		return experimental(req, id, tgpp.ResultErrorAuthSchemeNotSupported)
	}

	if mar.Resync != nil && (s.ServerName == "" || s.ServerName == mar.ServerName) {
		if sqn, err := milenage.Resync(s.K, s.OPc, mar.Resync.RAND, mar.Resync.AUTS); err == nil {
			s.SQN = sqn
		}
	}

	r := make([]byte, milenage.RANDLen)
	_, _ = rand.Read(r)

	s.SQN = (s.SQN + 1) & milenage.MaxSQN

	v, err := milenage.GenerateVector(s.K, s.OPc, r, s.SQN, amf)
	if err != nil {
		return base(req, id, diameter.ResultUnableToComply)
	}

	if mar.ServerName != s.ServerName {
		s.ServerName, s.ReassignPending = mar.ServerName, false
	}

	s.AuthPending = true

	return must(cx.NewMultimediaAuthAnswer(req, id, cx.MultimediaAuth{
		PublicIdentity: mar.PublicIdentity,
		Items: []cx.AuthItem{{
			ItemNumber: 1,
			Scheme:     cx.SchemeDigestAKAv1MD5,
			AKA:        &cx.AKAVector{RAND: v.RAND, AUTN: v.AUTN, XRES: v.XRES, CK: v.CK, IK: v.IK},
		}},
	}))
}

func (h *HSS) serverAssignment(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	sar, err := cx.ParseServerAssignmentRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.recordLocked(Request{SAR: &sar})

	id := c.LocalIdentity()

	for _, impu := range sar.PublicIdentities {
		if _, ans := h.lookup(req, id, sar.PrivateIdentity, impu); ans != nil {
			return ans
		}
	}

	s, ans := h.lookup(req, id, sar.PrivateIdentity, "")
	if ans != nil {
		return ans
	}

	other := s.ServerName != "" && s.ServerName != sar.ServerName && !s.ReassignPending
	userData := false

	switch sar.Type {
	case cx.AssignmentRegistration, cx.AssignmentReRegistration, cx.AssignmentUnregisteredUser:
		if other {
			return must(cx.NewServerAssignmentErrorAnswer(req, id, cx.ServerAssignmentError{
				ResultError: cx.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorIdentityAlreadyRegistered)},
				ServerName:  s.ServerName,
			}))
		}

		s.ServerName, s.ReassignPending = sar.ServerName, false
		s.State, userData = Registered, !sar.UserDataAlreadyAvailable

		if sar.Type == cx.AssignmentUnregisteredUser {
			s.State, userData = Unregistered, true
		} else {
			s.AuthPending = false
		}
	case cx.AssignmentNoAssignment:
		if other {
			return base(req, id, diameter.ResultUnableToComply)
		}

		userData = true
	case cx.AssignmentTimeoutDeregistrationStoreServer, cx.AssignmentUserDeregistrationStoreServer:
		if s.State == Registered {
			s.State = Unregistered
		}
	case cx.AssignmentAuthenticationFailure, cx.AssignmentAuthenticationTimeout:
		if s.State == NotRegistered {
			s.ServerName = ""
		}

		s.AuthPending = false
	case cx.AssignmentTimeoutDeregistration, cx.AssignmentUserDeregistration,
		cx.AssignmentAdministrativeDeregistration, cx.AssignmentDeregistrationTooMuchData:
		s.ServerName, s.State, s.AuthPending, s.ReassignPending = "", NotRegistered, false, false
	default:
		return experimental(req, id, tgpp.ResultErrorInAssignmentType)
	}

	a := cx.ServerAssignment{PrivateIdentity: s.IMPI}

	if userData {
		if a.UserData, err = cx.MarshalUserData(s.subscription()); err != nil {
			panic(fmt.Sprintf("hsstest: user data for %s: %v", s.IMPI, err))
		}
	}

	return must(cx.NewServerAssignmentAnswer(req, id, a))
}

func (s *Subscriber) subscription() cx.IMSSubscription {
	return cx.IMSSubscription{
		PrivateIdentity: s.IMPI,
		IMSI:            s.IMSI,
		ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: append([]cx.ProfileIdentity(nil), s.IMPUs...)}},
	}
}

func (h *HSS) locationInfo(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	lir, err := cx.ParseLocationInfoRequest(req)
	if err != nil {
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.recordLocked(Request{LIR: &lir})

	id := c.LocalIdentity()

	i := slices.IndexFunc(h.order, func(impi string) bool { return h.subscribers[impi].has(lir.PublicIdentity) })
	if i < 0 {
		return experimental(req, id, tgpp.ResultErrorUserUnknown)
	}

	s := h.subscribers[h.order[i]]

	switch {
	case lir.AuthorizationType == cx.AuthorizationRegistrationAndCapabilities:
		s.ReassignPending = s.ServerName != ""
		return must(cx.NewLocationInfoAnswer(req, id, cx.LocationInfo{}))
	case s.State != NotRegistered:
		return must(cx.NewLocationInfoAnswer(req, id, cx.LocationInfo{ServerName: s.ServerName}))
	case !s.UnregisteredServices && !lir.Originating:
		return experimental(req, id, tgpp.ResultErrorIdentityNotRegistered)
	case s.ServerName != "":
		return must(cx.NewLocationInfoAnswer(req, id, cx.LocationInfo{ServerName: s.ServerName}))
	}

	return must(cx.NewLocationInfoAnswer(req, id, cx.LocationInfo{Result: tgpp.Experimental(tgpp.ResultUnregisteredService)}))
}

var ErrNotConnected = errors.New("hsstest: the IMS is not connected")

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

	h.Update(impi, func(s *Subscriber) {
		s.ServerName, s.State, s.AuthPending, s.ReassignPending = "", NotRegistered, false, false
	})

	ans, err := h.node.Do(ctx, imsPeer, req)
	if err != nil {
		return cx.RegistrationTermination{}, err
	}

	return cx.ParseRegistrationTerminationAnswer(ans)
}
