//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/netnstest"
	"github.com/ellanetworks/ims/internal/pcrftest"
	"github.com/ellanetworks/ims/internal/pcscf"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/internal/testue"
	"github.com/ellanetworks/ims/sip"
)

const (
	domain  = "ims.mnc001.mcc001.3gppnetwork.org"
	imsHost = "ims." + domain

	pcscfPort = 5060

	pcscfIPsecServerPort = 5063
)

const subscribers = 4

type subscriber struct {
	imsi, impi, imei string
	msisdn, tel      string
}

func subscriberAt(i int) subscriber {
	n := fmt.Sprint(i + 1)
	imsi := "00101000000000" + n

	return subscriber{
		imsi:   imsi,
		impi:   imsi + "@" + domain,
		imei:   fmt.Sprintf("356938035643%02d", 80+i),
		msisdn: "sip:+1555000" + n + "@" + domain,
		tel:    "tel:+1555000" + n,
	}
}

func ueAddrsAt(i int) []netip.Prefix {
	return []netip.Prefix{
		netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 0, 0, byte(2 + i)}), 24),
		netip.PrefixFrom(netip.MustParseAddr(fmt.Sprintf("fd00::%x", 2+i)), 64),
	}
}

var (
	impi     = subscriberAt(0).impi
	tempIMPU = "sip:" + impi
	msisdn   = subscriberAt(0).msisdn

	imsAddrs = []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24"), netip.MustParsePrefix("fd00::1/64")}
	ueAddrs  = ueAddrsAt(0)

	testK   = []byte("0123456789abcdef")
	testOPc = []byte("fedcba9876543210")
)

func TestMain(m *testing.M) {
	netnstest.Main(m)
}

type scene struct {
	t     *testing.T
	ims   *netnstest.Netns
	ue    *netnstest.Netns
	hosts map[int]*host
	hss   *hsstest.HSS
	pcrf  *pcrftest.PCRF
	srv   *server.Server
	db    string
	wire  *wireLog
	rec   *recorder
}

func (s *scene) in(t *testing.T) *scene {
	c := *s
	c.t = t

	return &c
}

type host struct {
	ns   *netnstest.Netns
	xfrm *ipsec.XFRM
}

func newScene(t *testing.T) *scene {
	t.Helper()

	return newSceneWith(t, nil)
}

func newSceneWith(t *testing.T, configure func(*config.Config)) *scene {
	t.Helper()

	s := &scene{
		t: t, ims: netnstest.Current(t), hosts: map[int]*host{}, db: filepath.Join(t.TempDir(), "ims.db"),
		wire: &wireLog{}, rec: &recorder{},
	}

	netnstest.Bridge(t, s.ims, "br0", imsAddrs)

	s.hss = hsstest.New(t, hsstest.Config{Realm: domain, IMSHost: imsHost})

	for i := range subscribers {
		sub := subscriberAt(i)
		s.hss.Add(hsstest.Subscriber{
			IMPI: sub.impi,
			IMSI: sub.imsi,
			K:    testK,
			OPc:  testOPc,
			SQN:  32,
			IMPUs: []cx.ProfileIdentity{
				{Identity: "sip:" + sub.impi, Barred: true}, {Identity: sub.msisdn}, {Identity: sub.tel},
			},
		})
	}

	s.pcrf = pcrftest.New(t, pcrftest.Config{Realm: "epc.mnc001.mcc001.3gppnetwork.org", IMSHost: imsHost, IMSRealm: domain})

	s.srv = &server.Server{Config: config.Config{
		DB:    config.DB{Path: s.db},
		API:   config.API{Address: netip.MustParseAddr("127.0.0.1")},
		IMS:   config.IMS{MCC: "001", MNC: "01", HomeDomain: domain},
		SIP:   config.SIP{Addresses: []netip.Addr{imsAddrs[0].Addr(), imsAddrs[1].Addr()}},
		PCSCF: config.PCSCF{Port: pcscfPort, IPsec: config.IPsec{ServerPort: pcscfIPsecServerPort, ClientPorts: []int{5064, 5065}}},
		ICSCF: config.ICSCF{Port: 5070},
		SCSCF: config.SCSCF{Port: 5080, MinExpires: 60, MaxExpires: 3600},
		Diameter: config.Diameter{
			OriginHost:  imsHost,
			OriginRealm: domain,
			Address:     s.hss.Addr().Addr(),
			Peers: []config.DiameterPeer{{
				ID: "hss", Host: s.hss.Host(), Realm: domain, Address: s.hss.Addr().Addr(), Port: int(s.hss.Addr().Port()),
				Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationCx},
			}, {
				ID: "pcrf", Host: s.pcrf.Host(), Realm: s.pcrf.Realm(), Address: s.pcrf.Addr().Addr(),
				Port: int(s.pcrf.Addr().Port()), Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationRx},
			}},
		},
	}, Logger: testLogger(t)}

	if configure != nil {
		configure(&s.srv.Config)
	}

	if err := s.srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { s.srv.Shutdown(context.Background()) })

	s.hss.WaitConnected(t)
	s.pcrf.WaitConnected(t)

	t.Cleanup(func() { s.record("") })

	s.ue = s.host(0).ns

	return s
}

func (s *scene) host(i int) *host {
	s.t.Helper()

	if h, ok := s.hosts[i]; ok {
		return h
	}

	h := &host{ns: netnstest.New(s.t)}

	netnstest.Attach(s.t, s.ims, "br0", fmt.Sprintf("ue%d", i), h.ns, ueAddrsAt(i))

	var err error

	h.ns.Do(func() { h.xfrm, err = ipsec.Open() })

	if err != nil {
		s.t.Fatal(err)
	}

	s.t.Cleanup(func() { _ = h.xfrm.Close() })

	s.hosts[i] = h

	return h
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (s *scene) newUE(v6 bool, cfg testue.Config) *testue.UE {
	s.t.Helper()

	return s.newUEAt(0, v6, cfg)
}

func (s *scene) newUEAt(i int, v6 bool, cfg testue.Config) *testue.UE {
	s.t.Helper()

	family := 0
	if v6 {
		family = 1
	}

	h, sub := s.host(i), subscriberAt(i)

	cfg.IMSI, cfg.IMEI = sub.imsi, sub.imei
	cfg.PCSCF = netip.AddrPortFrom(imsAddrs[family].Addr(), pcscfPort)
	cfg.Local = ueAddrsAt(i)[family].Addr()
	cfg.Do = h.ns.Do
	cfg.Logger = testLogger(s.t).With("ue", i)
	cfg.Trace = s.tracer(i, cfg.Plain)

	if cfg.K == nil {
		cfg.K, cfg.OPc = testK, testOPc
	}

	if !cfg.Plain {
		cfg.Kernel = h.xfrm
	}

	u, err := testue.New(cfg)
	if err != nil {
		s.t.Fatal(err)
	}

	s.t.Cleanup(func() { _ = u.Close() })

	return u
}

func (s *scene) ctx() context.Context {
	ctx, cancel := context.WithTimeout(s.t.Context(), 30*time.Second)
	s.t.Cleanup(cancel)

	return ctx
}

func (s *scene) register(u *testue.UE) {
	s.t.Helper()

	if err := u.Register(s.ctx()); err != nil {
		s.t.Fatalf("Register: %v", err)
	}
}

func (s *scene) pcscfSAs() []db.SecurityAssociation {
	s.t.Helper()

	d, err := db.Open(s.t.Context(), s.db)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	sas, err := d.ListSecurityAssociations(s.t.Context())
	if err != nil {
		s.t.Fatal(err)
	}

	return sas
}

var (
	xfrmSPI     = regexp.MustCompile(`spi (0x[0-9a-f]+)`)
	xfrmPackets = regexp.MustCompile(`(\d+)\(packets\)`)
)

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

	ims, ue := espPackets(t, s.ims), espPackets(t, s.ue)
	if ims[sa.Set.Remote.SPIS] == 0 || ue[sa.Set.Local.SPIC] == 0 {
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

	err := u.Register(s.ctx())

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

	s.hss.Drain()
	drain(u)

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	after := s.established(u)
	if after.Set != before.Set || !after.Expires.After(before.Expires) {
		t.Fatalf("set %s until %s after re-registration, want %s kept and extended", after.Set, after.Expires, before.Set)
	}

	if espPackets(t, s.ims)[before.Set.Remote.SPIS] <= sent {
		t.Fatal("the re-registration did not go over the established SAs")
	}

	for {
		select {
		case r := <-s.hss.Requests():
			if r.MAR != nil {
				t.Fatalf("%s during the re-registration", r)
			}

			continue
		default:
		}

		break
	}

	for {
		select {
		case e := <-u.Events():
			if e.Response != nil && e.Response.StatusCode == 401 {
				t.Fatalf("401 during the re-registration:\n%s", e.Response)
			}

			continue
		default:
		}

		break
	}
}

func drain(u *testue.UE) {
	for {
		select {
		case <-u.Events():
		default:
			return
		}
	}
}

func TestReAuthentication(t *testing.T) {
	s := newSceneWith(t, func(c *config.Config) { c.SCSCF.ReauthInterval = time.Nanosecond })
	u := s.newUE(false, testue.Config{NoRegEvent: true})

	s.register(u)

	old := s.established(u)

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	sas := u.SAs()
	if len(sas) != 2 || sas[0].State != testue.Old || sas[0].Set != old.Set || sas[1].State != testue.Established ||
		sas[1].Set.Local.PortS != old.Set.Local.PortS || sas[1].Set.Local.PortC == old.Set.Local.PortC {
		t.Fatalf("SAs = %+v after re-authentication, want the old set and a new one on port-s %d", sas, old.Set.Local.PortS)
	}

	set := sas[1].Set

	if espPackets(t, s.ims)[set.Remote.SPIS] == 0 {
		t.Fatalf("no ESP on the new set %s", set)
	}

	for _, spi := range spis(set) {
		if _, ok := espPackets(t, s.ims)[spi]; !ok {
			t.Fatalf("no SA with SPI %d in the IMS's kernel", spi)
		}
	}

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	ue := espPackets(t, s.ue)
	for _, spi := range spis(old.Set) {
		if _, ok := ue[spi]; ok {
			t.Fatalf("the old SA with SPI %d is still in the UE's kernel", spi)
		}
	}

	if sas := u.SAs(); len(sas) != 2 || sas[0].Set != set || sas[0].State != testue.Old {
		t.Fatalf("SAs = %+v after the next re-authentication, want %s old", sas, set)
	}
}

func TestDeregistration(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	sa := s.established(u)

	if err := u.Deregister(s.ctx()); err != nil {
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

	deadline := time.Now().Add(5 * time.Second)

	for {
		sas := s.pcscfSAs()

		scheduled := len(sas) > 0
		for _, sa := range sas {
			scheduled = scheduled && sa.IMPI == impi && !sa.ExpiresAt.After(time.Now().Add(pcscf.DefaultGrace))
		}

		if scheduled {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("P-CSCF SAs %+v, want them expiring within %s", sas, pcscf.DefaultGrace)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func TestPlainSIPRegistrationRejected(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{Plain: true})

	err := u.Register(s.ctx())

	var re *testue.ResponseError
	if !errors.As(err, &re) || re.Response.StatusCode != 421 || re.Response.Header.Get("Require") != "sec-agree" ||
		!re.Response.Header.Has("Security-Server") {
		t.Fatalf("Register = %v, want 421 requiring sec-agree, with Security-Server", err)
	}

	if u.State().Registered || len(espPackets(t, s.ue)) != 0 {
		t.Fatalf("state %+v and SAs %v after the rejection", u.State(), espPackets(t, s.ue))
	}
}
