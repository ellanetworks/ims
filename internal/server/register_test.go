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
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

// TestRegisterThroughTheRoles sends a REGISTER to the P-CSCF port: it crosses
// the I-CSCF, which queries the HSS, and reaches the S-CSCF, which challenges
// it.
func TestRegisterThroughTheRoles(t *testing.T) {
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

	srv := startIMS(t, cfg)
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
	ue.Send(sip.UDP, pcscf, register)

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

// TestRegisterToSCSCFFromOutsideTheTrustDomain sends a REGISTER straight to
// the S-CSCF port, claiming the integrity protection only the P-CSCF can
// vouch for (TS 33.203 §6.1): it is refused without reaching the HSS.
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
