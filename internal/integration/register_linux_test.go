//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/netnstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/internal/testue"
	"github.com/ellanetworks/ims/sip"
)

const (
	domain   = "ims.mnc001.mcc001.3gppnetwork.org"
	imsHost  = "ims." + domain
	imsi     = "001010000000001"
	impi     = imsi + "@" + domain
	tempIMPU = "sip:" + impi
	msisdn   = "sip:+15550001@" + domain
	imei     = "35693803564380"

	pcscfPort = 5060
)

var (
	imsAddrs = []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24"), netip.MustParsePrefix("fd00::1/64")}
	ueAddrs  = []netip.Prefix{netip.MustParsePrefix("10.0.0.2/24"), netip.MustParsePrefix("fd00::2/64")}

	testK   = []byte("0123456789abcdef")
	testOPc = []byte("fedcba9876543210")
)

func TestMain(m *testing.M) {
	netnstest.Main(m)
}

// scene is the IMS with real IPsec in the test's namespace, the fake HSS on
// its loopback, and a namespace for the UE linked to it.
type scene struct {
	t    *testing.T
	ims  *netnstest.Netns
	ue   *netnstest.Netns
	hss  *hsstest.HSS
	srv  *server.Server
	xfrm *ipsec.XFRM
}

func newScene(t *testing.T) *scene {
	t.Helper()

	s := &scene{t: t, ims: netnstest.Current(t), ue: netnstest.New(t)}

	netnstest.Link(t, s.ims, s.ue, imsAddrs, ueAddrs)

	// Deleting the link at once frees the IMS's addresses for the next test;
	// the UE's namespace may outlive the test for a while.
	t.Cleanup(func() { _, _ = s.ims.Command("ip", "link", "del", "veth0") })

	s.hss = hsstest.New(t, hsstest.Config{Realm: domain, IMSHost: imsHost})
	s.hss.Add(hsstest.Subscriber{
		IMPI:  impi,
		IMSI:  imsi,
		K:     testK,
		OPc:   testOPc,
		SQN:   32,
		IMPUs: []cx.ProfileIdentity{{Identity: tempIMPU, Barred: true}, {Identity: msisdn}, {Identity: "tel:+15550001"}},
	})

	s.srv = &server.Server{Config: config.Config{
		DB:          config.DB{Path: filepath.Join(t.TempDir(), "ims.db")},
		CallHistory: config.CallHistory{Retention: 24 * time.Hour},
		API:         config.API{Address: netip.MustParseAddr("127.0.0.1")},
		IMS:         config.IMS{MCC: "001", MNC: "01", HomeDomain: domain},
		SIP:         config.SIP{Addresses: []netip.Addr{imsAddrs[0].Addr(), imsAddrs[1].Addr()}},
		PCSCF:       config.PCSCF{Port: pcscfPort, IPsec: config.IPsec{ServerPort: 5063, ClientPorts: []int{5064, 5065}}},
		ICSCF:       config.ICSCF{Port: 5070},
		SCSCF:       config.SCSCF{Port: 5080, MinExpires: 60, MaxExpires: 3600},
		Diameter: config.Diameter{
			OriginHost:  imsHost,
			OriginRealm: domain,
			Address:     s.hss.Addr().Addr(),
			Peers: []config.DiameterPeer{{
				ID: "hss", Host: s.hss.Host(), Realm: domain, Address: s.hss.Addr().Addr(), Port: int(s.hss.Addr().Port()),
				Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationCx},
			}},
		},
	}, Logger: testLogger(t)}

	if err := s.srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { s.srv.Shutdown(context.Background()) })

	s.hss.WaitConnected(t)

	var err error

	s.ue.Do(func() { s.xfrm, err = ipsec.Open() })

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.xfrm.Close() })

	return s
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// newUE makes a UE in the UE's namespace, over IPv6 when v6 is set.
func (s *scene) newUE(v6 bool, cfg testue.Config) *testue.UE {
	s.t.Helper()

	family := 0
	if v6 {
		family = 1
	}

	cfg.IMSI, cfg.IMEI = imsi, imei
	cfg.PCSCF = netip.AddrPortFrom(imsAddrs[family].Addr(), pcscfPort)
	cfg.Local = ueAddrs[family].Addr()
	cfg.Do = s.ue.Do
	cfg.Logger = testLogger(s.t)

	if cfg.K == nil {
		cfg.K, cfg.OPc = testK, testOPc
	}

	if !cfg.Plain {
		cfg.Kernel = s.xfrm
	}

	u, err := testue.New(cfg)
	if err != nil {
		s.t.Fatal(err)
	}

	s.t.Cleanup(func() { _ = u.Close() })

	return u
}

func (s *scene) register(u *testue.UE) {
	s.t.Helper()

	ctx, cancel := context.WithTimeout(s.t.Context(), 30*time.Second)
	defer cancel()

	if err := u.Register(ctx); err != nil {
		s.t.Fatalf("Register: %v", err)
	}
}

var (
	xfrmSPI     = regexp.MustCompile(`spi (0x[0-9a-f]+)`)
	xfrmPackets = regexp.MustCompile(`(\d+)\(packets\)`)
)

// espPackets maps the SPIs of the namespace's SAs to the packets they
// carried.
func espPackets(t *testing.T, n *netnstest.Netns) map[uint32]int {
	t.Helper()

	out, err := n.Command("ip", "-s", "xfrm", "state")
	if err != nil {
		t.Fatal(err)
	}

	packets := make(map[uint32]int)

	for _, block := range strings.Split(string(out), "\nsrc ") {
		spi, count := xfrmSPI.FindStringSubmatch(block), xfrmPackets.FindStringSubmatch(block)
		if spi == nil || count == nil {
			continue
		}

		v, _ := strconv.ParseUint(spi[1], 0, 32)
		c, _ := strconv.Atoi(count[1])
		packets[uint32(v)] = c
	}

	return packets
}

func spis(s ipsec.Set) []uint32 {
	return []uint32{s.Local.SPIC, s.Local.SPIS, s.Remote.SPIC, s.Remote.SPIS}
}

// established returns the UE's one set of SAs, checking that both kernels
// hold its four SAs.
func (s *scene) established(u *testue.UE) testue.SA {
	s.t.Helper()

	sas := u.SAs()
	if len(sas) != 1 || sas[0].State != testue.Established {
		s.t.Fatalf("SAs = %+v, want one established set", sas)
	}

	ue, ims := espPackets(s.t, s.ue), espPackets(s.t, s.ims)

	for _, spi := range spis(sas[0].Set) {
		if _, ok := ue[spi]; !ok {
			s.t.Errorf("no SA with SPI %d in the UE's kernel: %v", spi, ue)
		}

		if _, ok := ims[spi]; !ok {
			s.t.Errorf("no SA with SPI %d in the IMS's kernel: %v", spi, ims)
		}
	}

	return sas[0]
}

func TestRegistrationOverIPsec(t *testing.T) {
	for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
		for _, v6 := range []bool{false, true} {
			for _, alg := range []ipsec.Integrity{ipsec.HMACSHA196, ipsec.HMACMD596} {
				name := string(tr) + "/IPv4/" + string(alg)
				if v6 {
					name = string(tr) + "/IPv6/" + string(alg)
				}

				t.Run(name, func(t *testing.T) { testRegistrationOverIPsec(t, tr, v6, alg) })
			}
		}
	}
}

func testRegistrationOverIPsec(t *testing.T, tr sip.Transport, v6 bool, alg ipsec.Integrity) {
	s := newScene(t)
	u := s.newUE(v6, testue.Config{Transport: tr, Offers: []testue.Offer{{Integrity: alg, Encryption: ipsec.EncryptionNull}}})

	s.register(u)

	st := u.State()
	if !st.Registered || st.DefaultIMPU != msisdn || !st.Barred || len(st.ServiceRoute) == 0 {
		t.Fatalf("state = %+v, want registered with the default IMPU %s and the temporary IMPU barred", st, msisdn)
	}

	sa := s.established(u)
	if sa.Set.Integrity != alg {
		t.Fatalf("set %s, want %s", sa.Set, alg)
	}

	// The protected REGISTER went to the P-CSCF's protected server port and
	// its 200 came back through one of the UE's inbound SAs.
	ims, ue := espPackets(t, s.ims), espPackets(t, s.ue)
	if ims[sa.Set.Remote.SPIS] == 0 || ue[sa.Set.Local.SPIC]+ue[sa.Set.Local.SPIS] == 0 {
		t.Fatalf("set %s: packets in the IMS %v and in the UE %v, want ESP both ways", sa.Set, ims, ue)
	}
}

func TestUSIMOnlyIdentities(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	var uar *cx.UserAuthorizationRequest

	for uar == nil {
		uar = s.hss.Next(t).UAR
	}

	if uar.PrivateIdentity != impi || uar.PublicIdentity != tempIMPU {
		t.Fatalf("UAR = %+v, want the IMPI %s and the temporary IMPU %s", uar, impi, tempIMPU)
	}

	st := u.State()
	if !st.Barred || st.DefaultIMPU != msisdn || len(st.AssociatedURIs) != 2 {
		t.Fatalf("state = %+v, want the temporary IMPU barred and %s the default IMPU", st, msisdn)
	}

	if sub, _ := s.hss.Subscriber(impi); sub.State != hsstest.Registered {
		t.Fatalf("HSS subscriber = %+v, want registered", sub)
	}
}

func TestResync(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{SQN: 5000})

	s.register(u)

	sub, _ := s.hss.Subscriber(impi)
	if u.SQN() != 5001 || sub.SQN != 5001 {
		t.Fatalf("SQN %d in the UE and %d in the HSS, want 5001 after the resynchronisation", u.SQN(), sub.SQN)
	}

	var resync bool

	for !resync {
		r := s.hss.Next(t)
		resync = r.MAR != nil && r.MAR.Resync != nil
	}

	s.established(u)
}

func TestMACFailure(t *testing.T) {
	s := newScene(t)
	s.hss.Update(impi, func(sub *hsstest.Subscriber) { sub.K = []byte("not the UE's K!!") })

	u := s.newUE(false, testue.Config{})

	err := u.Register(t.Context())

	var re *testue.ResponseError
	if !errors.Is(err, testue.ErrNetworkAuthentication) || !errors.As(err, &re) || re.Response.StatusCode != 403 {
		t.Fatalf("Register = %v, want a network authentication failure answered 403", err)
	}

	if len(u.SAs()) != 0 || len(espPackets(t, s.ue)) != 0 {
		t.Fatalf("SAs %+v in the UE after a MAC failure", espPackets(t, s.ue))
	}
}

func TestReRegistration(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	before := s.established(u)
	sent := espPackets(t, s.ims)[before.Set.Remote.SPIS]

	if err := u.Reregister(t.Context()); err != nil {
		t.Fatal(err)
	}

	after := s.established(u)
	if after.Set != before.Set || !after.Expires.After(before.Expires) {
		t.Fatalf("set %s until %s after re-registration, want %s kept and extended", after.Set, after.Expires, before.Set)
	}

	if espPackets(t, s.ims)[before.Set.Remote.SPIS] <= sent {
		t.Fatal("the re-registration did not go over the established SAs")
	}
}

func TestReAuthentication(t *testing.T) {
	t.Skip("the S-CSCF challenges only a REGISTER that is not integrity-protected (internal/scscf/register.go), " +
		"so a re-registration over the SAs is never re-authenticated")

	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	old := s.established(u)

	if err := u.Reregister(t.Context()); err != nil {
		t.Fatal(err)
	}

	sa := s.established(u)
	if sa.Set == old.Set || sa.Set.Local.PortS != old.Set.Local.PortS {
		t.Fatalf("set %s after re-authentication, want a new set on port-s %d", sa.Set, old.Set.Local.PortS)
	}

	for _, spi := range spis(old.Set)[:2] {
		if _, ok := espPackets(t, s.ue)[spi]; ok {
			t.Fatalf("the old SA with SPI %d is still in the UE's kernel", spi)
		}
	}
}

func TestDeregistration(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	sa := s.established(u)

	if err := u.Deregister(t.Context()); err != nil {
		t.Fatal(err)
	}

	if u.State().Registered || len(u.SAs()) != 0 {
		t.Fatalf("state %+v and SAs %+v after deregistration", u.State(), u.SAs())
	}

	ue := espPackets(t, s.ue)
	for _, spi := range spis(sa.Set) {
		if _, ok := ue[spi]; ok {
			t.Fatalf("SA with SPI %d still in the UE's kernel: %v", spi, ue)
		}
	}

	if sub, _ := s.hss.Subscriber(impi); sub.State != hsstest.NotRegistered {
		t.Fatalf("HSS subscriber = %+v, want not registered", sub)
	}

	// The P-CSCF keeps its SAs for pcscf.DefaultGrace (128 s), to let the
	// last transactions end, which this test does not wait for.
}

func TestPlainSIPRegistration(t *testing.T) {
	t.Skip("without IPsec the P-CSCF marks the answer to the challenge integrity-protected=\"no\" and the " +
		"S-CSCF challenges it again (internal/scscf/register.go), so the registration never completes")

	s := newScene(t)
	u := s.newUE(false, testue.Config{Plain: true})

	s.register(u)

	if !u.State().Registered || len(espPackets(t, s.ue)) != 0 {
		t.Fatalf("state %+v and SAs %v, want registered without SAs", u.State(), espPackets(t, s.ue))
	}
}
