package server

import (
	"bytes"
	"context"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func TestRegisterThroughTheRoles(t *testing.T) {
	testRegisterThroughTheRoles(t, false)
}

func TestRegisterWithIPsecThroughTheRoles(t *testing.T) {
	testRegisterThroughTheRoles(t, true)
}

func testRegisterThroughTheRoles(t *testing.T, secAgree bool) {
	uars := make(chan cx.UserAuthorizationRequest, 4)
	mars := make(chan cx.MultimediaAuthRequest, 4)

	mux := diameter.NewMux()
	mux.Handle(cx.ApplicationID, cx.CommandUserAuthorization, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			uar, err := cx.ParseUserAuthorizationRequest(req)
			if err != nil {
				return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
			}

			uars <- uar

			ans, _ := cx.NewUserAuthorizationAnswer(req, c.LocalIdentity(), cx.UserAuthorization{
				Result: tgpp.Experimental(tgpp.ResultFirstRegistration),
			})

			return ans
		}))
	mux.Handle(cx.ApplicationID, cx.CommandMultimediaAuth, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			mar, err := cx.ParseMultimediaAuthRequest(req)
			if err != nil {
				return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
			}

			mars <- mar

			ans, _ := cx.NewMultimediaAuthAnswer(req, c.LocalIdentity(), cx.MultimediaAuth{Items: []cx.AuthItem{{
				Scheme: cx.SchemeDigestAKAv1MD5,
				AKA: &cx.AKAVector{
					RAND: bytes.Repeat([]byte{1}, 16), AUTN: bytes.Repeat([]byte{2}, 16), XRES: bytes.Repeat([]byte{3}, 8),
					CK: bytes.Repeat([]byte{4}, 16), IK: bytes.Repeat([]byte{5}, 16),
				},
			}}})

			return ans
		}))

	hss := newFakePeerWithHandler(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, mux,
		config.ApplicationCx, config.ApplicationRx)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Diameter = diameterConfig(hss.config("hss"))

	kernel := ipsectest.NewKernel()
	srv := startIMSWith(t, cfg, kernel)
	waitOpen(t, srv, "hss")

	pcscf := sipListener(t, srv, rolePCSCF, loopback)
	scscf := sipListener(t, srv, roleSCSCF, loopback)
	ue := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	impi := "001010000000001@" + imsRealm
	impu := "sip:" + impi

	register := siptest.NewRequest("REGISTER", "sip:"+imsRealm, sip.UDP, ue.Addr())
	register.Header.Set("To", "<"+impu+">")
	register.Header.Set("From", "<"+impu+">;tag="+sip.NewTag())
	register.Header.Add("Expires", "600")
	register.Header.Add("Authorization", `Digest username="`+impi+`", realm="`+imsRealm+`", uri="sip:`+imsRealm+
		`", nonce="", response=""`)

	if secAgree {
		register.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;"+
			"port-s=6300;alg=hmac-sha-1-96;ealg=null")
		register.Header.Add("Require", "sec-agree")
		register.Header.Add("Proxy-Require", "sec-agree")
	}

	ue.Send(sip.UDP, pcscf, register)

	if !secAgree {
		if res := wantResponse(t, ue, 421, "REGISTER"); res.Header.Get("Require") != "sec-agree" {
			t.Fatalf("Require = %q, want sec-agree", res.Header.Get("Require"))
		}

		select {
		case uar := <-uars:
			t.Fatalf("UAR %+v for a REGISTER without IPsec", uar)
		case <-time.After(100 * time.Millisecond):
		}

		return
	}

	uar := next(t, uars)
	if uar.PrivateIdentity != impi || uar.PublicIdentity != impu || uar.VisitedNetwork != imsRealm ||
		uar.AuthorizationType != cx.AuthorizationRegistration {
		t.Fatalf("UAR = %+v", uar)
	}

	mar := next(t, mars)
	if want := "sip:scscf." + imsRealm + ":" + strconv.Itoa(int(scscf.Port())); mar.ServerName != want || mar.PrivateIdentity != impi {
		t.Fatalf("MAR = %+v, want Server-Name %s", mar, want)
	}

	res := wantResponse(t, ue, 401, "REGISTER")

	a, err := sip.ParseAuth(res.Header.Get("WWW-Authenticate"))
	if err != nil {
		t.Fatal(err)
	}

	if a.Params.Has("ck") || a.Params.Has("ik") || !a.Params.Has("nonce") {
		t.Fatalf("WWW-Authenticate = %q, want a challenge without ck and ik", res.Header.Get("WWW-Authenticate"))
	}

	installed := kernel.Installed()

	if !secAgree {
		if res.Header.Has("Security-Server") || len(installed) != 0 {
			t.Fatalf("Security-Server %q and %d sets for a REGISTER without sec-agree", res.Header.Get("Security-Server"), len(installed))
		}

		return
	}

	if len(installed) != 1 {
		t.Fatalf("installed = %v, want one set", installed)
	}

	for set, keys := range installed {
		protected := sipListener(t, srv, rolePCSCFProtected, loopback)

		if set.Server().String() != res.Header.Get("Security-Server") || !bytes.Equal(keys.CK, bytes.Repeat([]byte{4}, 16)) ||
			!bytes.Equal(keys.IK, bytes.Repeat([]byte{5}, 16)) || set.Local.PortS != protected.Port() {
			t.Fatalf("set %s with keys %x %x for Security-Server %q", set, keys.CK, keys.IK, res.Header.Get("Security-Server"))
		}
	}
}

func next[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	select {
	case v := <-ch:
		return v
	case <-time.After(siptest.Timeout):
		t.Fatal("timed out waiting for a Diameter request")
	}

	var zero T

	return zero
}

func TestRegisterToSCSCFFromOutsideTheTrustDomain(t *testing.T) {
	requests := make(chan uint32, 4)

	mux := diameter.NewMux()

	for _, cmd := range []uint32{cx.CommandMultimediaAuth, cx.CommandServerAssignment} {
		mux.Handle(cx.ApplicationID, cmd, diameter.HandlerFunc(
			func(_ context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
				requests <- req.CommandCode
				return nil
			}))
	}

	hss := newFakePeerWithHandler(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, mux,
		config.ApplicationCx, config.ApplicationRx)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Diameter = diameterConfig(hss.config("hss"))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "hss")

	scscf := sipListener(t, srv, roleSCSCF, loopback)
	ue := siptest.NewSocket(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.2"), 0))

	impi := "001010000000001@" + imsRealm
	impu := "sip:" + impi

	register := siptest.NewRequest("REGISTER", "sip:"+imsRealm, sip.UDP, ue.Addr())
	register.Header.Set("To", "<"+impu+">")
	register.Header.Set("From", "<"+impu+">;tag="+sip.NewTag())
	register.Header.Set("Contact", "*")
	register.Header.Set("Expires", "0")
	register.Header.Add("Authorization", `Digest username="`+impi+`", realm="`+imsRealm+`", uri="sip:`+imsRealm+
		`", nonce="", response="", integrity-protected="yes"`)
	ue.Send(sip.UDP, scscf, register)

	wantResponse(t, ue, 403, "REGISTER")

	select {
	case cmd := <-requests:
		t.Fatalf("the S-CSCF sent Cx command %d", cmd)
	case <-time.After(100 * time.Millisecond):
	}
}
