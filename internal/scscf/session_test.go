package scscf

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	mmtel      = "urn:urn-7:3gpp-service.ims.icsi.mmtel"
	mmtelParam = `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`
	remoteTel  = "tel:+15559990000"
)

type sessionHarness struct {
	*harness
	ue   *ue
	orig *siptest.Socket
	term *siptest.Socket

	serviceRoute string
}

func newSessionHarness(t *testing.T) *sessionHarness {
	t.Helper()

	h := newHarness(t)
	sh := &sessionHarness{
		harness: h,
		orig:    siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		term:    siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
	}

	sh.ue = h.newUE()
	sh.ue.path = "<sip:term@" + sh.term.Addr().String() + ";lr>"

	res := sh.ue.register(registerOptions{})

	sr, err := sip.ParseAddress(res.Header.Get("Service-Route"))
	if err != nil {
		t.Fatalf("Service-Route: %v", err)
	}

	sh.serviceRoute = "<sip:" + sr.URI.User + "@" + h.scscf.String() + ";lr>"

	return sh
}

func (sh *sessionHarness) originating(method, target, asserted string) *sip.Request {
	req := siptest.NewRequest(method, target, sip.UDP, sh.orig.Addr())
	req.Header.Prepend("Route", sh.serviceRoute)

	if asserted != "" {
		req.Header.Add("P-Asserted-Identity", asserted)
	}

	return req
}

func (sh *sessionHarness) terminating(method, target string) *sip.Request {
	req := siptest.NewRequest(method, target, sip.UDP, sh.icscf.Addr())
	req.Header.Prepend("Route", "<sip:"+sh.scscf.String()+";lr>")

	return req
}

func final(t *testing.T, s *siptest.Socket) *sip.Response {
	t.Helper()

	for {
		res, _ := s.RecvResponse()
		if res.StatusCode != 100 {
			return res
		}
	}
}

func reply(t *testing.T, s *siptest.Socket, req *sip.Request, from sip.Flow, code int, tag string, fields ...sip.Field) {
	t.Helper()

	res := sip.NewResponse(req, code, "")
	if err := res.Header.SetToTag(tag); err != nil {
		t.Fatal(err)
	}

	if code < 300 {
		res.Header.Set("Contact", "<sip:callee@"+s.Addr().String()+">")

		for _, rr := range req.Header.Values("Record-Route") {
			res.Header.Add("Record-Route", rr)
		}
	}

	for _, f := range fields {
		res.Header.Add(f.Name, f.Value)
	}

	s.Send(sip.UDP, from.Remote, res)
}

func asserted(t *testing.T, m sip.Header) []string {
	t.Helper()

	as, err := m.Addresses("P-Asserted-Identity")
	if err != nil {
		t.Fatal(err)
	}

	var out []string
	for _, a := range as {
		out = append(out, a.String())
	}

	return out
}

func TestOriginatingInvite(t *testing.T) {
	sh := newSessionHarness(t)

	req := sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")
	req.Header.Add("P-Preferred-Service", mmtel)
	req.Header.Add("P-Asserted-Service", "urn:urn-7:3gpp-service.ims.icsi.forged")
	req.Header.Add("P-Access-Network-Info", "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b")
	req.Header.Add("P-Served-User", "<"+testMSISDN+">")
	sh.orig.Send(sip.UDP, sh.scscf, req)

	got, f := sh.icscf.RecvRequest()

	if got.URI.String() != remoteTel {
		t.Errorf("Request-URI = %s, want %s", got.URI, remoteTel)
	}

	routes, _ := got.Header.Routes()
	if len(routes) != 1 || routes[0].URI.HostPort() != sh.icscf.Addr().String() || !routes[0].URI.IsLooseRouter() {
		t.Errorf("Route = %v, want the I-CSCF alone", got.Header.Values("Route"))
	}

	rr, _ := got.Header.RecordRoutes()
	if len(rr) != 1 || rr[0].URI.User != rrOriginating || !rr[0].URI.Params.Has("did") || rr[0].URI.HostPort() != sh.scscf.String() {
		t.Errorf("Record-Route = %v, want one mo entry with a dialog id", got.Header.Values("Record-Route"))
	}

	if want := []string{"<" + testMSISDN + ">", "<" + testTel + ">"}; !slices.Equal(asserted(t, got.Header), want) {
		t.Errorf("P-Asserted-Identity = %v, want %v", asserted(t, got.Header), want)
	}

	if v := got.Header.Values("P-Asserted-Service"); !slices.Equal(v, []string{mmtel}) {
		t.Errorf("P-Asserted-Service = %v, want %s", v, mmtel)
	}

	for _, name := range []string{"P-Preferred-Service", "P-Access-Network-Info", "P-Served-User"} {
		if got.Header.Has(name) {
			t.Errorf("%s forwarded: %q", name, got.Header.Get(name))
		}
	}

	reply(t, sh.icscf, got, f, 180, "callee")

	if res := final(t, sh.orig); res.StatusCode != 180 {
		t.Fatalf("caller got %q, want 180", res.StartLine())
	}

	reply(t, sh.icscf, got, f, 200, "callee")

	ok := final(t, sh.orig)
	if ok.StatusCode != 200 {
		t.Fatalf("caller got %q, want 200", ok.StartLine())
	}

	inDialog := func(method string, seq string) *sip.Request {
		r := siptest.NewRequest(method, "sip:callee@"+sh.icscf.Addr().String(), sip.UDP, sh.orig.Addr())
		r.Header.Set("Call-ID", req.Header.CallID())
		r.Header.Set("From", req.Header.Get("From"))
		r.Header.Set("To", ok.Header.Get("To"))
		r.Header.Set("CSeq", seq+" "+method)
		r.Header.Add("Route", rr[0].String())

		return r
	}

	ack := inDialog("ACK", "1")
	sh.orig.Send(sip.UDP, sh.scscf, ack)

	if got, _ := sh.icscf.RecvRequest(); got.Method != "ACK" || got.Header.Has("Route") {
		t.Fatalf("callee got:\n%s\nwant the ACK without Route", got)
	}

	sh.orig.Send(sip.UDP, sh.scscf, inDialog("BYE", "2"))

	bye, f := sh.icscf.RecvRequest()
	if bye.Method != "BYE" || bye.Header.Has("Route") || bye.Header.Has("Record-Route") {
		t.Fatalf("callee got:\n%s\nwant the BYE without Route or Record-Route", bye)
	}

	reply(t, sh.icscf, bye, f, 200, "callee")

	if res := final(t, sh.orig); res.StatusCode != 200 {
		t.Fatalf("caller got %q to BYE, want 200", res.StartLine())
	}
}

func TestOriginatingTargetRefreshIsRecordRouted(t *testing.T) {
	sh := newSessionHarness(t)

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", remoteTel, "<"+testMSISDN+">"))

	invite, f := sh.icscf.RecvRequest()
	reply(t, sh.icscf, invite, f, 200, "callee")

	ok := final(t, sh.orig)
	rr := invite.Header.Values("Record-Route")

	update := siptest.NewRequest("UPDATE", "sip:callee@"+sh.icscf.Addr().String(), sip.UDP, sh.orig.Addr())
	update.Header.Set("Call-ID", invite.Header.CallID())
	update.Header.Set("From", invite.Header.Get("From"))
	update.Header.Set("To", ok.Header.Get("To"))
	update.Header.Set("CSeq", "2 UPDATE")
	update.Header.Add("Route", rr[0])
	sh.orig.Send(sip.UDP, sh.scscf, update)

	got, _ := sh.icscf.RecvRequest()
	if v := got.Header.Values("Record-Route"); !slices.Equal(v, rr) {
		t.Fatalf("UPDATE Record-Route = %v, want the dialog's %v", v, rr)
	}
}

func TestOriginatingAssertedService(t *testing.T) {
	sh := newSessionHarness(t)

	tests := []struct {
		name      string
		method    string
		preferred []string
		want      []string
	}{
		{"MMTel preferred", "INVITE", []string{mmtel}, []string{mmtel}},
		{"MMTel in a list", "INVITE", []string{"urn:urn-7:3gpp-service.ims.icsi.other, " + strings.ToUpper(mmtel)}, []string{mmtel}},
		{"INVITE without a preference", "INVITE", nil, []string{mmtel}},
		{"unsupported service", "INVITE", []string{"urn:urn-7:3gpp-service.ims.icsi.other"}, nil},
		{"OPTIONS without a preference", "OPTIONS", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := sh.originating(tt.method, remoteTel, "<"+testMSISDN+">")
			for _, v := range tt.preferred {
				req.Header.Add("P-Preferred-Service", v)
			}

			req.Header.Add("P-Asserted-Service", "urn:urn-7:3gpp-service.ims.icsi.forged")
			sh.orig.Send(sip.UDP, sh.scscf, req)

			got, _ := sh.icscf.RecvRequest()
			if v := got.Header.Values("P-Asserted-Service"); !slices.Equal(v, tt.want) || got.Header.Has("P-Preferred-Service") {
				t.Fatalf("P-Asserted-Service = %v, P-Preferred-Service = %v, want %v and none",
					v, got.Header.Values("P-Preferred-Service"), tt.want)
			}
		})
	}
}

func TestOriginatingAssertedIdentity(t *testing.T) {
	sh := newSessionHarness(t)

	tests := []struct {
		name     string
		asserted string
		want     []string
	}{
		{"tel URI", `"Alice" <` + testTel + ">", []string{`"Alice" <` + testTel + ">", `"Alice" <sip:+15551230001@` + homeDomain + ";user=phone>"}},
		{"SIP URI without a tel alias", "<" + testAlias + ">", []string{"<" + testAlias + ">"}},
		{"second identity not an alias", "<" + testAlias + ">, <" + testTel + ">", []string{"<" + testAlias + ">"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh.orig.Send(sip.UDP, sh.scscf, sh.originating("OPTIONS", remoteTel, tt.asserted))

			got, f := sh.icscf.RecvRequest()
			if !slices.Equal(asserted(t, got.Header), tt.want) {
				t.Errorf("P-Asserted-Identity = %v, want %v", asserted(t, got.Header), tt.want)
			}

			if got.Header.Has("Record-Route") {
				t.Errorf("OPTIONS record-routed: %v", got.Header.Values("Record-Route"))
			}

			reply(t, sh.icscf, got, f, 200, "x")
			final(t, sh.orig)
		})
	}
}

func TestOriginatingRefused(t *testing.T) {
	sh := newSessionHarness(t)

	tests := []struct {
		name   string
		req    func() *sip.Request
		code   int
		reason string
	}{
		{"no asserted identity", func() *sip.Request { return sh.originating("INVITE", remoteTel, "") }, 403, "Not Registered"},
		{"barred identity", func() *sip.Request { return sh.originating("INVITE", remoteTel, "<"+testIMPU+">") }, 403, "Barred"},
		{"identity of another registration", func() *sip.Request {
			return sh.originating("INVITE", remoteTel, "<"+secondIMPU+">")
		}, 403, "Not Registered"},
		{"another contact's Service-Route", func() *sip.Request {
			r := sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")
			r.Header.Set("Route", "<sip:orig-9999@"+sh.scscf.String()+";lr>")

			return r
		}, 403, "Not Registered"},
		{"malformed Service-Route", func() *sip.Request {
			r := sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")
			r.Header.Set("Route", "<sip:orig-x@"+sh.scscf.String()+";lr>")

			return r
		}, 403, "Forbidden"},
		{"MESSAGE", func() *sip.Request {
			return sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">")
		}, messageRejectCode, messageRejectReason},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh.orig.Send(sip.UDP, sh.scscf, tt.req())

			if res := final(t, sh.orig); res.StatusCode != tt.code || res.Reason != tt.reason {
				t.Fatalf("got %q, want %d %s", res.StartLine(), tt.code, tt.reason)
			}
		})
	}

	sh.icscf.RecvNone(50 * time.Millisecond)
}

func TestOriginatingDropsPreloadedRoutes(t *testing.T) {
	sh := newSessionHarness(t)

	req := sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")
	req.Header.Add("Route", "<sip:elsewhere.example.org;lr>")
	sh.orig.Send(sip.UDP, sh.scscf, req)

	got, _ := sh.icscf.RecvRequest()
	if routes := got.Header.Values("Route"); len(routes) != 1 || !strings.Contains(routes[0], sh.icscf.Addr().String()) {
		t.Fatalf("Route = %v, want the I-CSCF alone", routes)
	}
}

func TestOriginatingOverTCP(t *testing.T) {
	sh := newSessionHarness(t)

	req := siptest.NewRequest("INVITE", remoteTel, sip.TCP, sh.orig.Addr())
	req.Header.Prepend("Route", sh.serviceRoute)
	req.Header.Add("P-Asserted-Identity", "<"+testMSISDN+">")
	sh.orig.Send(sip.TCP, sh.scscf, req)

	got, f := sh.icscf.RecvRequest()
	if f.Transport != sip.TCP {
		t.Errorf("INVITE to the I-CSCF over %s, want TCP", f.Transport)
	}

	if route := got.Header.Get("Route"); !strings.Contains(route, "transport=tcp") {
		t.Errorf("Route = %s, want transport=tcp", route)
	}

	if rr := got.Header.Values("Record-Route"); len(rr) != 1 {
		t.Errorf("Record-Route = %v, want one entry for one transport", rr)
	}
}

func TestOriginatingHomeLocalNumber(t *testing.T) {
	sh := newSessionHarness(t)
	sh.numbering = Numbering{CountryCode: "1", NationalPrefix: "1"}
	sh.restart()

	req := sh.originating("INVITE", "sip:15559990000;phone-context="+homeDomain+"@"+homeDomain+";user=phone", "<"+testMSISDN+">")
	sh.orig.Send(sip.UDP, sh.scscf, req)

	got, _ := sh.icscf.RecvRequest()
	if got.URI.String() != remoteTel {
		t.Fatalf("Request-URI = %s, want %s", got.URI, remoteTel)
	}

	if to, _ := got.Header.To(); !strings.Contains(to.URI.User, "phone-context") {
		t.Errorf("To = %s, want it as dialled", to)
	}
}

func TestOriginatingLocalNumberNotTranslated(t *testing.T) {
	tests := []struct {
		name      string
		numbering Numbering
		uri       string
	}{
		{"no numbering rule", Numbering{}, "tel:15559990000;phone-context=" + homeDomain},
		{"no numbering rule, SIP", Numbering{}, "sip:15559990000;phone-context=" + homeDomain + "@" + homeDomain + ";user=phone"},
		{"short code", Numbering{CountryCode: "1", NationalPrefix: "1"}, "tel:999;phone-context=" + homeDomain},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := newSessionHarness(t)
			sh.numbering = tt.numbering
			sh.restart()

			sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", tt.uri, "<"+testMSISDN+">"))

			if res := final(t, sh.orig); res.StatusCode != 404 {
				t.Fatalf("got %q, want 404", res.StartLine())
			}
		})
	}
}

func TestTerminatingInvite(t *testing.T) {
	sh := newSessionHarness(t)

	req := sh.terminating("INVITE", testTel)
	req.Header.Add("P-Served-User", "<"+testTel+">")
	req.Header.Add("P-User-Database", "<aaa://hss."+homeDomain+">")
	req.Header.Add("P-Called-Party-ID", "<sip:forged@"+homeDomain+">")
	sh.icscf.Send(sip.UDP, sh.scscf, req)

	got, f := sh.term.RecvRequest()

	if got.URI.String() != sh.ue.contact {
		t.Errorf("Request-URI = %s, want the contact %s", got.URI, sh.ue.contact)
	}

	if v := got.Header.Values("Route"); !slices.Equal(v, []string{sh.ue.path}) {
		t.Errorf("Route = %v, want the Path %s", v, sh.ue.path)
	}

	if v := got.Header.Values("P-Called-Party-ID"); !slices.Equal(v, []string{"<" + testTel + ">"}) {
		t.Errorf("P-Called-Party-ID = %v, want <%s>", v, testTel)
	}

	rr, _ := got.Header.RecordRoutes()
	if len(rr) != 1 || rr[0].URI.User != rrTerminating || !rr[0].URI.Params.Has("did") {
		t.Errorf("Record-Route = %v, want one mt entry with a dialog id", got.Header.Values("Record-Route"))
	}

	for _, name := range []string{"P-Served-User", "P-User-Database"} {
		if got.Header.Has(name) {
			t.Errorf("%s forwarded", name)
		}
	}

	reply(t, sh.term, got, f, 180, "ue", sip.Field{Name: "P-Asserted-Identity", Value: "<" + testTel + ">"})

	res := final(t, sh.icscf)
	if want := []string{"<" + testTel + ">", "<sip:+15551230001@" + homeDomain + ";user=phone>"}; res.StatusCode != 180 ||
		!slices.Equal(asserted(t, res.Header), want) {
		t.Fatalf("got %q with P-Asserted-Identity %v, want 180 with %v", res.StartLine(), asserted(t, res.Header), want)
	}

	reply(t, sh.term, got, f, 200, "ue", sip.Field{Name: "P-Asserted-Identity", Value: "<" + testMSISDN + ">"})

	res = final(t, sh.icscf)
	if want := []string{"<" + testMSISDN + ">", "<" + testTel + ">"}; res.StatusCode != 200 || !slices.Equal(asserted(t, res.Header), want) {
		t.Fatalf("got %q with P-Asserted-Identity %v, want 200 with %v", res.StartLine(), asserted(t, res.Header), want)
	}
}

func TestTerminatingIdentityForms(t *testing.T) {
	sh := newSessionHarness(t)

	for _, target := range []string{testTel, testMSISDN, testAlias, "sip:+15551230001@" + homeDomain + ";user=phone"} {
		t.Run(target, func(t *testing.T) {
			sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("OPTIONS", target))

			got, f := sh.term.RecvRequest()
			if got.URI.String() != sh.ue.contact || got.Header.Has("Record-Route") {
				t.Fatalf("P-CSCF got:\n%s\nwant the OPTIONS to the contact, not record-routed", got)
			}

			reply(t, sh.term, got, f, 200, "ue")
			final(t, sh.icscf)
		})
	}
}

func TestTerminatingRefused(t *testing.T) {
	sh := newSessionHarness(t)

	tests := []struct {
		name   string
		target string
		code   int
	}{
		{"barred", testIMPU, 404},
		{"not registered here", "sip:nobody@" + homeDomain, 480},
		{"unregistered identity", secondIMPU, 480},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", tt.target))

			if res := final(t, sh.icscf); res.StatusCode != tt.code {
				t.Fatalf("got %q, want %d", res.StartLine(), tt.code)
			}
		})
	}

	sh.term.RecvNone(50 * time.Millisecond)
}

func TestTerminatingAfterExpiry(t *testing.T) {
	sh := newSessionHarness(t)
	sh.clock.Advance(2 * time.Hour)

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	if res := final(t, sh.icscf); res.StatusCode != 480 {
		t.Fatalf("got %q, want 480", res.StartLine())
	}
}

func (sh *sessionHarness) addBinding(t *testing.T, uri, params string) {
	t.Helper()

	regs, err := sh.db.ListRegistrationsByIMPI(t.Context(), testIMPI)
	if err != nil {
		t.Fatal(err)
	}

	i := slices.IndexFunc(regs, func(r db.Registration) bool { return len(r.Bindings) > 0 })
	reg := regs[i]

	b := reg.Bindings[0]
	b.Contact = db.Contact{URI: uri, Params: params, Path: b.Contact.Path}
	b.CallID = sip.NewTag()
	b.RegisteredAt = b.RegisteredAt.Add(time.Minute)
	reg.Bindings = append(reg.Bindings, b)

	if _, err := sh.db.SaveRegistration(t.Context(), reg); err != nil {
		t.Fatal(err)
	}
}

// requestsTo reads what the S-CSCF sends toward the UEs until it has n requests of the method, and returns their
// sorted Request-URIs.
func (sh *sessionHarness) requestsTo(t *testing.T, method string, n int) []string {
	t.Helper()

	var uris []string

	for len(uris) < n {
		if got, _ := sh.term.RecvRequest(); got.Method == method {
			uris = append(uris, got.URI.String())
		}
	}

	slices.Sort(uris)

	return uris
}

// RFC 3841 §7.2.4: a preference without "require" orders the contacts it does not match after the others, in the
// same group; "require" with "explicit" drops them.
func TestTerminatingAcceptContact(t *testing.T) {
	sh := newSessionHarness(t)
	sh.ue.register(registerOptions{contact: "<" + sh.ue.contact + ">;+g.3gpp.icsi-ref=" + mmtelParam})
	sh.addBinding(t, "sip:sms@127.0.0.1:5999", ";+g.3gpp.smsip")

	both := []string{sh.ue.contact, "sip:sms@127.0.0.1:5999"}
	slices.Sort(both)

	invite := sh.terminating("INVITE", testTel)
	invite.Header.Add("Accept-Contact", "*;+g.3gpp.icsi-ref="+mmtelParam)
	sh.icscf.Send(sip.UDP, sh.scscf, invite)

	if got := sh.requestsTo(t, "INVITE", 2); !slices.Equal(got, both) {
		t.Fatalf("INVITE to %v, want both contacts", got)
	}

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", testTel))

	if got := sh.requestsTo(t, "MESSAGE", 2); !slices.Equal(got, both) {
		t.Fatalf("MESSAGE to %v, want both contacts", got)
	}

	required := sh.terminating("INVITE", testTel)
	required.Header.Add("Accept-Contact", "*;+g.3gpp.icsi-ref="+mmtelParam+";require;explicit")
	sh.icscf.Send(sip.UDP, sh.scscf, required)

	for {
		got, _ := sh.term.RecvRequest()
		if got.Method == "INVITE" && got.Header.CallID() == required.Header.CallID() {
			if got.URI.String() != sh.ue.contact {
				t.Fatalf("INVITE to %s, want only the MMTel contact", got.URI)
			}

			break
		}
	}

	sh.term.RecvNone(50 * time.Millisecond)

	other := sh.terminating("INVITE", testTel)
	other.Header.Add("Accept-Contact", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.other";require;explicit`)
	sh.icscf.Send(sip.UDP, sh.scscf, other)

	for {
		if res, _ := sh.icscf.RecvResponse(); res.Header.CallID() == other.Header.CallID() && res.StatusCode >= 200 {
			if res.StatusCode != 480 {
				t.Fatalf("got %q, want 480 with no contact for the required ICSI", res.StartLine())
			}

			break
		}
	}
}

func TestTerminatingBarredInAnotherRegistration(t *testing.T) {
	sh := newSessionHarness(t)

	other := db.Registration{
		IMPI: "other@" + homeDomain, IMPU: testTel,
		Identities: []db.PublicIdentity{{URI: testTel, Key: identityKeyOf(testTel), Barred: true}},
	}
	if _, err := sh.db.SaveRegistration(t.Context(), other); err != nil {
		t.Fatal(err)
	}

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	if res := final(t, sh.icscf); res.StatusCode != 404 {
		t.Fatalf("got %q, want 404", res.StartLine())
	}
}

func TestTerminatingUnreachablePath(t *testing.T) {
	for _, path := range []string{"<sip:term@pcscf." + homeDomain + ";lr>", "<sip:term@[::1]:5060;lr>"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()
			u.path = path
			u.register(registerOptions{})

			req := siptest.NewRequest("INVITE", testTel, sip.UDP, h.icscf.Addr())
			req.Header.Prepend("Route", "<sip:"+h.scscf.String()+";lr>")
			h.icscf.Send(sip.UDP, h.scscf, req)

			if res := final(t, h.icscf); res.StatusCode != 480 {
				t.Fatalf("got %q, want 480", res.StartLine())
			}
		})
	}
}

func TestTerminatingWithRoute(t *testing.T) {
	sh := newSessionHarness(t)

	req := sh.terminating("INVITE", testTel)
	req.Header.Add("Route", "<sip:as@"+sh.term.Addr().String()+";lr>")
	sh.icscf.Send(sip.UDP, sh.scscf, req)

	got, f := sh.term.RecvRequest()
	if got.URI.String() != testTel || got.Header.Has("P-Called-Party-ID") {
		t.Fatalf("got:\n%s\nwant the request forwarded on its Route, unchanged", got)
	}

	reply(t, sh.term, got, f, 180, "as", sip.Field{Name: "P-Asserted-Identity", Value: "<" + testTel + ">"})

	if res := final(t, sh.icscf); len(asserted(t, res.Header)) != 2 {
		t.Fatalf("P-Asserted-Identity = %v, want the alias added", asserted(t, res.Header))
	}
}

func TestTerminatingCalledPartyIDWithoutPortability(t *testing.T) {
	sh := newSessionHarness(t)

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("OPTIONS", testTel+";npdi;rn=+15550000000"))

	got, _ := sh.term.RecvRequest()
	if v := got.Header.Get("P-Called-Party-ID"); v != "<"+testTel+">" {
		t.Fatalf("P-Called-Party-ID = %s, want <%s>", v, testTel)
	}
}

func TestTerminatingDatabaseFailure(t *testing.T) {
	sh := newSessionHarness(t)
	_ = sh.db.Close()

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	if res := final(t, sh.icscf); res.StatusCode != 500 || !res.Header.Has("Retry-After") {
		t.Fatalf("got %q, want 500 with Retry-After", res.StartLine())
	}
}

func TestTerminatingCancel(t *testing.T) {
	sh := newSessionHarness(t)

	invite := sh.terminating("INVITE", testTel)
	sh.icscf.Send(sip.UDP, sh.scscf, invite)

	got, f := sh.term.RecvRequest()
	reply(t, sh.term, got, f, 180, "ue")
	final(t, sh.icscf)

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		t.Fatal(err)
	}

	sh.icscf.Send(sip.UDP, sh.scscf, cancel)

	got2, f2 := sh.term.RecvRequest()
	if got2.Method != "CANCEL" {
		t.Fatalf("P-CSCF got %s, want CANCEL", got2.Method)
	}

	sh.term.Send(sip.UDP, f2.Remote, sip.NewResponse(got2, 200, ""))
	reply(t, sh.term, got, f, 487, "ue")

	for {
		res, _ := sh.icscf.RecvResponse()
		if cseq, _ := res.Header.CSeq(); cseq.Method == "INVITE" {
			if res.StatusCode != 487 {
				t.Fatalf("got %q to INVITE, want 487", res.StartLine())
			}

			return
		}
	}
}

func (sh *sessionHarness) call(t *testing.T) (*sip.Request, *sip.Response) {
	t.Helper()

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", remoteTel, "<"+testMSISDN+">"))

	invite, f := sh.icscf.RecvRequest()
	reply(t, sh.icscf, invite, f, 200, "callee")

	return invite, final(t, sh.orig)
}

func (sh *sessionHarness) inDialog(method string, invite *sip.Request, ok *sip.Response, route string) *sip.Request {
	r := siptest.NewRequest(method, "sip:callee@"+sh.icscf.Addr().String(), sip.UDP, sh.orig.Addr())
	r.Header.Set("Call-ID", invite.Header.CallID())
	r.Header.Set("From", invite.Header.Get("From"))
	r.Header.Set("To", ok.Header.Get("To"))
	r.Header.Set("CSeq", "2 "+method)
	r.Header.Add("Route", route)

	return r
}

func TestInDialogAfterRestart(t *testing.T) {
	sh := newSessionHarness(t)

	invite, ok := sh.call(t)
	route := strings.Replace(invite.Header.Values("Record-Route")[0], "did=", "did=gone", 1)

	sh.orig.Send(sip.UDP, sh.scscf, sh.inDialog("BYE", invite, ok, route))

	if got, _ := sh.icscf.RecvRequest(); got.Method != "BYE" {
		t.Fatalf("callee got %s, want the BYE of a dialog the S-CSCF does not know (Decision 2)", got.Method)
	}
}

func TestInDialogReleased(t *testing.T) {
	sh := newSessionHarness(t)

	invite, ok := sh.call(t)
	rr, _ := invite.Header.RecordRoutes()

	d := sh.sipProxy.Dialog([]sip.URI{rr[0].URI})
	if d == nil {
		t.Fatal("no dialog for the Record-Route")
	}

	d.Discard()

	sh.orig.Send(sip.UDP, sh.scscf, sh.inDialog("BYE", invite, ok, rr[0].String()))

	if res := final(t, sh.orig); res.StatusCode != 481 {
		t.Fatalf("got %q, want 481", res.StartLine())
	}

	sh.icscf.RecvNone(50 * time.Millisecond)
}

func TestInDialogSubscriptionRefreshIsRecordRouted(t *testing.T) {
	sh := newSessionHarness(t)

	invite, ok := sh.call(t)
	rr := invite.Header.Values("Record-Route")

	sh.orig.Send(sip.UDP, sh.scscf, sh.inDialog("NOTIFY", invite, ok, rr[0]))

	if got, _ := sh.icscf.RecvRequest(); !slices.Equal(got.Header.Values("Record-Route"), rr) {
		t.Fatalf("NOTIFY Record-Route = %v, want %v", got.Header.Values("Record-Route"), rr)
	}
}

func TestAckWithoutRouteIsDropped(t *testing.T) {
	sh := newSessionHarness(t)

	invite, ok := sh.call(t)
	ack := sh.inDialog("ACK", invite, ok, "")
	ack.Header.Del("Route")
	ack.Header.Set("CSeq", "1 ACK")
	sh.orig.Send(sip.UDP, sh.scscf, ack)

	sh.icscf.RecvNone(50 * time.Millisecond)
}

func TestInDialogNotThroughTheSCSCF(t *testing.T) {
	sh := newSessionHarness(t)

	bye := siptest.NewRequest("BYE", "sip:callee@"+sh.term.Addr().String(), sip.UDP, sh.icscf.Addr())
	bye.Header.Set("To", "<"+testTel+">;tag=x")
	sh.icscf.Send(sip.UDP, sh.scscf, bye)

	if res := final(t, sh.icscf); res.StatusCode != 403 {
		t.Fatalf("got %q, want 403", res.StartLine())
	}
}

func TestNormalise(t *testing.T) {
	n := Numbering{CountryCode: "44", NationalPrefix: "0", InternationalPrefix: "00"}

	tests := []struct {
		name, in, want string
	}{
		{"home-local SIP", "sip:02079460000;phone-context=" + homeDomain + "@" + homeDomain + ";user=phone", "tel:+442079460000"},
		{"SIP without phone-context", "sip:020-7946-0000@" + homeDomain + ";user=phone", "tel:+442079460000"},
		{"home-local tel", "tel:02079460000;phone-context=" + homeDomain, "tel:+442079460000"},
		{"home domain with a trailing dot", "tel:02079460000;phone-context=" + strings.ToUpper(homeDomain) + ".", "tel:+442079460000"},
		{"national number", "tel:2079460000;phone-context=+44", "tel:+442079460000"},
		{"geo-local", "tel:02079460000;phone-context=geo-local." + homeDomain, "tel:+442079460000"},
		{"geo-local EPS", "tel:02079460000;phone-context=001.01.eps." + homeDomain, "tel:+442079460000"},
		{"geo-local 5GS", "tel:02079460000;phone-context=001.01.5gs." + homeDomain, "tel:+442079460000"},
		{"own number as context", "sip:07700900123;phone-context=07700900999@07700900999;user=phone", "tel:+447700900123"},
		{"international prefix", "tel:00447700900123;phone-context=" + homeDomain, "tel:+447700900123"},
		{"international prefix abroad", "tel:0033123456789;phone-context=" + homeDomain, "tel:+33123456789"},
		{"tel parameters kept", "tel:02079460000;isub=1234;phone-context=" + homeDomain, "tel:+442079460000;isub=1234"},
		{"SIP user parameters kept", "sip:02079460000;isub=1234;phone-context=" + homeDomain + "@" + homeDomain + ";user=phone", "tel:+442079460000;isub=1234"},
		{"short code", "tel:999;phone-context=" + homeDomain, ""},
		{"national prefix alone", "tel:0;phone-context=" + homeDomain, ""},
		{"international prefix alone", "tel:00;phone-context=" + homeDomain, ""},
		{"longer than E.164", "tel:00123456789012345678;phone-context=" + homeDomain, ""},
		{"country code 0", "tel:000123456789;phone-context=" + homeDomain, ""},
		{"area context", "tel:79460000;phone-context=+4420", ""},
		{"foreign country", "tel:2079460000;phone-context=+1", ""},
		{"foreign domain", "tel:02079460000;phone-context=other.example.org", ""},
		{"global tel", "tel:+15551230001", ""},
		{"global SIP", "sip:+15551230001@" + homeDomain + ";user=phone", ""},
		{"SIP without user=phone", "sip:02079460000@" + homeDomain, ""},
		{"SIP on a foreign host", "sip:02079460000@other.example.org;user=phone", ""},
		{"service code", "tel:*21#;phone-context=" + homeDomain, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := sip.ParseURI(tt.in)
			if err != nil {
				t.Fatal(err)
			}

			got, ok := n.normalise(u, homeDomain)
			switch {
			case tt.want == "" && ok:
				t.Errorf("normalise(%s) = %s, want it unchanged", tt.in, got)
			case tt.want != "" && (!ok || got.String() != tt.want):
				t.Errorf("normalise(%s) = %s, %v, want %s", tt.in, got, ok, tt.want)
			}
		})
	}

	u, _ := sip.ParseURI("tel:02079460000;phone-context=" + homeDomain)
	if _, ok := (Numbering{}).normalise(u, homeDomain); ok {
		t.Error("normalised without a numbering rule")
	}
}

func TestLocalNumber(t *testing.T) {
	tests := []struct {
		uri  string
		want bool
	}{
		{"tel:02079460000;phone-context=" + homeDomain, true},
		{"tel:02079460000;phone-context=other.example.org", true},
		{"tel:*21#;phone-context=" + homeDomain, true},
		{"sip:02079460000;phone-context=" + homeDomain + "@" + homeDomain + ";user=phone", true},
		{"sip:02079460000@" + strings.ToUpper(homeDomain) + ".;user=phone", true},
		{"tel:+442079460000", false},
		{"sip:+442079460000@" + homeDomain + ";user=phone", false},
		{"sip:02079460000@other.example.org;user=phone", false},
		{"sip:02079460000@" + homeDomain, false},
		{"sip:alice@" + homeDomain, false},
	}

	for _, tt := range tests {
		u, err := sip.ParseURI(tt.uri)
		if err != nil {
			t.Fatal(err)
		}

		if got := localNumber(u, homeDomain); got != tt.want {
			t.Errorf("localNumber(%s) = %v, want %v", tt.uri, got, tt.want)
		}
	}
}

func TestAssertedAlias(t *testing.T) {
	userData := func(ids ...cx.ProfileIdentity) []byte {
		b, err := cx.MarshalUserData(cx.IMSSubscription{PrivateIdentity: testIMPI, ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: ids}}})
		if err != nil {
			t.Fatal(err)
		}

		return b
	}

	telOnly := db.Registration{UserData: userData(cx.ProfileIdentity{Identity: testAlias}, cx.ProfileIdentity{Identity: testTel, DisplayName: "Alice"})}
	barredTel := db.Registration{UserData: userData(cx.ProfileIdentity{Identity: testMSISDN}, cx.ProfileIdentity{Identity: testTel, Barred: true})}
	grouped := db.Registration{UserData: userData(
		cx.ProfileIdentity{Identity: testAlias, AliasIdentityGroupID: "a"},
		cx.ProfileIdentity{Identity: "tel:+15550009999", AliasIdentityGroupID: "b"},
		cx.ProfileIdentity{Identity: testTel, AliasIdentityGroupID: "a"},
	)}

	tests := []struct {
		name     string
		reg      db.Registration
		asserted string
		want     string
	}{
		{"user=phone with only the tel URI in the profile", telOnly, "<" + testMSISDN + ">", `"Alice" <` + testTel + ">"},
		{"user=phone with separators", telOnly, "<sip:+1-555-123-0001@" + homeDomain + ";user=phone>", `"Alice" <` + testTel + ">"},
		{"user=phone whose tel URI is barred", barredTel, "<" + testMSISDN + ">", ""},
		{"SIP URI aliased by the HSS", telOnly, "<" + testAlias + ">", `"Alice" <` + testTel + ">"},
		{"alias group", grouped, "<" + testAlias + ">", "<" + testTel + ">"},
		{"tel URI", telOnly, `"A" <` + testTel + ">", `"A" <sip:+15551230001@` + homeDomain + ";user=phone>"},
		{"local tel URI", telOnly, "<tel:5551230001;phone-context=" + homeDomain + ">", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := sip.ParseAddress(tt.asserted)
			if err != nil {
				t.Fatal(err)
			}

			got, ok := assertedAlias(a, tt.reg, homeDomain)
			if ok != (tt.want != "") || ok && got.String() != tt.want {
				t.Fatalf("assertedAlias(%s) = %s, %v, want %q", tt.asserted, got, ok, tt.want)
			}
		})
	}
}

func TestIdentityKeys(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{testTel, []string{testTel, testMSISDN}},
		{testMSISDN, []string{testMSISDN, testTel}},
		{"tel:0498765432100;phone-context=" + homeDomain, []string{"tel:0498765432100;phone-context=" + homeDomain, "tel:0498765432100"}},
		{testAlias, []string{testAlias}},
		{"sip:%2B15551230001@" + homeDomain + ";user=phone", []string{"sip:%2B15551230001@" + homeDomain + ";user=phone", testTel, testMSISDN}},
	}

	for _, tt := range tests {
		u, err := sip.ParseURI(tt.in)
		if err != nil {
			t.Fatal(err)
		}

		if got := identityKeys(u, homeDomain); !slices.Equal(got, tt.want) {
			t.Errorf("identityKeys(%s) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// The first contact of the target set is the one a request that does not fork goes to.
func TestTargetSetOrder(t *testing.T) {
	at := func(s int) time.Time { return testEpoch.Add(time.Duration(s) * time.Second) }
	binding := func(uri, params string, registered int) db.Binding {
		return db.Binding{Contact: db.Contact{URI: uri, Params: params}, RegisteredAt: at(registered), ExpiresAt: at(3600)}
	}

	voice := binding("sip:voice@ue", ";+g.3gpp.icsi-ref="+mmtelParam, 1)
	plain := binding("sip:plain@ue", ";+g.3gpp.smsip", 2)
	video := binding("sip:video@ue", `;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel,urn%3Aother"`, 0)
	low := binding("sip:low@ue", ";q=0.1;+g.3gpp.icsi-ref="+mmtelParam, 5)

	immune := binding("sip:immune@ue", "", 0)
	lowPlain := binding("sip:lowplain@ue", ";q=0.5;+g.3gpp.smsip", 9)
	instance := binding("sip:instance@ue", `;+sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref=`+mmtelParam, 0)
	videoCapable := binding("sip:videocapable@ue", ";video;+g.3gpp.icsi-ref="+mmtelParam, 0)
	audioOnly := binding("sip:audioonly@ue", `;video="FALSE";+g.3gpp.icsi-ref=`+mmtelParam, 3)

	predicates := func(values ...string) sip.Header {
		var h sip.Header
		for _, v := range values {
			h.Add("Accept-Contact", v)
		}

		return h
	}
	accept := func(params string) sip.Header { return predicates("*;+g.3gpp.icsi-ref=" + mmtelParam + params) }

	tests := []struct {
		name     string
		bindings []db.Binding
		prefs    sip.Header
		want     string
	}{
		{"newest without preferences", []db.Binding{voice, plain}, nil, "sip:plain@ue"},
		{"matching ICSI first", []db.Binding{voice, plain}, accept(""), "sip:voice@ue"},
		{"ICSI in a list", []db.Binding{video, plain}, accept(""), "sip:video@ue"},
		{"q before age", []db.Binding{voice, low}, accept(""), "sip:voice@ue"},
		{"newest among equals", []db.Binding{video, voice}, accept(""), "sip:voice@ue"},
		{"require keeps contacts without the feature", []db.Binding{plain}, accept(";require"), "sip:plain@ue"},
		{"require and explicit drop them", []db.Binding{plain}, accept(";require;explicit"), ""},
		{"require drops another ICSI", []db.Binding{binding("sip:other@ue", `;+g.3gpp.icsi-ref="urn%3Aother"`, 9)}, accept(";require"), ""},
		{"none", nil, nil, ""},
		{"callee q before caller preference", []db.Binding{low, lowPlain}, accept(""), "sip:lowplain@ue"},
		{"immune contact kept", []db.Binding{immune}, accept(";require;explicit"), "sip:immune@ue"},
		{"immune contact has a full preference", []db.Binding{immune, plain}, accept(""), "sip:immune@ue"},
		{"predicate without ICSI", []db.Binding{voice, instance}, predicates(`*;+sip.instance="<urn:gsma:imei:35622410-483840-0>";require`), "sip:instance@ue"},
		{"instance compared with case", []db.Binding{instance}, predicates(`*;+sip.instance="<URN:GSMA:IMEI:35622410-483840-0>";require;explicit`), ""},
		{"every term of a predicate", []db.Binding{videoCapable, audioOnly}, accept(";video;require"), "sip:videocapable@ue"},
		{"negated value", []db.Binding{videoCapable, audioOnly}, accept(`;video="!TRUE"`), "sip:audioonly@ue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, _ := targetSet([]db.Registration{{Bindings: tt.bindings}}, testEpoch, tt.prefs)

			got := ""
			if len(set) > 0 {
				got = set[0][0].binding.Contact.URI
			}

			if got != tt.want {
				t.Fatalf("first contact %q, want %q", got, tt.want)
			}
		})
	}
}

func contactURIs(set [][]target) [][]string {
	var out [][]string

	for _, g := range set {
		var uris []string
		for _, t := range g {
			uris = append(uris, t.binding.Contact.URI)
		}

		out = append(out, uris)
	}

	return out
}

// TS 24.229 §5.4.3.3 steps 8 to 10, RFC 3841, RFC 5626 §7
func TestTargetSetGroups(t *testing.T) {
	at := func(s int) time.Time { return testEpoch.Add(time.Duration(s) * time.Second) }
	binding := func(uri, imei, params string, registered int) db.Binding {
		c := db.Contact{URI: uri, Params: params}
		if imei != "" {
			c.Instance = "urn:gsma:imei:" + imei
			c.Params = `;+sip.instance="<` + c.Instance + `>"` + params
		}

		return db.Binding{Contact: c, RegisteredAt: at(registered), ExpiresAt: at(3600)}
	}

	phone := binding("sip:phone@ue", "1", ";audio", 1)
	tablet := binding("sip:tablet@ue", "2", ";audio;video", 2)
	desk := binding("sip:desk@ue", "", ";q=0.5;audio", 3)
	soft := binding("sip:soft@ue", "", ";q=0.5;audio", 4)
	moved := binding("sip:moved@ue", "1", ";audio", 5)
	expired := binding("sip:expired@ue", "", ";audio", 0)
	expired.ExpiresAt = at(-1)

	header := func(fields ...string) sip.Header {
		var h sip.Header
		for i := 0; i+1 < len(fields); i += 2 {
			h.Add(fields[i], fields[i+1])
		}

		return h
	}

	tests := []struct {
		name     string
		bindings []db.Binding
		h        sip.Header
		want     string
	}{
		{"equal q-values ring together", []db.Binding{phone, tablet}, nil, "[[sip:tablet@ue sip:phone@ue]]"},
		{"lower q-values after", []db.Binding{desk, phone, soft}, nil, "[[sip:phone@ue] [sip:soft@ue sip:desk@ue]]"},
		{"expired contacts left out", []db.Binding{phone, expired}, nil, "[[sip:phone@ue]]"},
		{"one contact per instance", []db.Binding{phone, moved, tablet}, nil, "[[sip:moved@ue sip:tablet@ue]]"},
		{"no-fork", []db.Binding{phone, tablet, desk}, header("Request-Disposition", "no-fork"), "[[sip:tablet@ue]]"},
		{"sequential", []db.Binding{phone, tablet, desk}, header("Request-Disposition", "proxy, sequential"), "[[sip:tablet@ue] [sip:phone@ue] [sip:desk@ue]]"},
		{"parallel", []db.Binding{phone, tablet, desk}, header("Request-Disposition", "parallel"), "[[sip:tablet@ue sip:phone@ue sip:desk@ue]]"},
		// RFC 3841 §7.2.4: the caller's preferences order the contacts of a q-value, without splitting them.
		{"partial matches ring together", []db.Binding{phone, tablet}, header("Accept-Contact", "*;audio;video"), "[[sip:tablet@ue sip:phone@ue]]"},
		{"best match first", []db.Binding{tablet, binding("sip:newer@ue", "3", ";audio", 9)}, header("Accept-Contact", "*;audio;video"), "[[sip:tablet@ue sip:newer@ue]]"},
		{"predicate with a term it cannot evaluate", []db.Binding{phone, tablet}, header("Reject-Contact", `*;video;+sip.foo="#>=2"`), "[[sip:tablet@ue sip:phone@ue]]"},
		{"compact form", []db.Binding{phone, tablet}, header("d", "no-fork"), "[[sip:tablet@ue]]"},
		{"Reject-Contact", []db.Binding{phone, tablet}, header("Reject-Contact", "*;video"), "[[sip:phone@ue]]"},
		{"tags without Accept-Contact", []db.Binding{phone, binding("sip:bare@ue", "", "", 0)}, nil, "[[sip:phone@ue sip:bare@ue]]"},
		{"Reject-Contact on a missing tag", []db.Binding{phone, tablet}, header("Reject-Contact", "*;video;+sip.foo"), "[[sip:tablet@ue sip:phone@ue]]"},
		{"required capability", []db.Binding{phone, tablet}, header("Accept-Contact", "*;video;require;explicit"), "[[sip:tablet@ue]]"},
		{"none", []db.Binding{expired}, nil, "[]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, step := targetSet([]db.Registration{{Bindings: tt.bindings}}, testEpoch, tt.h)
			// Without directives, the groups are the S-CSCF's own, and step from one to the next.
			wantStep := len(set) > 0 && !tt.h.Has("Request-Disposition") && !tt.h.Has("d")

			if got := fmt.Sprint(contactURIs(set)); got != tt.want || step != wantStep {
				t.Fatalf("target set %s with step %v, want %s", got, step, tt.want)
			}
		})
	}
}

// RFC 5626 §7: the flows of an outbound UA instance are tried in turn on a 430.
func TestTargetSetFlows(t *testing.T) {
	at := func(s int) time.Time { return testEpoch.Add(time.Duration(s) * time.Second) }
	// flow is a binding of instance 1: a flow with its reg-id, or a contact address without.
	flow := func(uri string, regID int64, registered int) db.Binding {
		return db.Binding{
			Contact:      db.Contact{URI: uri, Instance: "urn:gsma:imei:1", RegID: regID},
			RegisteredAt: at(registered), ExpiresAt: at(3600),
		}
	}

	for _, tc := range []struct {
		name     string
		bindings []db.Binding
		want     string
	}{
		// a is the most recent flow, b another flow, and d no flow at all.
		{"outbound", []db.Binding{flow("sip:a@ue", 1, 3), flow("sip:b@ue", 2, 2), flow("sip:d@ue", 0, 0)}, "sip:a@ue [sip:b@ue]"},
		// Only an outbound flow has other flows to replace it on a 430.
		{"most recent without outbound", []db.Binding{flow("sip:a@ue", 1, 3), flow("sip:d@ue", 0, 4)}, "sip:d@ue []"},
		// Flows may share a URI (RFC 5626 §6).
		{"flows on one URI", []db.Binding{flow("sip:a@ue", 1, 3), flow("sip:a@ue", 2, 2)}, "sip:a@ue [sip:a@ue]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := targetSet([]db.Registration{{Bindings: tc.bindings}}, testEpoch, nil)
			if len(set) != 1 || len(set[0]) != 1 {
				t.Fatalf("target set %v", contactURIs(set))
			}

			var others []string
			for _, o := range set[0][0].others {
				others = append(others, o.binding.Contact.URI)
			}

			if got := set[0][0].binding.Contact.URI + " " + fmt.Sprint(others); got != tc.want {
				t.Errorf("contact and other flows %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionsAfterClose(t *testing.T) {
	sh := newSessionHarness(t)
	sh.reg.Close()

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", remoteTel, "<"+testMSISDN+">"))

	if res := final(t, sh.orig); res.StatusCode != 500 || !res.Header.Has("Retry-After") {
		t.Fatalf("got %q, want 500 with Retry-After once the registrar is closed", res.StartLine())
	}

	sh.icscf.RecvNone(50 * time.Millisecond)
}

func TestCalledPartyID(t *testing.T) {
	tests := []struct{ in, want string }{
		{testTel + ";npdi;rn=+15550000000", testTel},
		{"tel:+15551230001;RN=+15550000000;isub=12", "tel:+15551230001;isub=12"},
		{"sip:+15551230001;npdi;rn=+15550000000@" + homeDomain + ";user=phone", testMSISDN},
		{testAlias, testAlias},
	}

	for _, tt := range tests {
		u, err := sip.ParseURI(tt.in)
		if err != nil {
			t.Fatal(err)
		}

		if got := calledPartyID(u).String(); got != tt.want {
			t.Errorf("calledPartyID(%s) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestInDialogDoubleRecordRoute(t *testing.T) {
	sh := newSessionHarness(t)

	invite := siptest.NewRequest("INVITE", testTel, sip.TCP, sh.icscf.Addr())
	invite.Header.Prepend("Route", "<sip:"+sh.scscf.String()+";transport=tcp;lr>")
	invite.Header.Set("Contact", "<sip:caller@"+sh.icscf.Addr().String()+";transport=tcp>")
	sh.icscf.Send(sip.TCP, sh.scscf, invite)

	got, f := sh.term.RecvRequest()

	rr, _ := got.Header.RecordRoutes()
	if len(rr) != 2 || !rr[0].URI.Params.Has("r2") || rr[0].URI.Params.Has("transport") || !rr[1].URI.Params.Has("transport") {
		t.Fatalf("Record-Route = %v, want the UDP entry above the TCP one", got.Header.Values("Record-Route"))
	}

	reply(t, sh.term, got, f, 200, "ue")

	ok := final(t, sh.icscf)

	update := siptest.NewRequest("UPDATE", "sip:caller@"+sh.icscf.Addr().String()+";transport=tcp", sip.UDP, sh.term.Addr())
	update.Header.Set("Call-ID", invite.Header.CallID())
	update.Header.Set("From", ok.Header.Get("To"))
	update.Header.Set("To", invite.Header.Get("From"))
	update.Header.Set("CSeq", "1 UPDATE")

	for _, a := range rr {
		update.Header.Add("Route", a.String())
	}

	sh.term.Send(sip.UDP, sh.scscf, update)

	fwd, ff := sh.icscf.RecvRequest()
	if ff.Transport != sip.TCP || fwd.Method != "UPDATE" || fwd.Header.Has("Route") {
		t.Fatalf("caller got over %s:\n%s\nwant the UPDATE over TCP without Route", ff.Transport, fwd)
	}

	want := []string{rr[1].String(), rr[0].String()}
	if v := fwd.Header.Values("Record-Route"); !slices.Equal(v, want) {
		t.Fatalf("UPDATE Record-Route = %v, want %v", v, want)
	}
}

// receive takes the next n requests the S-CSCF sends toward the UEs, by Request-URI.
func (sh *sessionHarness) receive(t *testing.T, n int) map[string]received {
	t.Helper()

	out := make(map[string]received)

	for len(out) < n {
		req, f := sh.term.RecvRequest()
		out[req.URI.String()] = received{req: req, f: f}
	}

	return out
}

type received struct {
	req *sip.Request
	f   sip.Flow
}

// TS 23.228 §4.2.7.2, TS 24.229 §5.4.3.3 step 10
func TestTerminatingForks(t *testing.T) {
	sh := newSessionHarness(t)
	sh.ue.register(registerOptions{contact: "<" + sh.ue.contact + ">;+g.3gpp.icsi-ref=" + mmtelParam})
	sh.addBinding(t, "sip:tablet@127.0.0.1:5999", ";+g.3gpp.icsi-ref="+mmtelParam)

	invite := sh.terminating("INVITE", testTel)
	invite.Header.Add("Accept-Contact", "*;+g.3gpp.icsi-ref="+mmtelParam)
	sh.icscf.Send(sip.UDP, sh.scscf, invite)

	got := sh.receive(t, 2)
	phone, tablet := got[sh.ue.contact], got["sip:tablet@127.0.0.1:5999"]

	if phone.req == nil || tablet.req == nil {
		t.Fatalf("INVITEs to %v", slices.Collect(maps.Keys(got)))
	}

	for _, r := range []received{phone, tablet} {
		if v := r.req.Header.Values("P-Called-Party-ID"); !slices.Equal(v, []string{"<" + testTel + ">"}) {
			t.Errorf("P-Called-Party-ID = %v", v)
		}

		if v := r.req.Header.Values("Route"); !slices.Equal(v, []string{sh.ue.path}) {
			t.Errorf("Route = %v", v)
		}

		if n, _ := r.req.Header.MaxBreadth(); n != 30 {
			t.Errorf("Max-Breadth %d", n)
		}
	}

	reply(t, sh.term, phone.req, phone.f, 180, "phone")
	reply(t, sh.term, tablet.req, tablet.f, 180, "tablet")
	reply(t, sh.term, tablet.req, tablet.f, 200, "tablet")

	for {
		if res := final(t, sh.icscf); res.StatusCode == 200 {
			break
		}
	}

	cancel, _ := sh.term.RecvRequest()
	if cancel.Method != "CANCEL" || cancel.URI.String() != sh.ue.contact ||
		cancel.Header.Get("Reason") != `SIP;cause=200;text="Call completed elsewhere"` {
		t.Fatalf("got %s with Reason %q, want a CANCEL to the phone", cancel.StartLine(), cancel.Header.Get("Reason"))
	}
}

// Contacts with a lower q-value get the request once the others rang for a while.
func TestTerminatingGroups(t *testing.T) {
	sh := newSessionHarness(t)
	sh.addBinding(t, "sip:desk@127.0.0.1:5999", ";q=0.5")

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	phone := sh.receive(t, 1)[sh.ue.contact]
	if phone.req == nil {
		t.Fatal("the contact with the highest q-value did not get the INVITE first")
	}

	reply(t, sh.term, phone.req, phone.f, 180, "phone")

	if res := final(t, sh.icscf); res.StatusCode != 180 {
		t.Fatalf("got %s", res.StartLine())
	}

	// RFC 3261 §16.8: the phone rang, so it is cancelled, and the next group starts on its final response.
	sh.sipClock.Advance(DefaultGroupNoAnswer)

	cancel, cf := sh.term.RecvRequest()
	if cancel.Method != "CANCEL" || cancel.URI.String() != sh.ue.contact {
		t.Fatalf("got %s, want a CANCEL to the phone", cancel.StartLine())
	}

	sh.term.RecvNone(50 * time.Millisecond)

	sh.term.Send(sip.UDP, cf.Remote, sip.NewResponse(cancel, 200, ""))
	reply(t, sh.term, phone.req, phone.f, 487, "phone")

	for {
		req, _ := sh.term.RecvRequest()
		if req.Method == "ACK" {
			continue
		}

		if req.Method != "INVITE" || req.URI.String() != "sip:desk@127.0.0.1:5999" {
			t.Fatalf("got %s, want the INVITE to the desk phone", req.StartLine())
		}

		break
	}
}

// RFC 3841 §9.1
func TestTerminatingNoFork(t *testing.T) {
	sh := newSessionHarness(t)
	sh.addBinding(t, "sip:tablet@127.0.0.1:5999", "")

	invite := sh.terminating("INVITE", testTel)
	invite.Header.Add("Request-Disposition", "no-fork")
	sh.icscf.Send(sip.UDP, sh.scscf, invite)

	if got, _ := sh.term.RecvRequest(); got.URI.String() != "sip:tablet@127.0.0.1:5999" {
		t.Fatalf("INVITE to %s, want the newest contact", got.URI)
	}

	sh.term.RecvNone(50 * time.Millisecond)
}

// RFC 3841 §7.2.4: a contact with every feature of an explicit predicate scores exactly 1, whatever the number of
// terms.
func TestCallerPreferenceFullMatch(t *testing.T) {
	for n := 1; n <= 12; n++ {
		var params, accept []string

		for i := range n {
			params = append(params, "+tag"+strconv.Itoa(i))
			accept = append(accept, "+tag"+strconv.Itoa(i))
		}

		var h sip.Header
		h.Add("Accept-Contact", "*;"+strings.Join(accept, ";")+";require;explicit")

		ps, _ := sip.ParseParams(";" + strings.Join(params, ";"))

		if qa, ok := callerPreference(contactFeatures(ps), preferences(h, "Accept-Contact")); !ok || qa != 1 {
			t.Errorf("%d terms: Qa %v, kept %v; want 1", n, qa, ok)
		}
	}
}

// A group none of whose contacts can be reached is no group: the last group left rings without a step.
func TestTerminatingLastUsableGroup(t *testing.T) {
	sh := newSessionHarness(t)
	sh.addBinding(t, "not a URI", ";q=0.5")

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	phone, f := sh.term.RecvRequest()
	if phone.URI.String() != sh.ue.contact {
		t.Fatalf("INVITE to %s, want the phone", phone.URI)
	}

	reply(t, sh.term, phone, f, 180, "phone")

	if res := final(t, sh.icscf); res.StatusCode != 180 {
		t.Fatalf("got %s", res.StartLine())
	}

	sh.sipClock.Advance(DefaultGroupNoAnswer)
	sh.term.RecvNone(50 * time.Millisecond)
}

// TS 24.229 §5.4.3.2 step 1: a UE may assert an identity of another of its registration sets than the one
// whose Service-Route it uses; the served user's own binding counts.
func TestOriginatingFromAnotherSet(t *testing.T) {
	sh := newSessionHarness(t)

	sh.ue.impu = secondIMPU
	wantStatus(t, sh.ue.send(registerOptions{auth: sh.ue.protected(testNonce(), testVector.XRES)}), 200)

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", remoteTel, "<"+secondIMPU+">"))

	if got, _ := sh.icscf.RecvRequest(); got.Method != "INVITE" {
		t.Fatalf("I-CSCF got %s", got.StartLine())
	}
}
