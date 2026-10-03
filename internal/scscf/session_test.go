package scscf

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	mmtel      = "urn:urn-7:3gpp-service.ims.icsi.mmtel"
	mmtelParam = `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`
	remoteTel  = "tel:+15559990000"
)

// sessionHarness has a registered UE, whose Path leads to term, the
// terminating P-CSCF; orig stands for the originating P-CSCF.
type sessionHarness struct {
	*harness
	ue   *ue
	orig *siptest.Socket
	term *siptest.Socket

	// serviceRoute is the UE's Service-Route, on the S-CSCF's address.
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

// final is the first non-100 response a socket receives.
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

	// The caller ACKs and hangs up along the route set of the 200.
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
		name string
		req  func() *sip.Request
		code int
	}{
		{"no asserted identity", func() *sip.Request { return sh.originating("INVITE", remoteTel, "") }, 403},
		{"barred identity", func() *sip.Request { return sh.originating("INVITE", remoteTel, "<"+testIMPU+">") }, 403},
		{"identity of another registration", func() *sip.Request { return sh.originating("INVITE", remoteTel, "<"+secondIMPU+">") }, 403},
		{"another contact's Service-Route", func() *sip.Request {
			r := sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")
			r.Header.Set("Route", "<sip:orig-9999@"+sh.scscf.String()+";lr>")

			return r
		}, 403},
		{"MESSAGE", func() *sip.Request { return sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">") }, messageRejectCode},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh.orig.Send(sip.UDP, sh.scscf, tt.req())

			if res := final(t, sh.orig); res.StatusCode != tt.code {
				t.Fatalf("got %q, want %d", res.StartLine(), tt.code)
			}
		})
	}

	sh.icscf.RecvNone(50 * time.Millisecond)
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

func TestTerminatingAcceptContact(t *testing.T) {
	sh := newSessionHarness(t)
	sh.ue.register(registerOptions{contact: "<" + sh.ue.contact + ">;+g.3gpp.icsi-ref=" + mmtelParam})

	invite := sh.terminating("INVITE", testTel)
	invite.Header.Add("Accept-Contact", "*;+g.3gpp.icsi-ref="+mmtelParam+";require;explicit")
	sh.icscf.Send(sip.UDP, sh.scscf, invite)

	if got, _ := sh.term.RecvRequest(); got.URI.String() != sh.ue.contact {
		t.Fatalf("Request-URI = %s, want the MMTel contact %s", got.URI, sh.ue.contact)
	}

	other := sh.terminating("INVITE", testTel)
	other.Header.Add("Accept-Contact", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.other";require;explicit`)
	sh.icscf.Send(sip.UDP, sh.scscf, other)

	if res := final(t, sh.icscf); res.StatusCode != 480 {
		t.Fatalf("got %q, want 480 with no contact for the required ICSI", res.StartLine())
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
	n := Numbering{CountryCode: "44", NationalPrefix: "0"}

	tests := []struct {
		in, want string
	}{
		{"sip:02079460000;phone-context=" + homeDomain + "@" + homeDomain + ";user=phone", "tel:+442079460000"},
		{"sip:020-7946-0000@" + homeDomain + ";user=phone", "tel:+442079460000"},
		{"tel:02079460000;phone-context=" + homeDomain, "tel:+442079460000"},
		{"tel:2079460000;phone-context=+44", "tel:+442079460000"},
		{"tel:02079460000;phone-context=other.example.org", ""},
		{"tel:+15551230001", ""},
		{"sip:+15551230001@" + homeDomain + ";user=phone", ""},
		{"sip:02079460000@" + homeDomain, ""},
		{"sip:02079460000@other.example.org;user=phone", ""},
		{"tel:*21#;phone-context=" + homeDomain, ""},
	}

	for _, tt := range tests {
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
	}

	u, _ := sip.ParseURI("tel:02079460000;phone-context=" + homeDomain)
	if _, ok := (Numbering{}).normalise(u, homeDomain); ok {
		t.Error("normalised without a numbering rule")
	}
}

func TestSelectBinding(t *testing.T) {
	at := func(s int) time.Time { return testEpoch.Add(time.Duration(s) * time.Second) }
	binding := func(uri, params string, registered int) db.Binding {
		return db.Binding{Contact: db.Contact{URI: uri, Params: params}, RegisteredAt: at(registered)}
	}

	voice := binding("sip:voice@ue", ";+g.3gpp.icsi-ref="+mmtelParam, 1)
	plain := binding("sip:plain@ue", ";+g.3gpp.smsip", 2)
	video := binding("sip:video@ue", `;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel,urn%3Aother"`, 0)
	low := binding("sip:low@ue", ";q=0.1;+g.3gpp.icsi-ref="+mmtelParam, 5)

	accept := func(params string) []preference {
		var h sip.Header
		h.Add("Accept-Contact", "*;+g.3gpp.icsi-ref="+mmtelParam+params)

		return preferences(h)
	}

	tests := []struct {
		name     string
		bindings []db.Binding
		prefs    []preference
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, ok := selectBinding(tt.bindings, tt.prefs)
			if got := b.Contact.URI; ok != (tt.want != "") || got != tt.want {
				t.Fatalf("selectBinding = %q, %v, want %q", got, ok, tt.want)
			}
		})
	}
}
