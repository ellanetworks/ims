package server

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	aliceIMPI = "001010000000011@" + imsRealm
	aliceTel  = "tel:+15550011"
	bobIMPI   = "001010000000012@" + imsRealm
	bobTel    = "tel:+15550012"
	bobPhone  = "sip:+15550012@" + imsRealm + ";user=phone"
	carolIMPI = "001010000000013@" + imsRealm
	carolTel  = "tel:+15550013"
)

// callScene is the IMS with two registered subscribers, alice and bob, whose
// P-CSCFs are test sockets: the S-CSCF, I-CSCF and HSS are real.
type callScene struct {
	scscf netip.AddrPort

	// alice is the originating P-CSCF; aliceRoute is her Service-Route.
	alice      *siptest.Socket
	aliceRoute string

	// bob is the terminating P-CSCF on bob's Path; bobContact his contact.
	bob        *siptest.Socket
	bobContact string
}

func newCallScene(t *testing.T, numbering config.Numbering) *callScene {
	t.Helper()

	hss := hsstest.New(t, hsstest.Config{Realm: imsRealm, IMSHost: imsHost})

	for _, s := range []struct {
		impi  string
		impus []string
	}{
		{aliceIMPI, []string{"sip:" + aliceIMPI, "sip:+15550011@" + imsRealm + ";user=phone", aliceTel}},
		{bobIMPI, []string{"sip:" + bobIMPI, bobPhone, bobTel}},
		{carolIMPI, []string{"sip:" + carolIMPI, carolTel}},
	} {
		sub := hsstest.Subscriber{IMPI: s.impi, IMSI: s.impi[:15], K: testK, OPc: testOPc, SQN: 32}
		for _, impu := range s.impus {
			sub.IMPUs = append(sub.IMPUs, cx.ProfileIdentity{Identity: impu})
		}

		hss.Add(sub)
	}

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Diameter = diameterConfig(config.DiameterPeer{
		ID: "hss", Host: hss.Host(), Realm: hss.Realm(), Address: hss.Addr().Addr(), Port: int(hss.Addr().Port()),
		Transport: config.TransportTCP, Applications: []config.Application{config.ApplicationCx},
	})
	cfg.IMS.Numbering = numbering
	cfg.SCSCF.MinExpires, cfg.SCSCF.MaxExpires = 60, 3600

	srv := startIMS(t, cfg)
	hss.WaitConnected(t)

	sc := &callScene{
		scscf: sipListener(t, srv, roleSCSCF, loopback),
		alice: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		bob:   siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
	}

	sc.aliceRoute = registerAtSCSCF(t, sc.scscf, sc.alice, aliceIMPI, "sip:alice@"+loopback.String()+":6000")
	sc.bobContact = "sip:bob@" + loopback.String() + ":6001"
	registerAtSCSCF(t, sc.scscf, sc.bob, bobIMPI, sc.bobContact)

	return sc
}

// registerAtSCSCF registers a subscriber at the S-CSCF as the P-CSCF does,
// with the P-CSCF's socket in Path, and returns the Service-Route on the
// S-CSCF's address.
func registerAtSCSCF(t *testing.T, scscf netip.AddrPort, pcscf *siptest.Socket, impi, contact string) string {
	t.Helper()

	impu := "sip:" + impi
	callID := sip.NewTag() + "@" + loopback.String()
	cseq := 0

	register := func(auth string) *sip.Response {
		cseq++

		r := siptest.NewRequest("REGISTER", "sip:"+imsRealm, sip.UDP, pcscf.Addr())
		r.Header.Set("To", "<"+impu+">")
		r.Header.Set("From", "<"+impu+">;tag="+sip.NewTag())
		r.Header.Set("Call-ID", callID)
		r.Header.Set("CSeq", strconv.Itoa(cseq)+" REGISTER")
		r.Header.Set("Contact", "<"+contact+">;+g.3gpp.icsi-ref=\"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel\"")
		r.Header.Add("Expires", "600")
		r.Header.Add("Path", "<sip:term@"+pcscf.Addr().String()+";lr>")
		r.Header.Add("Authorization", auth)
		pcscf.Send(sip.UDP, scscf, r)

		for {
			res, _ := pcscf.RecvResponse()
			if res.StatusCode != 100 {
				return res
			}
		}
	}

	res := register(fmt.Sprintf(`Digest username="%s", realm="%s", uri="sip:%s", nonce="", response="", integrity-protected="no"`,
		impi, imsRealm, imsRealm))
	if res.StatusCode != 401 {
		t.Fatalf("got %q, want 401", res.StartLine())
	}

	a, _ := sip.ParseAuth(res.Header.Get("WWW-Authenticate"))
	nonce, _ := a.Params.Get("nonce")
	nonce = sip.Unquote(nonce)

	vector, _ := base64.StdEncoding.DecodeString(nonce)

	aka, err := milenage.Respond(testK, testOPc, vector[:16], vector[16:])
	if err != nil {
		t.Fatal(err)
	}

	const nc, cnonce = "00000001", "0a4f113b"

	uri := "sip:" + imsRealm
	ha1 := hexMD5([]byte(impi+":"+imsRealm+":"), aka.RES)
	ha2 := hexMD5([]byte("REGISTER:" + uri))
	response := hexMD5([]byte(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2))

	res = register(fmt.Sprintf(`Digest username="%s", realm="%s", uri="%s", nonce="%s", response="%s", algorithm=AKAv1-MD5, `+
		`qop=auth, nc=%s, cnonce="%s", integrity-protected="yes"`, impi, imsRealm, uri, nonce, response, nc, cnonce))
	if res.StatusCode != 200 {
		t.Fatalf("got %q, want 200", res.StartLine())
	}

	sr, err := sip.ParseAddress(res.Header.Get("Service-Route"))
	if err != nil {
		t.Fatal(err)
	}

	return "<sip:" + sr.URI.User + "@" + scscf.String() + ";lr>"
}

func hexMD5(parts ...[]byte) string {
	h := md5.New()
	for _, p := range parts {
		h.Write(p)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func (sc *callScene) invite(target string) *sip.Request {
	req := siptest.NewRequest("INVITE", target, sip.UDP, sc.alice.Addr())
	req.Header.Set("From", "<"+aliceTel+">;tag="+sip.NewTag())
	req.Header.Prepend("Route", sc.aliceRoute)
	req.Header.Add("P-Asserted-Identity", "<"+aliceTel+">")
	req.Header.Add("P-Preferred-Service", "urn:urn-7:3gpp-service.ims.icsi.mmtel")

	return req
}

func finalResponse(t *testing.T, s *siptest.Socket) *sip.Response {
	t.Helper()

	for {
		res, _ := s.RecvResponse()
		if res.StatusCode != 100 {
			return res
		}
	}
}

func TestCallThroughTheSCSCFAndICSCF(t *testing.T) {
	sc := newCallScene(t, config.Numbering{})

	invite := sc.invite(bobTel)
	sc.alice.Send(sip.UDP, sc.scscf, invite)

	got, f := sc.bob.RecvRequest()

	if got.URI.String() != sc.bobContact {
		t.Errorf("Request-URI = %s, want bob's contact %s", got.URI, sc.bobContact)
	}

	if v := got.Header.Values("P-Called-Party-ID"); !slices.Equal(v, []string{"<" + bobTel + ">"}) {
		t.Errorf("P-Called-Party-ID = %v, want <%s>", v, bobTel)
	}

	if v := got.Header.Values("P-Asserted-Identity"); !slices.Equal(v, []string{"<" + aliceTel + ">", "<sip:+15550011@" + imsRealm + ";user=phone>"}) {
		t.Errorf("P-Asserted-Identity = %v, want alice's tel URI and its SIP form", v)
	}

	rr, _ := got.Header.RecordRoutes()
	if len(rr) != 2 || rr[0].URI.User != "mt" || rr[1].URI.User != "mo" {
		t.Fatalf("Record-Route = %v, want the terminating and originating S-CSCF", got.Header.Values("Record-Route"))
	}

	if routes := got.Header.Values("Route"); len(routes) != 1 {
		t.Errorf("Route = %v, want bob's Path", routes)
	}

	answer := func(code int) {
		res := sip.NewResponse(got, code, "")
		_ = res.Header.SetToTag("bob")
		res.Header.Set("Contact", "<"+sc.bobContact+">")
		res.Header.Add("P-Asserted-Identity", "<"+bobPhone+">")
		res.Header.Add("Record-Route", "<sip:mt@"+sc.bob.Addr().String()+";lr>")

		for _, v := range got.Header.Values("Record-Route") {
			res.Header.Add("Record-Route", v)
		}

		sc.bob.Send(sip.UDP, f.Remote, res)
	}

	answer(180)

	ringing := finalResponse(t, sc.alice)
	if want := []string{"<" + bobPhone + ">", "<" + bobTel + ">"}; ringing.StatusCode != 180 || !slices.Equal(ringing.Header.Values("P-Asserted-Identity"), want) {
		t.Fatalf("alice got %q with P-Asserted-Identity %v, want 180 with %v", ringing.StartLine(),
			ringing.Header.Values("P-Asserted-Identity"), want)
	}

	answer(200)

	ok := finalResponse(t, sc.alice)
	if ok.StatusCode != 200 {
		t.Fatalf("alice got %q, want 200", ok.StartLine())
	}

	routeSet, _ := ok.Header.RecordRoutes()
	slices.Reverse(routeSet)

	inDialog := func(method string, seq int) *sip.Request {
		r := siptest.NewRequest(method, sc.bobContact, sip.UDP, sc.alice.Addr())
		r.Header.Set("Call-ID", invite.Header.CallID())
		r.Header.Set("From", invite.Header.Get("From"))
		r.Header.Set("To", ok.Header.Get("To"))
		r.Header.Set("CSeq", strconv.Itoa(seq)+" "+method)

		for _, a := range routeSet {
			r.Header.Add("Route", a.String())
		}

		return r
	}

	sc.alice.Send(sip.UDP, sc.scscf, inDialog("ACK", 1))

	if ack, _ := sc.bob.RecvRequest(); ack.Method != "ACK" || ack.URI.String() != sc.bobContact {
		t.Fatalf("bob's P-CSCF got:\n%s\nwant the ACK", ack)
	}

	sc.alice.Send(sip.UDP, sc.scscf, inDialog("BYE", 2))

	bye, f := sc.bob.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("bob's P-CSCF got %s, want BYE", bye.Method)
	}

	sc.bob.Send(sip.UDP, f.Remote, sip.NewResponse(bye, 200, ""))

	if res := finalResponse(t, sc.alice); res.StatusCode != 200 {
		t.Fatalf("alice got %q to BYE, want 200", res.StartLine())
	}
}

func TestCallToAHomeLocalNumber(t *testing.T) {
	sc := newCallScene(t, config.Numbering{CountryCode: "1", NationalPrefix: "0"})

	sc.alice.Send(sip.UDP, sc.scscf, sc.invite("tel:05550012;phone-context="+imsRealm))

	if got, _ := sc.bob.RecvRequest(); got.URI.String() != sc.bobContact {
		t.Fatalf("Request-URI = %s, want bob's contact %s", got.URI, sc.bobContact)
	}
}

func TestCallFailures(t *testing.T) {
	sc := newCallScene(t, config.Numbering{})

	tests := []struct {
		name   string
		target string
		code   int
	}{
		{"unknown number", "tel:+15550099", 404},
		{"unregistered subscriber", carolTel, 480},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc.alice.Send(sip.UDP, sc.scscf, sc.invite(tt.target))

			if res := finalResponse(t, sc.alice); res.StatusCode != tt.code {
				t.Fatalf("got %q, want %d", res.StartLine(), tt.code)
			}
		})
	}

	t.Run("MESSAGE", func(t *testing.T) {
		msg := sc.invite(bobTel)
		msg.Method = "MESSAGE"
		msg.Header.Set("CSeq", "1 MESSAGE")
		sc.alice.Send(sip.UDP, sc.scscf, msg)

		if res := finalResponse(t, sc.alice); res.StatusCode != 403 {
			t.Fatalf("got %q, want 403", res.StartLine())
		}
	})
}
