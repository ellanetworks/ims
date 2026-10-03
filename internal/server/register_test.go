package server

import (
	"bytes"
	"encoding/base64"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	testIMSI = "001010000000001"
	testIMPI = testIMSI + "@" + imsRealm
	testIMPU = "sip:" + testIMPI
)

var (
	testK   = []byte("0123456789abcdef")
	testOPc = []byte("fedcba9876543210")
)

func newHSS(t *testing.T) (*hsstest.HSS, config.DiameterPeer) {
	t.Helper()

	hss := hsstest.New(t, hsstest.Config{Realm: imsRealm, IMSHost: imsHost})
	hss.Add(hsstest.Subscriber{
		IMPI: testIMPI, IMSI: testIMSI, K: testK, OPc: testOPc, SQN: 32,
		IMPUs: []cx.ProfileIdentity{{Identity: testIMPU}},
	})

	return hss, config.DiameterPeer{
		ID: "hss", Host: hss.Host(), Realm: hss.Realm(), Address: hss.Addr().Addr(), Port: int(hss.Addr().Port()),
		Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationCx},
	}
}

func newRegister(ue netip.AddrPort) *sip.Request {
	register := siptest.NewRequest("REGISTER", "sip:"+imsRealm, sip.UDP, ue)
	register.Header.Set("To", "<"+testIMPU+">")
	register.Header.Set("From", "<"+testIMPU+">;tag="+sip.NewTag())
	register.Header.Add("Authorization", `Digest username="`+testIMPI+`", realm="`+imsRealm+`", uri="sip:`+imsRealm+
		`", nonce="", response=""`)

	return register
}

func TestRegisterWithIPsecThroughTheRoles(t *testing.T) {
	hss, peer := newHSS(t)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Diameter = diameterConfig(peer)

	kernel := ipsectest.NewKernel()
	srv := startIMSWith(t, cfg, kernel)
	hss.WaitConnected(t)

	pcscf := sipListener(t, srv, rolePCSCF, loopback)
	scscf := sipListener(t, srv, roleSCSCF, loopback)
	ue := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	register := newRegister(ue.Addr())
	register.Header.Add("Expires", "600")
	register.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;"+
		"port-s=6300;alg=hmac-sha-1-96;ealg=null")
	register.Header.Add("Require", "sec-agree")
	register.Header.Add("Proxy-Require", "sec-agree")
	ue.Send(sip.UDP, pcscf, register)

	uar := hss.Next(t).UAR
	if uar == nil || uar.PrivateIdentity != testIMPI || uar.PublicIdentity != testIMPU || uar.VisitedNetwork != imsRealm ||
		uar.AuthorizationType != cx.AuthorizationRegistration {
		t.Fatalf("UAR = %+v", uar)
	}

	mar := hss.Next(t).MAR
	if want := "sip:scscf." + imsRealm + ":" + strconv.Itoa(int(scscf.Port())); mar == nil || mar.ServerName != want ||
		mar.PrivateIdentity != testIMPI {
		t.Fatalf("MAR = %+v, want Server-Name %s", mar, want)
	}

	res := wantResponse(t, ue, 401, "REGISTER")

	a, err := sip.ParseAuth(res.Header.Get("WWW-Authenticate"))
	if err != nil {
		t.Fatal(err)
	}

	nonce, _ := a.Params.Get("nonce")

	vector, err := base64.StdEncoding.DecodeString(sip.Unquote(nonce))
	if err != nil || len(vector) != 32 || a.Params.Has("ck") || a.Params.Has("ik") {
		t.Fatalf("WWW-Authenticate = %q, want RAND and AUTN in the nonce, without ck and ik", res.Header.Get("WWW-Authenticate"))
	}

	c, err := milenage.New(testK, testOPc)
	if err != nil {
		t.Fatal(err)
	}

	_, ck, ik, _, err := c.F2345(vector[:16])
	if err != nil {
		t.Fatal(err)
	}

	installed := kernel.Installed()
	if len(installed) != 1 {
		t.Fatalf("installed = %v, want one set", installed)
	}

	protected := sipListener(t, srv, rolePCSCFProtected, loopback)

	for set, keys := range installed {
		if set.Server().String() != res.Header.Get("Security-Server") || !bytes.Equal(keys.CK, ck) ||
			!bytes.Equal(keys.IK, ik) || set.Local.PortS != protected.Port() {
			t.Fatalf("set %s with keys %x %x for Security-Server %q", set, keys.CK, keys.IK, res.Header.Get("Security-Server"))
		}
	}
}

func TestRegisterToSCSCFFromOutsideTheTrustDomain(t *testing.T) {
	hss, peer := newHSS(t)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Diameter = diameterConfig(peer)

	srv := startIMS(t, cfg)
	hss.WaitConnected(t)

	scscf := sipListener(t, srv, roleSCSCF, loopback)
	ue := siptest.NewSocket(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.2"), 0))

	register := newRegister(ue.Addr())
	register.Header.Set("Contact", "*")
	register.Header.Set("Expires", "0")
	register.Header.Set("Authorization", `Digest username="`+testIMPI+`", realm="`+imsRealm+`", uri="sip:`+imsRealm+
		`", nonce="", response="", integrity-protected="yes"`)
	ue.Send(sip.UDP, scscf, register)

	wantResponse(t, ue, 403, "REGISTER")

	select {
	case r := <-hss.Requests():
		t.Fatalf("the S-CSCF sent %s", r)
	case <-time.After(100 * time.Millisecond):
	}
}
