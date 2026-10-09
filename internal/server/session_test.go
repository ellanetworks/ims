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
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	aliceIMPI = "001010000000011@" + imsRealm
	aliceTel  = "tel:+15550011"
	bobIMPI   = "001010000000012@" + imsRealm
	bobTel    = "tel:+15550012"
	bobPhone  = "sip:+15550012@" + imsRealm + ";user=phone"
	bobLocal  = "tel:5550012"
	carolIMPI = "001010000000013@" + imsRealm
	carolTel  = "tel:+15550013"
)

type callScene struct {
	srv   *Server
	scscf netip.AddrPort

	alice      *siptest.Peer
	aliceRoute string

	bob        *siptest.Peer
	bobContact string
}

// newCallScene starts the IMS with a numbering plan, or the default one without.
func newCallScene(t *testing.T, numbering *settings.Numbering) *callScene {
	t.Helper()

	hss := hsstest.New(t, hsstest.Config{Realm: imsRealm, IMSHost: imsHost})

	for _, s := range []struct {
		impi  string
		impus []string
	}{
		{aliceIMPI, []string{"sip:" + aliceIMPI, "sip:+15550011@" + imsRealm + ";user=phone", aliceTel}},
		{bobIMPI, []string{"sip:" + bobIMPI, bobPhone, bobTel, bobLocal}},
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
	cfg.Peers = seedPeers(settings.Peer{
		ID: "hss", Host: hss.Host(), Realm: hss.Realm(), Address: hss.Addr().Addr(), Port: int(hss.Addr().Port()),
		Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationCx},
	})

	srv := startIMS(t, cfg)

	if numbering != nil {
		op := srv.settings.Get().Operator
		op.Numbering = *numbering

		if err := srv.settings.UpdateOperator(t.Context(), op); err != nil {
			t.Fatalf("UpdateOperator: %v", err)
		}
	}

	waitOpen(t, srv, "hss")

	sc := &callScene{
		srv:   srv,
		scscf: sipListener(t, srv, roleSCSCF, loopback),
		alice: newPeer(t, srv, netip.AddrPortFrom(loopback, 6100)),
		bob:   newPeer(t, srv, netip.AddrPortFrom(loopback, 6101)),
	}

	sc.aliceRoute = registerAtSCSCF(t, sc.scscf, sc.alice, aliceIMPI, "sip:alice@"+loopback.String()+":6000")
	sc.bobContact = "sip:bob@" + loopback.String() + ":6001"
	registerAtSCSCF(t, sc.scscf, sc.bob, bobIMPI, sc.bobContact)

	return sc
}

func registerAtSCSCF(t *testing.T, scscf netip.AddrPort, pcscf *siptest.Peer, impi, contact string) string {
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

func responseTo(t *testing.T, s node, req *sip.Request) *sip.Response {
	t.Helper()

	want, err := req.Header.CSeq()
	if err != nil {
		t.Fatal(err)
	}

	for {
		res, _ := s.RecvResponse()
		if cseq, _ := res.Header.CSeq(); res.StatusCode != 100 && res.Header.CallID() == req.Header.CallID() && cseq == want {
			return res
		}
	}
}

func failedInvite(t *testing.T, s node, to netip.AddrPort, invite *sip.Request) *sip.Response {
	t.Helper()

	res := responseTo(t, s, invite)
	if res.StatusCode < 300 {
		t.Fatalf("got %q, want a failure", res.StartLine())
	}

	ack, err := sip.NewAck(invite, res)
	if err != nil {
		t.Fatal(err)
	}

	s.Send(sip.UDP, to, ack)

	return res
}

func TestCallThroughTheSCSCFAndICSCF(t *testing.T) {
	sc := newCallScene(t, nil)

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

	ringing := responseTo(t, sc.alice, invite)
	if want := []string{"<" + bobPhone + ">", "<" + bobTel + ">"}; ringing.StatusCode != 180 || !slices.Equal(ringing.Header.Values("P-Asserted-Identity"), want) {
		t.Fatalf("alice got %q with P-Asserted-Identity %v, want 180 with %v", ringing.StartLine(),
			ringing.Header.Values("P-Asserted-Identity"), want)
	}

	answer(200)

	ok := responseTo(t, sc.alice, invite)
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

	byeReq := inDialog("BYE", 2)
	sc.alice.Send(sip.UDP, sc.scscf, byeReq)

	bye, f := sc.bob.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("bob's P-CSCF got %s, want BYE", bye.Method)
	}

	sc.bob.Send(sip.UDP, f.Remote, sip.NewResponse(bye, 200, ""))

	if res := responseTo(t, sc.alice, byeReq); res.StatusCode != 200 {
		t.Fatalf("alice got %q to BYE, want 200", res.StartLine())
	}
}

func TestCallToAHomeLocalNumber(t *testing.T) {
	sc := newCallScene(t, &settings.Numbering{CountryCode: "1", NationalPrefix: "0"})

	sc.alice.Send(sip.UDP, sc.scscf, sc.invite("tel:05550012;phone-context="+imsRealm))

	if got, _ := sc.bob.RecvRequest(); got.URI.String() != sc.bobContact {
		t.Fatalf("Request-URI = %s, want bob's contact %s", got.URI, sc.bobContact)
	}
}

func TestCallFromSamsung(t *testing.T) {
	sc := newCallScene(t, &settings.Numbering{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"})

	sc.alice.Send(sip.UDP, sc.scscf, sc.invite("sip:15550012;phone-context=15550011@15550011;user=phone"))

	if got, _ := sc.bob.RecvRequest(); got.URI.String() != sc.bobContact {
		t.Fatalf("Request-URI = %s, want bob's contact %s", got.URI, sc.bobContact)
	}
}

func TestCallFailures(t *testing.T) {
	sc := newCallScene(t, nil)

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
			invite := sc.invite(tt.target)
			sc.alice.Send(sip.UDP, sc.scscf, invite)

			if res := failedInvite(t, sc.alice, sc.scscf, invite); res.StatusCode != tt.code {
				t.Fatalf("got %q, want %d", res.StartLine(), tt.code)
			}
		})
	}

	t.Run("MESSAGE", func(t *testing.T) {
		msg := sc.invite(bobTel)
		msg.Method = "MESSAGE"
		msg.Header.Set("CSeq", "1 MESSAGE")
		sc.alice.Send(sip.UDP, sc.scscf, msg)

		if res := responseTo(t, sc.alice, msg); res.StatusCode != 403 {
			t.Fatalf("got %q, want 403", res.StartLine())
		}
	})

	sc.alice.RecvNone(2 * transaction.DefaultT1)
}
