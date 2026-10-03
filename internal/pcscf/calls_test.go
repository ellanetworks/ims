package pcscf

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
)

const callee = "tel:+15551239999"

func (s *ipsecScene) serviceRoute() string {
	return "<sip:orig@" + s.scscf.Addr().String() + ";lr>"
}

// ueInvite builds an INVITE from the UE as a Samsung phone sends it: a tel URI
// in From, and the P-CSCF's protected port and the Service-Route preloaded.
func (s *ipsecScene) ueInvite(u *ue, edit func(*sip.Request)) *sip.Request {
	r := siptest.NewRequest("INVITE", callee, sip.UDP, u.us.Addr())
	r.Header.Set("From", "<"+testTel+">;tag="+sip.NewTag())
	r.Header.Set("Contact", ueContact(u))
	r.Header.Add("Route", "<sip:"+s.ps.String()+";lr>, "+s.serviceRoute())

	if edit != nil {
		edit(r)
	}

	return r
}

// coreResponse answers a request the S-CSCF received, record-routing it as the
// rest of the core would.
func (s *ipsecScene) coreResponse(req *sip.Request, code int, tag string) *sip.Response {
	res := sip.NewResponse(req, code, "")
	_ = res.Header.SetToTag(tag)
	res.Header.Add("Contact", "<sip:callee@"+s.scscf.Addr().String()+">")
	res.Header.Add("Record-Route", "<sip:mt@"+s.scscf.Addr().String()+";lr>")

	for _, rr := range req.Header.Values("Record-Route") {
		res.Header.Add("Record-Route", rr)
	}

	return res
}

// uris are the URIs of a parsed header field, nil when it fails to parse.
func uris(as []sip.Address, err error) []string {
	if err != nil {
		return nil
	}

	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.URI.String()
	}

	return out
}

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

func wantAbsent(t *testing.T, h sip.Header, names ...string) {
	t.Helper()

	for _, n := range names {
		if h.Has(n) {
			t.Errorf("%s: %q present, want it removed", n, h.Values(n))
		}
	}
}

func chargingOf(t *testing.T, h sip.Header) chargingVector {
	t.Helper()

	cv, ok := parseChargingVector(h.Get("P-Charging-Vector"))
	if !ok {
		t.Fatalf("P-Charging-Vector = %q, want an icid", h.Get("P-Charging-Vector"))
	}

	return cv
}

func TestOriginatingCall(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)

	invite := s.ueInvite(u, func(r *sip.Request) {
		r.Header.Add("P-Preferred-Identity", "<"+testTel+">")
		r.Header.Add("P-Asserted-Identity", "<sip:bob@"+homeDomain+">")
		r.Header.Add("P-Charging-Vector", "icid-value=forged")
		r.Header.Add("Security-Verify", "ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=3;port-s=4")
		r.Header.Add("Require", "sec-agree")
		r.Header.Add("Proxy-Require", "sec-agree")
		r.Header.Add("Supported", "100rel, sec-agree")
		r.Header.Add("P-Access-Network-Info", "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b")
		r.Header.Add("P-Access-Network-Info", "3GPP-E-UTRAN-FDD;network-provided")
		r.Header.Add("P-Early-Media", "supported")
		r.Header.Add("Feature-Caps", "*;+g.3gpp.srvcc")
		r.Header.Add("P-Media-Authorization", "0020000100100101706366")
		r.Header.Add("Geolocation", "<cid:target@ue>;inserted-by=ue;loc-src=ue.example")
	})
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, first(u.us.RecvResponse()), 100)

	got, f := s.scscf.RecvRequest()
	if got.Method != "INVITE" {
		t.Fatalf("S-CSCF got %s, want the INVITE", got.Method)
	}

	if routes := uris(got.Header.Addresses("Route")); !slices.Equal(routes, []string{"sip:orig@" + s.scscf.Addr().String() + ";lr"}) {
		t.Errorf("Route = %q, want the Service-Route", routes)
	}

	if pai := got.Header.Values("P-Asserted-Identity"); !slices.Equal(pai, []string{"<" + testTel + ">"}) {
		t.Errorf("P-Asserted-Identity = %q, want the preferred tel URI", pai)
	}

	wantAbsent(t, got.Header, "P-Preferred-Identity", "Security-Verify", "Security-Client", "Require", "Proxy-Require",
		"Feature-Caps", "P-Media-Authorization")

	if pani := got.Header.Values("P-Access-Network-Info"); len(pani) != 1 || strings.Contains(pani[0], "network-provided") {
		t.Errorf("P-Access-Network-Info = %q, want the UE's own only", pani)
	}

	if v := got.Header.Get("P-Early-Media"); v != "supported" {
		t.Errorf("P-Early-Media = %q, want supported", v)
	}

	if v := got.Header.Get("Geolocation"); strings.Contains(v, "loc-src") || !strings.Contains(v, "cid:target@ue") {
		t.Errorf("Geolocation = %q, want it without loc-src", v)
	}

	cv := chargingOf(t, got.Header)
	if cv.icid == "forged" || cv.origIOI != homeDomain {
		t.Errorf("P-Charging-Vector = %q, want a new icid and the home orig-ioi", got.Header.Get("P-Charging-Vector"))
	}

	rr := uris(got.Header.RecordRoutes())
	if len(rr) != 2 {
		t.Fatalf("Record-Route = %q, want the P-CSCF twice", rr)
	}

	core, ueSide := mustURI(t, rr[0]), mustURI(t, rr[1])

	if core.Port != s.pcscf.Port() || ueSide.Port != s.ps.Port() || core.User != token || ueSide.User != token ||
		!ueSide.Params.Has(ueFacing) || core.Params.Has(ueFacing) {
		t.Errorf("Record-Route = %q, want the core side on %d and the UE side on the protected port %d, with the flow token",
			rr, s.pcscf.Port(), s.ps.Port())
	}

	// The callee's identity reaches the caller, unless it asks for privacy;
	// the core's own header fields do not.
	tag := sip.NewTag()

	ringing := s.coreResponse(got, 180, tag)
	ringing.Header.Add("P-Asserted-Identity", "<"+callee+">")
	ringing.Header.Add("P-Charging-Vector", "icid-value=core")
	ringing.Header.Add("P-Asserted-Service", icsiForTest)
	ringing.Header.Add("P-Access-Network-Info", "3GPP-E-UTRAN-FDD;network-provided")
	ringing.Header.Add("P-Early-Media", "sendrecv")
	s.scscf.Send(f.Transport, f.Remote, ringing)

	res, _ := u.us.RecvResponse()
	wantStatus(t, res, 180)

	if v := res.Header.Values("P-Asserted-Identity"); !slices.Equal(v, []string{"<" + callee + ">"}) {
		t.Errorf("P-Asserted-Identity to the caller = %q, want the callee's", v)
	}

	wantAbsent(t, res.Header, "P-Charging-Vector", "P-Asserted-Service", "P-Access-Network-Info")

	// The core may signal early media to the caller (RFC 5009 §6).
	if v := res.Header.Get("P-Early-Media"); v != "sendrecv" {
		t.Errorf("P-Early-Media to the caller = %q, want the core's sendrecv", v)
	}

	ok := s.coreResponse(got, 200, tag)
	ok.Header.Add("P-Asserted-Identity", "<"+callee+">")
	ok.Header.Add("Privacy", "id")
	s.scscf.Send(f.Transport, f.Remote, ok)

	res, _ = u.us.RecvResponse()
	wantStatus(t, res, 200)
	wantAbsent(t, res.Header, "P-Asserted-Identity")

	// The UE's route set is the 200's Record-Route reversed.
	routes := res.Header.Values("Record-Route")
	slices.Reverse(routes)

	from, _ := invite.Header.From()

	inDialog := func(method, cseq string, routes []string, fromTag string) *sip.Request {
		r := siptest.NewRequest(method, "sip:callee@"+s.scscf.Addr().String(), sip.UDP, u.us.Addr())
		r.Header.Set("From", "<"+testTel+">;tag="+fromTag)
		r.Header.Set("To", "<"+callee+">;tag="+tag)
		r.Header.Set("Call-ID", invite.Header.CallID())
		r.Header.Set("CSeq", cseq+" "+method)
		r.Header.Add("Route", strings.Join(routes, ", "))
		r.Header.Add("P-Preferred-Identity", "<"+testTel+">")
		r.Header.Add("Security-Verify", "ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=3;port-s=4")

		return r
	}

	u.uc.Send(sip.UDP, s.ps, inDialog("ACK", "1", routes, from.Tag()))

	ack, _ := s.scscf.RecvRequest()
	if ack.Method != "ACK" || chargingOf(t, ack.Header).icid != cv.icid {
		t.Fatalf("S-CSCF got:\n%s\nwant the ACK with the INVITE's icid", ack)
	}

	wantAbsent(t, ack.Header, "P-Preferred-Identity", "Security-Verify")

	// TS 24.229 §5.2.6.3.9: only a party of the dialog may send on it.
	u.uc.Send(sip.UDP, s.ps, inDialog("BYE", "2", routes, "stranger"))
	wantStatus(t, first(u.us.RecvResponse()), 403)
	s.scscf.RecvNone(quiet)

	// The routes are the dialog's, whatever the UE puts there.
	forged := slices.Clone(routes)
	forged[len(forged)-1] = "<sip:attacker@192.0.2.1;lr>"

	u.uc.Send(sip.UDP, s.ps, inDialog("BYE", "2", forged, from.Tag()))

	bye, bf := s.scscf.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", bye.Method)
	}

	if r := uris(bye.Header.Addresses("Route")); !slices.Equal(r, []string{"sip:mt@" + s.scscf.Addr().String() + ";lr"}) {
		t.Errorf("BYE Route = %q, want the dialog's", r)
	}

	if chargingOf(t, bye.Header).icid != cv.icid {
		t.Errorf("BYE P-Charging-Vector = %q, want the INVITE's icid %s", bye.Header.Get("P-Charging-Vector"), cv.icid)
	}

	s.scscf.Send(bf.Transport, bf.Remote, sip.NewResponse(bye, 200, ""))
	wantStatus(t, first(u.us.RecvResponse()), 200)
}

const icsiForTest = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

func TestOriginatingRoutesAreTheServiceRoute(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)

	u.uc.Send(sip.UDP, s.ps, s.ueInvite(u, func(r *sip.Request) {
		r.Header.Set("Route", "<sip:"+s.ps.String()+";lr>, <sip:attacker@192.0.2.1;lr>")
	}))

	got, _ := s.scscf.RecvRequest()
	if r := uris(got.Header.Addresses("Route")); !slices.Equal(r, []string{"sip:orig@" + s.scscf.Addr().String() + ";lr"}) {
		t.Errorf("Route = %q, want the Service-Route in place of the UE's", r)
	}

	// Without a P-Preferred-Identity, the default identity is asserted.
	if pai := got.Header.Values("P-Asserted-Identity"); !slices.Equal(pai, []string{"<" + testIMPU + ">"}) {
		t.Errorf("P-Asserted-Identity = %q, want the default identity", pai)
	}
}

func TestRequestsOutsideADialogAreRefused(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)

	for _, method := range []string{"UPDATE", "BYE", "PRACK"} {
		r := siptest.NewRequest(method, callee, sip.UDP, u.us.Addr())
		r.Header.Add("Route", "<sip:"+s.ps.String()+";lr>, "+s.serviceRoute())
		u.uc.Send(sip.UDP, s.ps, r)

		wantStatus(t, first(u.us.RecvResponse()), 403)
	}

	s.scscf.RecvNone(quiet)
}

// coreRequest builds a request from the terminating S-CSCF to the UE's
// contact, along its Path.
func (s *ipsecScene) coreRequest(u *ue, method, path string, edit func(*sip.Request)) *sip.Request {
	r := siptest.NewRequest(method, "sip:ue@"+u.us.Addr().String(), sip.UDP, s.scscf.Addr())
	r.Header.Set("From", "<sip:caller@"+homeDomain+">;tag="+sip.NewTag())
	r.Header.Set("To", "<"+testIMPU+">")
	r.Header.Set("Contact", "<sip:caller@"+s.scscf.Addr().String()+">")
	r.Header.Add("Route", "<"+path+">")
	r.Header.Add("Record-Route", "<sip:mt@"+s.scscf.Addr().String()+";lr>")
	r.Header.Add("P-Called-Party-ID", "<"+testIMPU+">")
	r.Header.Add("P-Charging-Vector", "icid-value=AB12;orig-ioi=other.example")

	if edit != nil {
		edit(r)
	}

	return r
}

func TestTerminatingCall(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)
	sets := s.installed()

	path := "sip:" + token + "@" + s.pcscf.String() + ";lr"

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", path, func(r *sip.Request) {
		r.Header.Add("P-Asserted-Identity", "<sip:caller@"+homeDomain+">")
		r.Header.Add("Privacy", "id")
		r.Header.Add("P-Asserted-Service", icsiForTest)
		r.Header.Add("P-Access-Network-Info", "3GPP-E-UTRAN-FDD;network-provided")
		r.Header.Add("P-Early-Media", "supported")
	}))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()
	if got.Method != "INVITE" {
		t.Fatalf("UE got %s, want the INVITE", got.Method)
	}

	if want := netip.AddrPortFrom(loopback, sets[0].Local.PortC); f.Remote != want {
		t.Errorf("INVITE from %s, want the protected client port %s", f.Remote, want)
	}

	if via := mustTopVia(t, got); via.Port != s.ps.Port() {
		t.Errorf("Via = %s, want the protected server port %d", via, s.ps.Port())
	}

	wantAbsent(t, got.Header, "P-Asserted-Identity", "P-Charging-Vector", "P-Asserted-Service", "P-Access-Network-Info")

	if v := got.Header.Get("P-Called-Party-ID"); v != "<"+testIMPU+">" {
		t.Errorf("P-Called-Party-ID = %q, want it kept", v)
	}

	if v := got.Header.Get("P-Early-Media"); v != "supported" {
		t.Errorf("P-Early-Media = %q, want the caller's supported", v)
	}

	rr := uris(got.Header.RecordRoutes())
	if len(rr) != 3 {
		t.Fatalf("Record-Route = %q, want the P-CSCF twice above the S-CSCF", rr)
	}

	ueSide, core := mustURI(t, rr[0]), mustURI(t, rr[1])

	if ueSide.Port != s.ps.Port() || !ueSide.Params.Has(ueFacing) || core.Port != s.pcscf.Port() || core.Params.Has(ueFacing) ||
		ueSide.User != token || core.User != token {
		t.Errorf("Record-Route = %q, want the UE side on the protected port %d and the core side on %d", rr, s.ps.Port(), s.pcscf.Port())
	}

	// The UE's responses carry the identity it was called on, and no early
	// media authorisation (TS 24.229 §5.2.6.4.4, Decision 7).
	tag := sip.NewTag()

	ringing := sip.NewResponse(got, 180, "")
	_ = ringing.Header.SetToTag(tag)
	ringing.Header.Add("Contact", ueContact(u))
	ringing.Header.Add("P-Asserted-Identity", "<sip:mallory@"+homeDomain+">")
	ringing.Header.Add("P-Preferred-Identity", "<sip:mallory@"+homeDomain+">")
	ringing.Header.Add("P-Early-Media", "sendrecv")
	ringing.Header.Add("P-Charging-Vector", "icid-value=forged")

	for _, v := range got.Header.Values("Record-Route") {
		ringing.Header.Add("Record-Route", v)
	}

	u.us.Send(sip.UDP, f.Remote, ringing)

	res, _ := s.scscf.RecvResponse()
	wantStatus(t, res, 180)

	if v := res.Header.Values("P-Asserted-Identity"); !slices.Equal(v, []string{"<" + testIMPU + ">"}) {
		t.Errorf("P-Asserted-Identity = %q, want the P-Called-Party-ID", v)
	}

	wantAbsent(t, res.Header, "P-Preferred-Identity", "P-Early-Media")

	cv := chargingOf(t, res.Header)
	if cv.icid != "AB12" || !strings.Contains(res.Header.Get("P-Charging-Vector"), "term-ioi="+homeDomain) {
		t.Errorf("P-Charging-Vector = %q, want the INVITE's icid and the home term-ioi", res.Header.Get("P-Charging-Vector"))
	}

	answer := sip.NewResponse(got, 200, "")
	_ = answer.Header.SetToTag(tag)
	answer.Header.Add("Contact", ueContact(u))

	for _, v := range got.Header.Values("Record-Route") {
		answer.Header.Add("Record-Route", v)
	}

	u.us.Send(sip.UDP, f.Remote, answer)
	wantStatus(t, first(s.scscf.RecvResponse()), 200)

	// The callee's route set is the INVITE's Record-Route, in order. Its BYE
	// follows the dialog's routes toward the caller, with the call's icid.
	from, _ := got.Header.From()
	routes := got.Header.Values("Record-Route")
	routes[len(routes)-1] = "<sip:attacker@192.0.2.1;lr>"

	bye := siptest.NewRequest("BYE", "sip:caller@"+s.scscf.Addr().String(), sip.UDP, u.us.Addr())
	bye.Header.Set("From", "<"+testIMPU+">;tag="+tag)
	bye.Header.Set("To", from.String())
	bye.Header.Set("Call-ID", got.Header.CallID())
	bye.Header.Set("CSeq", "1 BYE")
	bye.Header.Add("Route", strings.Join(routes, ", "))
	u.uc.Send(sip.UDP, s.ps, bye)

	fwd, _ := s.scscf.RecvRequest()
	if fwd.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", fwd.Method)
	}

	if r := uris(fwd.Header.Addresses("Route")); !slices.Equal(r, []string{"sip:mt@" + s.scscf.Addr().String() + ";lr"}) {
		t.Errorf("BYE Route = %q, want the dialog's", r)
	}

	if chargingOf(t, fwd.Header).icid != "AB12" {
		t.Errorf("BYE P-Charging-Vector = %q, want the INVITE's icid", fwd.Header.Get("P-Charging-Vector"))
	}
}

func TestTerminatingStandaloneResponsesAreAsserted(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "MESSAGE", "sip:"+token+"@"+s.pcscf.String()+";lr", nil))

	got, f := u.us.RecvRequest()
	if got.Method != "MESSAGE" || len(got.Header.Values("Record-Route")) != 1 {
		t.Fatalf("UE got:\n%s\nwant the MESSAGE, not record-routed", got)
	}

	u.us.Send(sip.UDP, f.Remote, sip.NewResponse(got, 486, ""))

	res, _ := s.scscf.RecvResponse()
	wantStatus(t, res, 486)

	if v := res.Header.Values("P-Asserted-Identity"); !slices.Equal(v, []string{"<" + testIMPU + ">"}) {
		t.Errorf("P-Asserted-Identity = %q, want the P-Called-Party-ID on any response to a standalone request", v)
	}
}

func TestTerminatingFromOutsideTheCore(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)

	outsider := siptest.NewSocket(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.3"), 0))
	r := s.coreRequest(u, "INVITE", "sip:"+token+"@"+s.pcscf.String()+";lr", nil)
	outsider.Send(sip.UDP, s.pcscf, r)

	u.us.RecvNone(quiet)
}

func TestAssertedIdentities(t *testing.T) {
	associated := []string{"sip:alice@" + homeDomain, "tel:+15551230001", "sip:+15551230002@" + homeDomain + ";user=phone"}

	for _, tc := range []struct {
		name      string
		preferred []string
		want      []string
	}{
		{"none", nil, []string{associated[0]}},
		{"unregistered", []string{"<sip:mallory@" + homeDomain + ">"}, []string{associated[0]}},
		{"display name", []string{`"Alice" <tel:+15551230001>`}, []string{associated[1]}},
		{"user=phone as tel", []string{"<sip:+15551230001@" + homeDomain + ";user=phone>"}, []string{associated[1]}},
		{"tel as user=phone", []string{"<tel:+15551230002>"}, []string{associated[2]}},
		{"one of each", []string{"<tel:+15551230001>", "<sip:alice@" + homeDomain + ">"}, []string{associated[1], associated[0]}},
		{"two SIP", []string{"<sip:alice@" + homeDomain + ">", "<sip:+15551230002@" + homeDomain + ";user=phone>"}, []string{associated[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var preferred []sip.Address

			for _, p := range tc.preferred {
				a, err := sip.ParseAddress(p)
				if err != nil {
					t.Fatal(err)
				}

				preferred = append(preferred, a)
			}

			if got := assertedIdentities(preferred, associated); !slices.Equal(got, tc.want) {
				t.Errorf("assertedIdentities = %q, want %q", got, tc.want)
			}
		})
	}
}

// moCall is an originating call through the P-CSCF, as the UE and the S-CSCF
// see it.
type moCall struct {
	s      *ipsecScene
	u      *ue
	invite *sip.Request // as the UE sent it
	core   *sip.Request // as the S-CSCF got it
	flow   sip.Flow     // the S-CSCF's flow from the P-CSCF
}

func (s *ipsecScene) originatingCall(t *testing.T, u *ue) *moCall {
	t.Helper()

	invite := s.ueInvite(u, nil)
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, first(u.us.RecvResponse()), 100)

	got, f := s.scscf.RecvRequest()

	return &moCall{s: s, u: u, invite: invite, core: got, flow: f}
}

// answer sends a response of the callee with the given To-tag, and returns it
// as the UE got it.
func (c *moCall) answer(t *testing.T, code int, tag string, edit func(*sip.Response)) *sip.Response {
	t.Helper()

	res := c.s.coreResponse(c.core, code, tag)
	if edit != nil {
		edit(res)
	}

	c.s.scscf.Send(c.flow.Transport, c.flow.Remote, res)

	got, _ := c.u.us.RecvResponse()
	wantStatus(t, got, code)

	return got
}

// request builds a request of the UE on the leg a response opened, along its
// route set.
func (c *moCall) request(method, cseq string, res *sip.Response) *sip.Request {
	routes := res.Header.Values("Record-Route")
	slices.Reverse(routes)

	to, _ := res.Header.To()
	from, _ := c.invite.Header.From()

	r := siptest.NewRequest(method, "sip:callee@"+c.s.scscf.Addr().String(), sip.UDP, c.u.us.Addr())
	r.Header.Set("From", from.String())
	r.Header.Set("To", to.String())
	r.Header.Set("Call-ID", c.invite.Header.CallID())
	r.Header.Set("CSeq", cseq+" "+method)
	r.Header.Add("Route", strings.Join(routes, ", "))

	return r
}

func (c *moCall) dialog(t *testing.T) *proxy.Dialog {
	t.Helper()

	rr, err := c.core.Header.RecordRoutes()
	if err != nil || len(rr) == 0 {
		t.Fatalf("Record-Route = %q", c.core.Header.Values("Record-Route"))
	}

	d := c.s.p.cfg.Proxy.Dialog([]sip.URI{rr[0].URI})
	if d == nil {
		t.Fatal("no dialog for the call")
	}

	return d
}

// TS 24.229 §5.2.6.3.9 step 2 holds on early dialogs too: a PRACK follows the
// routes of the leg the reliable 183 opened.
func TestEarlyRoutesAreTheDialogs(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)

	c := s.originatingCall(t, u)
	progress := c.answer(t, 183, sip.NewTag(), func(r *sip.Response) {
		r.Header.Add("Require", "100rel")
		r.Header.Add("RSeq", "1")
	})

	prack := c.request("PRACK", "2", progress)
	prack.Header.Set("Route", strings.Replace(prack.Header.Get("Route"), "sip:mt@"+s.scscf.Addr().String(), "sip:attacker@192.0.2.1", 1))
	prack.Header.Add("RAck", "1 1 INVITE")
	u.uc.Send(sip.UDP, s.ps, prack)

	got, _ := s.scscf.RecvRequest()
	if got.Method != "PRACK" {
		t.Fatalf("S-CSCF got %s, want the PRACK", got.Method)
	}

	if r := uris(got.Header.Addresses("Route")); !slices.Equal(r, []string{"sip:mt@" + s.scscf.Addr().String() + ";lr"}) {
		t.Errorf("PRACK Route = %q, want the early leg's", r)
	}
}

// RFC 3261 §13.2.2.4: the caller ACKs and BYEs a second 2xx a forking proxy
// downstream sent, on a leg the tracker does not follow.
func TestSecondAnswerCanBeEnded(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)

	c := s.originatingCall(t, u)
	c.answer(t, 200, sip.NewTag(), nil)
	second := c.answer(t, 200, sip.NewTag(), nil)

	u.uc.Send(sip.UDP, s.ps, c.request("ACK", "1", second))

	if ack, _ := s.scscf.RecvRequest(); ack.Method != "ACK" {
		t.Fatalf("S-CSCF got %s, want the ACK of the second 2xx", ack.Method)
	}

	u.uc.Send(sip.UDP, s.ps, c.request("BYE", "2", second))

	if bye, _ := s.scscf.RecvRequest(); bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE of the second leg", bye.Method)
	}
}

// Requests from the core toward the UE lose the core's header fields, and
// the UE's responses carry the request's charging vector with the P-CSCF's
// term-ioi (TS 24.229 §5.2.6.4.10).
func TestCoreRequestOnACall(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)

	c := s.originatingCall(t, u)
	tag := sip.NewTag()
	ok := c.answer(t, 200, tag, nil)

	from, _ := c.invite.Header.From()
	to, _ := ok.Header.To()

	routes := slices.Clone(c.core.Header.Values("Record-Route"))

	bye := siptest.NewRequest("BYE", ueContact(u)[1:len(ueContact(u))-1], sip.UDP, s.scscf.Addr())
	bye.Header.Set("From", to.String())
	bye.Header.Set("To", from.String())
	bye.Header.Set("Call-ID", c.invite.Header.CallID())
	bye.Header.Set("CSeq", "7 BYE")
	bye.Header.Add("Route", strings.Join(routes, ", "))
	bye.Header.Add("P-Charging-Vector", "icid-value=CORE1;orig-ioi=other.example")
	bye.Header.Add("P-Asserted-Identity", "<"+callee+">")
	bye.Header.Add("Privacy", "header;id")
	bye.Header.Add("Reason", "SIP;cause=200")
	s.scscf.Send(sip.UDP, s.pcscf, bye)

	got, f := u.us.RecvRequest()
	if got.Method != "BYE" {
		t.Fatalf("UE got %s, want the BYE", got.Method)
	}

	wantAbsent(t, got.Header, "P-Charging-Vector", "P-Asserted-Identity")

	if !got.Header.Has("Reason") || got.Header.Get("Privacy") != "header;id" {
		t.Errorf("Reason = %q, Privacy = %q; want both kept", got.Header.Get("Reason"), got.Header.Get("Privacy"))
	}

	u.us.Send(sip.UDP, f.Remote, sip.NewResponse(got, 200, ""))

	res, _ := s.scscf.RecvResponse()
	wantStatus(t, res, 200)

	if v := res.Header.Get("P-Charging-Vector"); v != "icid-value=CORE1;orig-ioi=other.example;term-ioi="+homeDomain {
		t.Errorf("P-Charging-Vector = %q, want the BYE's icid and orig-ioi with the home term-ioi", v)
	}
}

// A BYE the P-CSCF sends itself (TS 24.229 §5.2.8.1) reaches the UE on its
// security associations, from port_pc to port_us.
func TestReleaseReachesTheUEOnItsSAs(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)
	sets := s.installed()

	c := s.originatingCall(t, u)
	ok := c.answer(t, 200, sip.NewTag(), nil)
	u.uc.Send(sip.UDP, s.ps, c.request("ACK", "1", ok))
	s.scscf.RecvRequest()

	if err := c.dialog(t).Release(proxy.Release{Toward: proxy.Caller}); err != nil {
		t.Fatal(err)
	}

	bye, f := u.us.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("UE got %s, want the BYE", bye.Method)
	}

	if want := netip.AddrPortFrom(loopback, sets[0].Local.PortC); f.Remote != want {
		t.Errorf("BYE from %s, want the protected client port %s", f.Remote, want)
	}

	if via := mustTopVia(t, bye); via.Port != s.ps.Port() {
		t.Errorf("Via = %s, want the protected server port %d", via, s.ps.Port())
	}
}

func TestUnregisteredUEOnItsSAsIsIgnored(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)
	s.p.regs.remove(testIMPI, ueAddr)

	u.uc.Send(sip.UDP, s.ps, s.ueInvite(u, nil))
	u.us.RecvNone(quiet)
	s.scscf.RecvNone(quiet)
}

func TestTerminatingFailureIsNotAsserted(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", "sip:"+token+"@"+s.pcscf.String()+";lr", nil))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()

	busy := sip.NewResponse(got, 486, "")
	_ = busy.Header.SetToTag(sip.NewTag())
	busy.Header.Add("P-Asserted-Identity", "<sip:mallory@"+homeDomain+">")
	busy.Header.Add("Reason", "SIP;cause=486")
	u.us.Send(sip.UDP, f.Remote, busy)

	res, _ := s.scscf.RecvResponse()
	wantStatus(t, res, 486)
	wantAbsent(t, res.Header, "P-Asserted-Identity")

	if !res.Header.Has("Reason") {
		t.Error("Reason from the UE removed, want it kept (TS 24.229 §4.4.7)")
	}
}

func TestKeptTowardUE(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []sip.Field
		want   []string
	}{
		{"identity", []sip.Field{{Name: "P-Asserted-Identity", Value: "<tel:+1>"}}, []string{"P-Asserted-Identity: <tel:+1>"}},
		{
			"privacy none",
			[]sip.Field{{Name: "Privacy", Value: "none"}, {Name: "P-Asserted-Identity", Value: "<tel:+1>"}},
			[]string{"P-Asserted-Identity: <tel:+1>"},
		},
		{"privacy id among others", []sip.Field{{Name: "Privacy", Value: "header; ID"}, {Name: "P-Asserted-Identity", Value: "<tel:+1>"}}, nil},
		{"history", []sip.Field{
			{Name: "History-Info", Value: "<sip:a@x>;index=1, <sip:b@x?Privacy=history>;index=1.1"},
			{Name: "Feature-Caps", Value: "*;+g.3gpp.srvcc-alerting"},
		}, []string{"History-Info: <sip:a@x>;index=1", "Feature-Caps: *;+g.3gpp.srvcc-alerting"}},
		{"history privacy", []sip.Field{{Name: "Privacy", Value: "history"}, {Name: "History-Info", Value: "<sip:a@x>;index=1"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, f := range keptTowardUE(sip.Header(tc.fields)) {
				got = append(got, f.Name+": "+f.Value)
			}

			if !slices.Equal(got, tc.want) {
				t.Errorf("kept %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChargingVectorOnIPv6(t *testing.T) {
	p := &PCSCF{cfg: Config{HomeDomain: homeDomain}}

	cv := p.newChargingVector(netip.MustParseAddr("2001:db8::1"))
	if !strings.Contains(cv.String(), "icid-generated-at=[2001:db8::1];") {
		t.Errorf("P-Charging-Vector = %q, want an IPv6 reference (RFC 7315 §5.6)", cv)
	}
}
