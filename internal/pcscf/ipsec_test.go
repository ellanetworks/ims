package pcscf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

const (
	testIMPI  = "001010000000001@" + homeDomain
	testCK    = "000102030405060708090a0b0c0d0e0f"
	testIK    = "101112131415161718191a1b1c1d1e1f"
	testGrace = 150 * time.Millisecond
	quiet     = 100 * time.Millisecond
)

func (l *lateHandler) filter(m sip.Message) error {
	if p := l.h.Load(); p != nil {
		return p.Filter(m)
	}

	return nil
}

func (l *lateHandler) responseFlow(req *sip.Request, res *sip.Response) (sip.Flow, bool, error) {
	if p := l.h.Load(); p != nil {
		return p.ResponseFlow(req, res)
	}

	return sip.Flow{}, false, nil
}

type ipsecScene struct {
	t      *testing.T
	icscf  *siptest.Socket
	scscf  *siptest.Socket
	pcscf  netip.AddrPort
	ps     netip.AddrPort
	pcs    [2]netip.AddrPort
	ue     *siptest.Socket
	kernel *ipsectest.Kernel
	store  *db.DB
	p      *PCSCF
	late   *lateHandler
	layer  *transaction.Layer
}

func openStore(t *testing.T) *db.DB {
	t.Helper()

	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return d
}

func newIPsecScene(t *testing.T, policy ipsec.Policy, opts ...func(*Config)) *ipsecScene {
	t.Helper()

	return newIPsecSceneWith(t, policy, ipsectest.NewKernel(), openStore(t), opts...)
}

func newIPsecSceneWith(t *testing.T, policy ipsec.Policy, kernel *ipsectest.Kernel, store *db.DB, opts ...func(*Config)) *ipsecScene {
	t.Helper()

	s := newIPsecSceneAt(t, loopback, policy, kernel, store, transport.Config{}, opts...)
	s.kernel = kernel
	s.ue = siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	return s
}

func newIPsecSceneAt(t *testing.T, addr netip.Addr, policy ipsec.Policy, kernel Kernel, store *db.DB, tc transport.Config,
	opts ...func(*Config),
) *ipsecScene {
	t.Helper()

	s := &ipsecScene{t: t, store: store, late: &lateHandler{}}
	s.icscf = siptest.NewSocket(t, netip.AddrPortFrom(addr, 0))
	s.scscf = siptest.NewSocket(t, netip.AddrPortFrom(addr, 0))

	layer, fallback := siptest.NewLayer(t, transaction.Config{
		Handler: s.late, Logger: slog.New(slog.DiscardHandler), Filter: s.late.filter, ResponseFlow: s.late.responseFlow,
		Transport: tc,
	})
	s.layer = layer
	s.pcscf = siptest.ListenLayer(t, layer, addr)
	s.ps = siptest.ListenLayer(t, layer, addr)
	s.pcs = [2]netip.AddrPort{siptest.ListenLayer(t, layer, addr), siptest.ListenLayer(t, layer, addr)}

	cfg := Config{
		Layer:      layer,
		Proxy:      s.newProxy(),
		Port:       s.pcscf.Port(),
		ICSCFPort:  s.icscf.Addr().Port(),
		HomeDomain: homeDomain,
		SCSCF: SCSCF{
			Name:      sip.URI{Scheme: "sip", Host: "scscf." + homeDomain},
			Listeners: []netip.AddrPort{s.scscf.Addr()},
		},
		Registrations: store,
		IPsec: IPsec{
			Kernel:      kernel,
			Store:       store,
			Policy:      policy,
			ServerPort:  s.ps.Port(),
			ClientPorts: [2]uint16{s.pcs[0].Port(), s.pcs[1].Port()},
			AwaitAuth:   2 * time.Second,
			Grace:       testGrace,
		},
		Trust:    trust.New([]netip.Addr{s.scscf.Addr().Addr()}, nil),
		Fallback: fallback,
		Logger:   slog.New(slog.DiscardHandler),
	}

	for _, o := range opts {
		o(&cfg)
	}

	s.p = New(cfg)
	t.Cleanup(s.p.Close)

	if err := s.p.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}

	s.late.h.Store(s.p)

	return s
}

func (s *ipsecScene) newProxy() *proxy.Proxy {
	return proxy.New(proxy.Config{
		Layer: s.layer, Port: s.pcscf.Port(), Supported: []string{secAgree},
		LocalPorts: []uint16{s.ps.Port(), s.pcs[0].Port(), s.pcs[1].Port()},
	})
}

type ue struct {
	uc, us     *siptest.Socket
	spiC, spiS uint32
	callID     string
	cseq       int
}

func (s *ipsecScene) newUE(spi uint32) *ue {
	return newUEAt(s.t, loopback, spi)
}

func newUEAt(t *testing.T, addr netip.Addr, spi uint32) *ue {
	return &ue{
		uc:     siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
		us:     siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
		spiC:   spi,
		spiS:   spi + 1,
		callID: sip.NewTag(),
	}
}

func (u *ue) rekeyed(t *testing.T) *ue {
	n := *u
	n.uc = siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	n.spiC, n.spiS = u.spiC+100, u.spiS+100

	return &n
}

func (u *ue) securityClient() string {
	return fmt.Sprintf("ipsec-3gpp;prot=esp;mod=trans;spi-c=%d;spi-s=%d;port-c=%d;port-s=%d;alg=hmac-sha-1-96;ealg=null, "+
		"ipsec-3gpp;prot=esp;mod=trans;spi-c=%d;spi-s=%d;port-c=%d;port-s=%d;alg=hmac-md5-96;ealg=null",
		u.spiC, u.spiS, u.uc.Addr().Port(), u.us.Addr().Port(), u.spiC, u.spiS, u.uc.Addr().Port(), u.us.Addr().Port())
}

func (u *ue) register(t *testing.T, sentBy netip.AddrPort, response string, edit func(*sip.Request)) *sip.Request {
	t.Helper()

	u.cseq++

	r := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, sentBy)
	r.Header.Set("To", "<sip:"+testIMPI+">")
	r.Header.Set("From", "<sip:"+testIMPI+">;tag="+sip.NewTag())
	r.Header.Set("Call-ID", u.callID)
	r.Header.Set("CSeq", strconv.Itoa(u.cseq)+" REGISTER")
	r.Header.Set("Contact", "<sip:ue@127.0.0.1:"+strconv.Itoa(int(u.us.Addr().Port()))+">")
	r.Header.Set("Expires", "600")
	r.Header.Add("Authorization", `Digest username="`+testIMPI+`", realm="`+homeDomain+`", uri="sip:`+homeDomain+
		`", nonce="bm9uY2U=", response="`+response+`"`)
	r.Header.Add("Security-Client", u.securityClient())
	r.Header.Add("Require", "sec-agree")
	r.Header.Add("Proxy-Require", "sec-agree")

	if edit != nil {
		edit(r)
	}

	return r
}

func (s *ipsecScene) forwarded() (*sip.Request, sip.Flow, string) {
	s.t.Helper()

	req, f := s.icscf.RecvRequest()

	for _, name := range []string{"Security-Client", "Security-Verify"} {
		if req.Header.Has(name) {
			s.t.Errorf("%s forwarded to the I-CSCF", name)
		}
	}

	if v := req.Header.Get("Require"); v != "path" {
		s.t.Errorf("Require: %s forwarded to the I-CSCF, want path only", v)
	}

	if req.Header.Has("Proxy-Require") {
		s.t.Errorf("Proxy-Require: %s forwarded to the I-CSCF", req.Header.Get("Proxy-Require"))
	}

	a, err := sip.ParseAuth(req.Header.Get("Authorization"))
	if err != nil {
		s.t.Fatal(err)
	}

	v, _ := a.Params.Get("integrity-protected")

	return req, f, sip.Unquote(v)
}

func (s *ipsecScene) answer(req *sip.Request, f sip.Flow, code int) {
	s.t.Helper()

	res := sip.NewResponse(req, code, "")
	_ = res.Header.SetToTag(sip.NewTag())

	switch code {
	case 401:
		res.Header.Add("WWW-Authenticate", `Digest realm="`+homeDomain+`", nonce="bm9uY2U=", algorithm=AKAv1-MD5, `+
			`qop="auth", ck="`+testCK+`", ik="`+testIK+`"`)
	case 200:
		for _, c := range req.Header.Values("Contact") {
			expires := "600"
			if req.Header.Get("Expires") == "0" {
				expires = "0"
			}

			res.Header.Add("Contact", c+";expires="+expires)
		}
	}

	s.icscf.Send(f.Transport, f.Remote, res)
}

func (s *ipsecScene) securityServer(res *sip.Response) sip.SecurityMechanism {
	s.t.Helper()

	ms, err := res.Header.SecurityMechanisms("Security-Server")
	if err != nil || len(ms) != 1 {
		s.t.Fatalf("Security-Server = %q, %v; want one mechanism", res.Header.Get("Security-Server"), err)
	}

	return ms[0]
}

func (s *ipsecScene) installed() []ipsec.Set {
	var out []ipsec.Set

	for set := range s.kernel.Installed() {
		out = append(out, set)
	}

	return out
}

func (s *ipsecScene) stored() []db.SecurityAssociation {
	s.t.Helper()

	s.p.sas.sync()

	sas, err := s.store.ListSecurityAssociations(context.Background())
	if err != nil {
		s.t.Fatal(err)
	}

	return sas
}

func wantStatus(t *testing.T, res *sip.Response, code int) {
	t.Helper()

	if res.StatusCode != code {
		t.Fatalf("got %q, want %d", res.StartLine(), code)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (s *ipsecScene) challenge(u *ue) sip.SecurityMechanism {
	s.t.Helper()

	s.ue.Send(sip.UDP, s.pcscf, u.register(s.t, s.ue.Addr(), "", nil))

	req, f, integrity := s.forwarded()
	if integrity != "no" {
		s.t.Fatalf("integrity-protected = %q on the unprotected REGISTER, want no", integrity)
	}

	s.answer(req, f, 401)

	res, _ := s.ue.RecvResponse()
	wantStatus(s.t, res, 401)

	if a, _ := sip.ParseAuth(res.Header.Get("WWW-Authenticate")); a.Params.Has("ck") || a.Params.Has("ik") {
		s.t.Fatal("ck or ik relayed to the UE")
	}

	return s.securityServer(res)
}

func (s *ipsecScene) authenticate(u *ue, server sip.SecurityMechanism) *sip.Response {
	s.t.Helper()

	r := u.register(s.t, u.us.Addr(), "c4c4", func(r *sip.Request) { r.Header.Add("Security-Verify", server.String()) })
	u.uc.Send(sip.UDP, s.ps, r)

	req, f, integrity := s.forwarded()
	if integrity != "yes" {
		s.t.Fatalf("integrity-protected = %q on the protected REGISTER, want yes", integrity)
	}

	s.answer(req, f, 200)

	res, from := u.us.RecvResponse()
	wantStatus(s.t, res, 200)

	if pc := portParam(s.t, server, "port-c"); from.Remote.Port() != pc {
		s.t.Fatalf("200 from port %d, want the protected client port %d", from.Remote.Port(), pc)
	}

	return res
}

func portParam(t *testing.T, m sip.SecurityMechanism, name string) uint16 {
	t.Helper()

	v, _ := m.Params.Get(name)

	n, err := strconv.ParseUint(v, 10, 16)
	if err != nil {
		t.Fatalf("%s %q: %v", name, v, err)
	}

	return uint16(n)
}

func TestInitialRegistrationOverIPsec(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)

	server := s.challenge(u)

	sets := s.installed()
	if len(sets) != 1 {
		t.Fatalf("installed = %v, want one set", sets)
	}

	set := sets[0]
	keys := s.kernel.Installed()[set]

	want := ipsec.Set{
		Local:      ipsec.Endpoint{Addr: loopback, PortC: s.pcs[0].Port(), PortS: s.ps.Port(), SPIC: set.Local.SPIC, SPIS: set.Local.SPIS},
		Remote:     ipsec.Endpoint{Addr: loopback, PortC: u.uc.Addr().Port(), PortS: u.us.Addr().Port(), SPIC: u.spiC, SPIS: u.spiS},
		Integrity:  ipsec.HMACSHA196,
		Encryption: ipsec.EncryptionNull,
	}
	if set != want || set.Local.SPIC < ipsec.MinSPI || set.Local.SPIS < ipsec.MinSPI {
		t.Fatalf("set = %s, want %s", set, want)
	}

	if fmt.Sprintf("%x %x", keys.CK, keys.IK) != testCK+" "+testIK {
		t.Fatalf("keys = %x %x", keys.CK, keys.IK)
	}

	if server.String() != set.Server().String() {
		t.Fatalf("Security-Server = %s, want %s", server, set.Server())
	}

	if len(s.stored()) != 0 {
		t.Fatal("temporary set stored")
	}

	s.authenticate(u, server)

	stored := s.stored()
	if len(stored) != 1 || stored[0].State != db.SecurityAssociationEstablished || stored[0].SPIPS != set.Local.SPIS ||
		time.Until(stored[0].ExpiresAt) < 600*time.Second {
		t.Fatalf("stored = %+v, want the established set for the registration plus 30 s", stored)
	}
}

func TestSecurityAgreementChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*sip.Request, sip.SecurityMechanism)
		code int
	}{
		{"Security-Verify altered", func(r *sip.Request, m sip.SecurityMechanism) {
			m.Params.Set("alg", "hmac-md5-96")
			r.Header.Add("Security-Verify", m.String())
		}, 403},
		{"Security-Client altered", func(r *sip.Request, m sip.SecurityMechanism) {
			r.Header.Set("Security-Client", "ipsec-3gpp;spi-c=1;spi-s=2;port-c=3;port-s=4;alg=hmac-sha-1-96")
			r.Header.Add("Security-Verify", m.String())
		}, 403},
		{"Security-Verify missing", func(*sip.Request, sip.SecurityMechanism) {}, 400},
		{"Via on another address", func(r *sip.Request, m sip.SecurityMechanism) {
			via, _ := r.Header.TopVia()
			via.Host = "192.0.2.9"
			_ = r.Header.SetTopVia(via)
			r.Header.Add("Security-Verify", m.String())
		}, 403},
		{"Via with a host name", func(r *sip.Request, m sip.SecurityMechanism) {
			via, _ := r.Header.TopVia()
			via.Host = "ue.example.org"
			_ = r.Header.SetTopVia(via)
			r.Header.Add("Security-Verify", m.String())
		}, 403},
		{"two Vias", func(r *sip.Request, m sip.SecurityMechanism) {
			r.Header.Add("Via", "SIP/2.0/UDP 192.0.2.9;branch=z9hG4bKother")
			r.Header.Add("Security-Verify", m.String())
		}, 403},
		{"another private identity", func(r *sip.Request, m sip.SecurityMechanism) {
			r.Header.Set("Authorization", `Digest username="mallory@`+homeDomain+`", realm="`+homeDomain+
				`", uri="sip:`+homeDomain+`", nonce="bm9uY2U=", response="c4c4"`)
			r.Header.Add("Security-Verify", m.String())
		}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newIPsecScene(t, ipsec.DefaultPolicy())
			u := s.newUE(25656)
			server := s.challenge(u)

			u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) { tc.edit(r, server) }))

			res, _ := u.us.RecvResponse()
			wantStatus(t, res, tc.code)
			s.icscf.RecvNone(quiet)

			eventually(t, "the temporary set to be removed", func() bool { return len(s.installed()) == 0 })
		})
	}
}

func TestVerifyIgnoresPreference(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	server := s.challenge(u)

	server.Params.Set("q", "0.100")
	server.Params.Del("prot")

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Add("Security-Verify", server.String())
	}))

	if _, _, integrity := s.forwarded(); integrity != "yes" {
		t.Fatalf("integrity-protected = %q, want yes", integrity)
	}
}

func TestProtectedPortsDropUnknownFlows(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "", nil))
	u.us.RecvNone(quiet)
	u.uc.RecvNone(0)
	s.icscf.RecvNone(0)

	server := s.challenge(u)

	stranger := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	stranger.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Add("Security-Verify", server.String())
	}))
	stranger.RecvNone(quiet)
	s.icscf.RecvNone(0)
}

func TestUnprotectedRequestsFromAProtectedUE(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	s.ue.Send(sip.UDP, s.pcscf, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, s.ue.Addr()))
	wantStatus(t, first(s.ue.RecvResponse()), 403)
	s.p.cfg.Fallback.(*siptest.TU).None(quiet)

	other := siptest.NewSocket(t, netip.MustParseAddrPort("127.0.0.2:0"))
	other.Send(sip.UDP, s.pcscf, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, other.Addr()))

	if req := fallbackRequest(t, s); req.Method != "OPTIONS" {
		t.Fatalf("fallback got %s", req.Method)
	}
}

func fallbackRequest(t *testing.T, s *ipsecScene) *sip.Request {
	t.Helper()

	return s.p.cfg.Fallback.(*siptest.TU).NextRequest().Req
}

func TestReRegistrationWithoutAuthentication(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	server := s.challenge(u)
	s.authenticate(u, server)

	before := s.stored()[0].ExpiresAt
	next := u.rekeyed(t)

	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Add("Security-Verify", server.String())
	}))

	req, f, integrity := s.forwarded()
	if integrity != "yes" {
		t.Fatalf("integrity-protected = %q on the established set, want yes", integrity)
	}

	time.Sleep(10 * time.Millisecond)
	s.answer(req, f, 200)

	res, _ := u.us.RecvResponse()
	wantStatus(t, res, 200)

	if after := s.stored()[0].ExpiresAt; !after.After(before) {
		t.Fatalf("expiry %v, want later than %v", after, before)
	}

	if len(s.installed()) != 1 {
		t.Fatalf("installed = %v, want the one set", s.installed())
	}
}

func TestReRegistrationWithoutSecurityClient(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	without := func(r *sip.Request) { r.Header.Del("Security-Client") }

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", without))

	req, f, integrity := s.forwarded()
	if integrity != "yes" {
		t.Fatalf("integrity-protected = %q, want yes", integrity)
	}

	s.answer(req, f, 200)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", without))

	req, f, _ = s.forwarded()
	s.answer(req, f, 401)
	wantStatus(t, first(u.us.RecvResponse()), 403)

	if len(s.installed()) != 1 {
		t.Fatalf("installed = %v, want the established set only", s.installed())
	}
}

func first[T, U any](t T, _ U) T {
	return t
}

func TestReAuthentication(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	first := s.installed()[0]
	next := u.rekeyed(t)

	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", nil))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)

	res, from := u.us.RecvResponse()
	wantStatus(t, res, 401)

	if from.Remote != s.pcs[0] {
		t.Fatalf("401 from %s, want the old set's client port %s", from.Remote, s.pcs[0])
	}

	server := s.securityServer(res)
	if portParam(t, server, "port-c") != s.pcs[1].Port() || portParam(t, server, "port-s") != s.ps.Port() {
		t.Fatalf("Security-Server = %s, want the other client port and the same server port", server)
	}

	if len(s.installed()) != 2 {
		t.Fatalf("installed = %v, want the old and the temporary set", s.installed())
	}

	next.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Add("Security-Verify", server.String())
	}))

	req, f, integrity := s.forwarded()
	if integrity != "yes" {
		t.Fatalf("integrity-protected = %q, want yes", integrity)
	}

	s.answer(req, f, 200)

	if res, from := u.us.RecvResponse(); res.StatusCode != 200 || from.Remote != s.pcs[1] {
		t.Fatalf("got %q from %s, want 200 from the new set's client port %s", res.StartLine(), from.Remote, s.pcs[1])
	}

	states := map[db.SecurityAssociationState]int{}
	for _, sa := range s.stored() {
		states[sa.State]++
	}

	if states[db.SecurityAssociationEstablished] != 1 || states[db.SecurityAssociationOld] != 1 {
		t.Fatalf("stored = %+v, want one established and one old set", s.stored())
	}

	time.Sleep(2 * testGrace)

	if len(s.installed()) != 2 {
		t.Fatal("the old set was removed before the UE used the new one")
	}

	next.uc.Send(sip.UDP, s.ps, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, u.us.Addr()))
	fallbackRequest(t, s)

	eventually(t, "the old set to be removed", func() bool {
		sets := s.installed()
		return len(sets) == 1 && sets[0] != first
	})

	if stored := s.stored(); len(stored) != 1 || stored[0].State != db.SecurityAssociationEstablished {
		t.Fatalf("stored = %+v, want the new set only", stored)
	}
}

func TestUnprotectedRegistrationReplacesTheUEsSets(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	first := s.installed()[0]

	server := s.challenge(u)

	if sets := s.installed(); len(sets) != 1 || sets[0] == first {
		t.Fatalf("installed = %v, want only the new temporary set: the UE reused its ports and SPIs", sets)
	}

	s.authenticate(u, server)

	if len(s.stored()) != 1 {
		t.Fatalf("stored = %+v, want one set", s.stored())
	}
}

func TestDeregistrationRemovesTheSets(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	server := s.challenge(u)
	s.authenticate(u, server)

	next := u.rekeyed(t)
	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) { r.Header.Set("Expires", "0") }))

	req, f, _ := s.forwarded()
	s.answer(req, f, 200)

	res, _ := u.us.RecvResponse()
	wantStatus(t, res, 200)

	if len(s.installed()) != 1 {
		t.Fatal("the set was removed before the deregistration's transaction ended")
	}

	eventually(t, "the set to be removed", func() bool { return len(s.installed()) == 0 && len(s.stored()) == 0 })
}

func TestRegisterWithoutIPsecIsRejected(t *testing.T) {
	policy := ipsec.DefaultPolicy()

	var want []string
	for _, m := range policy.Mechanisms() {
		want = append(want, m.String())
	}

	tests := []struct {
		name   string
		client string
		tags   map[string]string
		code   int
	}{
		{"no sec-agree", "", nil, 421},
		{"sec-agree required, no Security-Client", "", map[string]string{"Require": "sec-agree", "Proxy-Require": "sec-agree"}, 494},
		{"sec-agree required, no ipsec-3gpp", "digest;q=0.1", map[string]string{"Require": "sec-agree", "Proxy-Require": "sec-agree"}, 494},
		{"sec-agree supported", "", map[string]string{"Supported": "path, sec-agree"}, 494},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newIPsecScene(t, policy)
			u := s.newUE(25656)

			s.ue.Send(sip.UDP, s.pcscf, u.register(t, s.ue.Addr(), "", func(r *sip.Request) {
				for _, name := range []string{"Security-Client", "Require", "Proxy-Require", "Supported"} {
					r.Header.Del(name)
				}

				if tt.client != "" {
					r.Header.Add("Security-Client", tt.client)
				}

				for name, v := range tt.tags {
					r.Header.Add(name, v)
				}
			}))

			res, _ := s.ue.RecvResponse()
			wantStatus(t, res, tt.code)

			if got := res.Header.Elements("Security-Server"); !slices.Equal(got, want) {
				t.Errorf("Security-Server = %q, want the policy's list %q", got, want)
			}

			if res.Header.Get("Require") != "sec-agree" || len(s.installed()) != 0 {
				t.Errorf("Require = %q and %d sets, want sec-agree required and none", res.Header.Get("Require"), len(s.installed()))
			}

			s.icscf.RecvNone(quiet)
		})
	}
}

func TestNoAcceptableAlgorithm(t *testing.T) {
	s := newIPsecScene(t, ipsec.Policy{Integrity: []ipsec.Integrity{ipsec.HMACSHA196}, Encryption: ipsec.EncryptionRequired})
	u := s.newUE(25656)

	s.ue.Send(sip.UDP, s.pcscf, u.register(t, s.ue.Addr(), "", nil))

	res, _ := s.ue.RecvResponse()
	wantStatus(t, res, 403)
	s.icscf.RecvNone(quiet)
}

func TestEncryptionPreferred(t *testing.T) {
	s := newIPsecScene(t, ipsec.Policy{Integrity: []ipsec.Integrity{ipsec.HMACSHA196}, Encryption: ipsec.EncryptionPreferred})
	u := s.newUE(25656)

	s.ue.Send(sip.UDP, s.pcscf, u.register(t, s.ue.Addr(), "", func(r *sip.Request) {
		r.Header.Add("Security-Client", fmt.Sprintf("ipsec-3gpp;spi-c=%d;spi-s=%d;port-c=%d;port-s=%d;alg=hmac-sha-1-96;ealg=aes-cbc",
			u.spiC, u.spiS, u.uc.Addr().Port(), u.us.Addr().Port()))
	}))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)

	res, _ := s.ue.RecvResponse()
	if v, _ := s.securityServer(res).Params.Get("ealg"); v != "aes-cbc" {
		t.Fatalf("ealg = %q, want aes-cbc", v)
	}

	for set, keys := range s.kernel.Installed() {
		if set.Encryption != ipsec.AESCBC || !bytes.Equal(keys.CK, mustHex(t, testCK)) {
			t.Fatalf("installed %s with CK %x", set, keys.CK)
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	var b []byte

	if _, err := fmt.Sscanf(s, "%x", &b); err != nil {
		t.Fatal(err)
	}

	return b
}

func TestInstallFailure(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)

	s.kernel.FailNext(errors.New("no IPsec"))
	s.ue.Send(sip.UDP, s.pcscf, u.register(t, s.ue.Addr(), "", nil))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)

	res, _ := s.ue.RecvResponse()
	wantStatus(t, res, 500)
}

func TestFailedAuthenticationKeepsTheRegistration(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	first := s.installed()[0]
	next := u.rekeyed(t)

	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", nil))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)

	res, _ := u.us.RecvResponse()
	server := s.securityServer(res)

	next.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "bad", func(r *sip.Request) {
		r.Header.Add("Security-Verify", server.String())
	}))

	req, f, _ = s.forwarded()
	s.answer(req, f, 403)

	if res, from := u.us.RecvResponse(); res.StatusCode != 403 || from.Remote != s.pcs[0] {
		t.Fatalf("got %q from %s, want 403 over the set being re-authenticated, from %s", res.StartLine(), from.Remote, s.pcs[0])
	}

	eventually(t, "the temporary set to be removed", func() bool {
		sets := s.installed()
		return len(sets) == 1 && sets[0] == first
	})
}

func TestTCPOverTheProtectedPorts(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	server := s.challenge(u)

	r := u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Add("Security-Verify", server.String())

		via, _ := r.Header.TopVia()
		via.Transport = sip.TCP
		_ = r.Header.SetTopVia(via)
	})
	u.uc.Send(sip.TCP, s.ps, r)

	req, f, integrity := s.forwarded()
	if integrity != "yes" {
		t.Fatalf("integrity-protected = %q, want yes", integrity)
	}

	s.answer(req, f, 200)

	res, from := u.uc.RecvResponse()
	if res.StatusCode != 200 || from.Transport != sip.TCP || from.Remote != s.ps {
		t.Fatalf("got %q over %s, want 200 on the UE's connection to %s", res.StartLine(), from, s.ps)
	}
}

func TestRestore(t *testing.T) {
	kernel := ipsectest.NewKernel()
	store := openStore(t)

	s := newIPsecSceneWith(t, ipsec.DefaultPolicy(), kernel, store)
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	v := s.newUE(40000)
	s.challenge(v)

	if len(kernel.Installed()) != 2 {
		t.Fatalf("installed = %v", kernel.Installed())
	}

	restart(t, s)

	if sets := kernel.Installed(); len(sets) != 1 {
		t.Fatalf("installed after restart = %v, want the established set only", sets)
	}

	next := u.rekeyed(t)
	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", nil))

	if _, _, integrity := s.forwarded(); integrity != "yes" {
		t.Fatalf("integrity-protected = %q after restart, want yes", integrity)
	}

	for set := range kernel.Installed() {
		kernel.Lose(set)
	}

	restart(t, s)

	if stored, _ := store.ListSecurityAssociations(context.Background()); len(stored) != 0 {
		t.Fatalf("stored = %+v, want sets the kernel lost dropped", stored)
	}
}

func restart(t *testing.T, s *ipsecScene) {
	t.Helper()

	s.p.Close()

	cfg := s.p.cfg
	cfg.Proxy = s.newProxy()

	p := New(cfg)
	t.Cleanup(p.Close)

	if err := p.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}

	s.p = p
	s.late.h.Store(p)
}

func TestSamsungRegistration(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)

	capture := func(name string, verify string) *sip.Request {
		t.Helper()

		raw, err := os.ReadFile(filepath.Join("..", "..", "sip", "internal", "corpus", "testdata", "open5gs", "ipsec_reg", name))
		if err != nil {
			t.Fatal(err)
		}

		text := strings.NewReplacer(
			"192.168.101.5", "127.0.0.1",
			"6301", strconv.Itoa(int(u.uc.Addr().Port())),
			"6300", strconv.Itoa(int(u.us.Addr().Port())),
		).Replace(strings.ReplaceAll(string(raw), "\r\n", "\n"))

		lines := strings.Split(text, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "Security-Verify:") {
				lines[i] = "Security-Verify: " + verify
			}
		}

		m, err := sip.Parse([]byte(strings.Join(lines, "\r\n")))
		if err != nil {
			t.Fatal(err)
		}

		return m.(*sip.Request)
	}

	s.ue.Send(sip.UDP, s.pcscf, capture("001-REGISTER.sip", ""))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)

	res, _ := s.ue.RecvResponse()
	server := s.securityServer(res)

	if v, _ := server.Params.Get("alg"); v != "hmac-md5-96" {
		t.Fatalf("Security-Server = %s, want the Samsung's only algorithm, hmac-md5-96", server)
	}

	u.uc.Send(sip.TCP, s.ps, capture("009-REGISTER.sip", server.String()))

	req, f, integrity := s.forwarded()
	if integrity != "yes" {
		t.Fatalf("integrity-protected = %q, want yes", integrity)
	}

	s.answer(req, f, 200)

	if res, from := u.uc.RecvResponse(); res.StatusCode != 200 || from.Transport != sip.TCP {
		t.Fatalf("got %q over %s, want 200 on the Samsung's TCP connection", res.StartLine(), from)
	}
}

func TestReAuthenticationMismatchAnswersOnTheOldSet(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	next := u.rekeyed(t)
	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", nil))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)
	s.securityServer(first(u.us.RecvResponse()))

	next.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", nil))

	if res, from := u.us.RecvResponse(); res.StatusCode != 400 || from.Remote != s.pcs[0] {
		t.Fatalf("got %q from %s, want 400 over the set being re-authenticated, from %s", res.StartLine(), from.Remote, s.pcs[0])
	}
}

func TestReAuthenticationKeepsTheLongerLifetime(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	before := s.stored()[0].ExpiresAt
	next := u.rekeyed(t)

	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", nil))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)
	server := s.securityServer(first(u.us.RecvResponse()))

	next.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Set("Expires", "60")
		r.Header.Add("Security-Verify", server.String())
	}))

	req, f, _ = s.forwarded()
	s.answer(req, f, 200)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	for _, sa := range s.stored() {
		if sa.State == db.SecurityAssociationEstablished && sa.ExpiresAt.Before(before) {
			t.Fatalf("new set expires %v, before the old set's %v", sa.ExpiresAt, before)
		}
	}
}

func TestMovingUE(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	for i, addr := range []string{"127.0.0.2", "127.0.0.3", "127.0.0.4"} {
		moved := newUEAt(t, netip.MustParseAddr(addr), uint32(30000+10*i))
		moved.callID = u.callID
		moved.cseq = u.cseq + 10*(i+1)
		unprotected := siptest.NewSocket(t, netip.AddrPortFrom(netip.MustParseAddr(addr), 0))

		unprotected.Send(sip.UDP, s.pcscf, moved.register(t, unprotected.Addr(), "", nil))

		req, f, _ := s.forwarded()
		s.answer(req, f, 401)

		res, _ := unprotected.RecvResponse()
		server := s.securityServer(res)

		moved.uc.Send(sip.UDP, s.ps, moved.register(t, moved.us.Addr(), "c4c4", func(r *sip.Request) {
			r.Header.Add("Security-Verify", server.String())
		}))

		req, f, _ = s.forwarded()
		s.answer(req, f, 200)
		wantStatus(t, first(moved.us.RecvResponse()), 200)
	}

	eventually(t, "the sets on old addresses to go", func() bool {
		sets := s.installed()
		return len(sets) == 1 && sets[0].Remote.Addr == netip.MustParseAddr("127.0.0.4")
	})
}

func TestRegistrationOutcome(t *testing.T) {
	req := func(contact, expires string) *sip.Request {
		r := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, netip.MustParseAddrPort("127.0.0.1:5060"))
		r.Header.Set("Contact", contact)

		if expires != "" {
			r.Header.Set("Expires", expires)
		}

		return r
	}

	res := func(r *sip.Request, contacts ...string) *sip.Response {
		out := sip.NewResponse(r, 200, "")
		for _, c := range contacts {
			out.Header.Add("Contact", c)
		}

		return out
	}

	ue := "<sip:ue@127.0.0.1:6300>"

	for _, tc := range []struct {
		name string
		req  *sip.Request
		res  []string
		want outcome
	}{
		{"granted", req(ue, "600"), []string{ue + ";expires=300", "<sip:other@192.0.2.1>;expires=900"}, outcome{lifetime: 300 * time.Second}},
		{"removed", req(ue, "0"), []string{ue + ";expires=0"}, outcome{dereg: true}},
		{"star", req("*", "0"), nil, outcome{dereg: true}},
		{"no match, requested", req(ue, "600"), []string{"<sip:other@192.0.2.1>;expires=900"}, outcome{lifetime: 600 * time.Second}},
		{"no match, removal requested", req(ue+";expires=0", "600"), nil, outcome{dereg: true}},
		{"no match, no expiry", req(ue, ""), nil, outcome{}},
	} {
		if got := registrationOutcome(tc.req, res(tc.req, tc.res...)); got != tc.want {
			t.Errorf("%s: outcome = %+v, want %+v", tc.name, got, tc.want)
		}
	}

	fetch := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, netip.MustParseAddrPort("127.0.0.1:5060"))
	fetch.Header.Del("Contact")

	if got := registrationOutcome(fetch, res(fetch, ue+";expires=300")); got != (outcome{}) {
		t.Errorf("fetch: outcome = %+v, want none", got)
	}
}

func TestPromotionWithoutMatchingContact(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	server := s.challenge(u)

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) { r.Header.Add("Security-Verify", server.String()) }))

	req, f, _ := s.forwarded()

	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Add("Contact", "<sip:ue@192.0.2.1:6300>;expires=900")
	s.icscf.Send(f.Transport, f.Remote, res)

	wantStatus(t, first(u.us.RecvResponse()), 200)

	if stored := s.stored(); len(stored) != 1 || time.Until(stored[0].ExpiresAt) < 600*time.Second {
		t.Fatalf("stored = %+v, want the set established for the requested 600 s", stored)
	}
}

func TestResponsesNeverLeaveAProtectedPortInClear(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	server := s.challenge(u)

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) { r.Header.Add("Security-Verify", server.String()) }))

	req, f, _ := s.forwarded()

	for set := range s.kernel.Installed() {
		s.p.sas.mu.Lock()
		for x := range s.p.sas.sets {
			if x.set == set {
				s.p.sas.remove(x)
			}
		}
		s.p.sas.mu.Unlock()
	}

	s.answer(req, f, 200)
	u.us.RecvNone(quiet)
	u.uc.RecvNone(quiet)
}

func TestRestoreBeforeTheNewSetIsUsed(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.authenticate(u, s.challenge(u))

	next := u.rekeyed(t)
	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", nil))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)
	server := s.securityServer(first(u.us.RecvResponse()))

	next.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) { r.Header.Add("Security-Verify", server.String()) }))

	req, f, _ = s.forwarded()
	s.answer(req, f, 200)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	restart(t, s)

	next.uc.Send(sip.UDP, s.ps, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, u.us.Addr()))
	fallbackRequest(t, s)

	eventually(t, "the old set to be removed", func() bool { return len(s.installed()) == 1 })
}

func TestSecurityClientOrderAndPreference(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	server := s.challenge(u)

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		ms, _ := r.Header.SecurityMechanisms("Security-Client")
		ms[0].Params.Set("q", "0.5")

		r.Header.Del("Security-Client")
		r.Header.Add("Security-Client", "sdes-srtp;mediasec")
		r.Header.Add("Security-Client", ms[1].String())
		r.Header.Add("Security-Client", ms[0].String())
		r.Header.Add("Security-Verify", server.String())
	}))

	if _, _, integrity := s.forwarded(); integrity != "yes" {
		t.Fatalf("integrity-protected = %q, want yes", integrity)
	}
}

func TestTemporarySetCarriesOnlyREGISTER(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	u := s.newUE(25656)
	s.challenge(u)

	u.uc.Send(sip.UDP, s.ps, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, u.us.Addr()))
	u.us.RecvNone(quiet)
	s.p.cfg.Fallback.(*siptest.TU).None(0)
}

func TestTCPWithoutSecurityAssociationsIsClosed(t *testing.T) {
	s := newIPsecScene(t, ipsec.DefaultPolicy())
	stranger := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	stranger.Send(sip.TCP, s.ps, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.TCP, stranger.Addr()))

	conn := stranger.Conn(s.ps)
	_ = conn.SetReadDeadline(time.Now().Add(siptest.Timeout))

	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("connection without security associations still open: %v", err)
	}
}
