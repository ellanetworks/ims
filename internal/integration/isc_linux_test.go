//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/testue"
	"github.com/ellanetworks/ims/ue"
)

const (
	icsiMCPTT = "urn:urn-7:3gpp-service.ims.icsi.mcptt"

	asTimeout = time.Second

	scscfURI = "sip:10.0.0.1:5080;lr"
	icscfURI = "sip:10.0.0.1:5070;lr"
)

var (
	asHost  = netip.MustParseAddr("127.0.0.1")
	silent  = "sip:" + netip.AddrPortFrom(asHost, 5998).String()
	refused = "sip:" + netip.AddrPortFrom(asHost, 5999).String() + ";transport=tcp"
)

func newISCScene(t *testing.T) *scene {
	t.Helper()

	return newSceneWith(t, func(srv *server.Server) {
		srv.ASTimeout = asTimeout
		srv.Config.SIP.TrustedNetworks = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	})
}

type as struct {
	t      *testing.T
	agent  *ua.Agent
	addr   netip.AddrPort
	reqs   chan *sip.Request
	handle func(r *ua.Request)
}

func newAS(t *testing.T, port uint16) *as {
	t.Helper()

	a := &as{t: t, addr: netip.AddrPortFrom(asHost, port), reqs: make(chan *sip.Request, 32)}

	agent, err := ua.New(ua.Config{
		Transaction: transaction.Config{Logger: testLogger(t).With("as", port)},
		Prepare: func(_ context.Context, req *sip.Request) error {
			tr, dest, err := sip.NextHop(req)
			if err != nil {
				return err
			}

			req.Flow = sip.Flow{Transport: tr, Local: a.addr, Remote: dest}
			req.Header.Prepend("Via", sip.NewVia(tr, a.addr).String())

			return nil
		},
		Contact: sip.Address{URI: sip.URI{Scheme: "sip", User: "as", Host: asHost.String(), Port: port}},
		Allow:   []string{"INVITE", "ACK", "BYE", "CANCEL", "OPTIONS", "MESSAGE", "REGISTER"},
		OnInvite: func(_ context.Context, r *ua.Request) ua.Handler {
			a.take(context.Background(), r)
			return ua.Handler{}
		},
		OnRequest: a.take,
		Logger:    testLogger(t).With("as", port),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := agent.Layer().Listen(t.Context(), a.addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = agent.Shutdown(ctx)
	})

	a.agent = agent

	return a
}

func (a *as) take(_ context.Context, r *ua.Request) {
	a.reqs <- r.Message().Clone()

	if a.handle != nil {
		a.handle(r)
		return
	}

	_ = r.Respond(ua.Reply{Code: 200})
}

func (a *as) URI() string {
	return "sip:" + a.addr.String()
}

func (a *as) next(t *testing.T) *sip.Request {
	t.Helper()

	select {
	case r := <-a.reqs:
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("no request reached the AS %s", a.addr)
	}

	return nil
}

func (a *as) none(t *testing.T, d time.Duration) {
	t.Helper()

	select {
	case r := <-a.reqs:
		t.Fatalf("the AS %s got:\n%s", a.addr, r)
	case <-time.After(d):
	}
}

func (a *as) send(t *testing.T, req *sip.Request) *sip.Response {
	t.Helper()

	done := make(chan *sip.Response, 1)

	if err := a.agent.Request(context.Background(), req, func(res *sip.Response, err error) {
		if err != nil {
			t.Logf("AS request: %v", err)
		}

		done <- res
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case res := <-done:
		return res
	case <-time.After(15 * time.Second):
		t.Fatal("no response to the AS's request")
	}

	return nil
}

// TS 24.229 clause 5.4.3.4
// TS 24.229 clause 5.7.5
func (a *as) relay(r *ua.Request) {
	in := r.Message()

	out := sip.NewRequest(in.Method, in.URI)
	out.Header = nil

	for i, v := range in.Header.Values("Route") {
		if i > 0 {
			out.Header.Add("Route", v)
		}
	}

	from, _ := in.Header.From()
	out.Header.Add("From", "<"+from.URI.String()+">")

	for _, name := range []string{"To", "P-Asserted-Identity", "P-Asserted-Service", "Accept-Contact"} {
		for _, v := range in.Header.Values(name) {
			out.Header.Add(name, v)
		}
	}

	out.SetBodies(in.Content())

	if err := a.agent.Request(context.Background(), out, func(res *sip.Response, err error) {
		code := 500
		if res != nil {
			code = res.StatusCode
		}

		_ = r.Respond(ua.Reply{Code: code})
	}); err != nil {
		_ = r.Reject(500)
	}
}

func spt(group int32, s cx.ServicePointTrigger) cx.ServicePointTrigger {
	s.Groups = []int32{group}
	return s
}

func methodIs(m string) cx.ServicePointTrigger { return cx.ServicePointTrigger{Method: &m} }

func caseIs(c cx.SessionCase) cx.ServicePointTrigger { return cx.ServicePointTrigger{SessionCase: &c} }

func headerIs(name, content string) cx.ServicePointTrigger {
	return cx.ServicePointTrigger{SIPHeader: &cx.HeaderMatch{Header: name, Content: &content}}
}

// TS 29.228 annex C
func ifc(priority int32, server string, handling cx.DefaultHandling, spts ...cx.ServicePointTrigger) cx.InitialFilterCriteria {
	tp := &cx.TriggerPoint{ConjunctiveNormalForm: true}

	for i, s := range spts {
		tp.ServicePointTriggers = append(tp.ServicePointTriggers, spt(int32(i), s))
	}

	return cx.InitialFilterCriteria{
		Priority: priority, Trigger: tp, ApplicationServer: cx.ApplicationServer{ServerName: server, DefaultHandling: &handling},
	}
}

func (s *scene) setIFCs(i int, ifcs ...cx.InitialFilterCriteria) {
	s.hss.Update(subscriberAt(i).impi, func(sub *hsstest.Subscriber) { sub.IFCs = ifcs })
}

func mcpttService(got chan<- *sip.Request, code int) ue.Service {
	mcptt := sip.FeatureSet{sip.NewFeature("+g.3gpp.mcptt")}

	return ue.Service{
		Name:          "mcptt",
		ICSI:          icsiMCPTT,
		Features:      mcptt,
		AcceptContact: []sip.CallerPreference{{Features: mcptt, Require: true, Explicit: true}},
		Allow:         []string{"INVITE", "MESSAGE"},
		OnRequest: func(r *ua.Request) {
			got <- r.Message().Clone()

			_ = r.Respond(ua.Reply{Code: code})
		},
		OnInvite: func(r *ua.Request) ua.Handler {
			got <- r.Message().Clone()

			_ = r.Reject(code)

			return ua.Handler{}
		},
	}
}

func (s *scene) mcpttUE(i int, got chan *sip.Request) *testue.UE {
	s.t.Helper()

	if got == nil {
		got = make(chan *sip.Request, 32)
	}

	u := s.newUEAt(i, false, testue.Config{Transport: sip.UDP, Services: []ue.Service{mcpttService(got, 200)}})
	s.register(u)

	return u
}

func mcpttMessage(t *testing.T, u *testue.UE, target string) *sip.Response {
	t.Helper()

	uri, err := sip.ParseURI(target)
	if err != nil {
		t.Fatal(err)
	}

	req := sip.NewRequest("MESSAGE", uri)
	req.Header = nil
	req.Header.Add("To", "<"+target+">")
	req.SetBody("text/plain", []byte("hello"))

	if err := u.Originate(req, "mcptt"); err != nil {
		t.Fatal(err)
	}

	done := make(chan *sip.Response, 1)

	if err := u.Agent().Request(context.Background(), req, func(res *sip.Response, err error) {
		if err != nil {
			t.Logf("MESSAGE: %v", err)
		}

		done <- res
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case res := <-done:
		if res == nil {
			t.Fatal("MESSAGE failed")
		}

		return res
	case <-time.After(15 * time.Second):
		t.Fatal("no response to the MESSAGE")
	}

	return nil
}

func received(t *testing.T, got <-chan *sip.Request) *sip.Request {
	t.Helper()

	select {
	case r := <-got:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the UE's service")
	}

	return nil
}

func asserted(t *testing.T, req *sip.Request) []string {
	t.Helper()

	ids, err := req.Header.Addresses("P-Asserted-Identity")
	if err != nil {
		t.Fatal(err)
	}

	var out []string
	for _, a := range ids {
		out = append(out, a.URI.String())
	}

	return out
}

// TS 24.229 clause 5.4.3.2
func TestMessageRouting(t *testing.T) {
	s := newISCScene(t)
	got := make(chan *sip.Request, 4)

	a := s.mcpttUE(0, nil)
	s.mcpttUE(1, got)

	if res := mcpttMessage(t, a, subscriberAt(1).tel); res.StatusCode != 200 {
		t.Fatalf("MESSAGE got %s, want 200", res.StartLine())
	}

	if m := received(t, got); string(m.Body) != "hello" {
		t.Fatalf("callee got:\n%s\nwant the MESSAGE", m)
	}
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.4
// TS 23.218 clause 5.2.3
func TestOriginatingTrigger(t *testing.T) {
	s := newISCScene(t)
	first, second := newAS(t, 5091), newAS(t, 5092)
	first.handle, second.handle = first.relay, second.relay

	orig := []cx.ServicePointTrigger{
		methodIs("MESSAGE"), caseIs(cx.SessionCaseOriginatingRegistered), headerIs("P-Asserted-Service", `icsi\.mcptt$`),
	}
	s.setIFCs(0,
		ifc(20, second.URI(), cx.DefaultHandlingSessionContinued, orig...),
		ifc(10, first.URI(), cx.DefaultHandlingSessionContinued, orig...),
	)

	got := make(chan *sip.Request, 4)
	a := s.mcpttUE(0, nil)
	s.mcpttUE(1, got)

	if res := mcpttMessage(t, a, subscriberAt(1).tel); res.StatusCode != 200 {
		t.Fatalf("MESSAGE got %s, want 200", res.StartLine())
	}

	for _, srv := range []*as{first, second} {
		r := srv.next(t)

		routes := r.Header.Values("Route")
		if len(routes) != 2 || !strings.Contains(routes[1], "odi-") {
			t.Fatalf("AS %s got Route %v, want its own then the S-CSCF's with an original dialog identifier", srv.addr, routes)
		}

		if v := r.Header.Get("P-Served-User"); v != "<"+subscriberAt(0).msisdn+">;sescase=orig;regstate=reg" {
			t.Errorf("AS %s got P-Served-User %q, want the caller, registered", srv.addr, v)
		}

		if v := r.Header.Get("P-Asserted-Service"); v != icsiMCPTT {
			t.Errorf("AS %s got P-Asserted-Service %q, want the MCPTT ICSI the UE prefers", srv.addr, v)
		}

		if v := r.Header.Get("Feature-Caps"); !strings.Contains(v, "+g.3gpp.registration-token") {
			t.Errorf("AS %s got Feature-Caps %q, want the registration token", srv.addr, v)
		}
	}

	m := received(t, got)
	if m.Header.Has("P-Served-User") || strings.Contains(m.Header.Get("Feature-Caps"), "registration-token") {
		t.Fatalf("callee got:\n%s\nwant no P-Served-User nor registration token", m)
	}

	first.none(t, 200*time.Millisecond)
	second.none(t, 0)
}

// TS 24.229 clause 5.4.3.3
func TestTerminatingTrigger(t *testing.T) {
	s := newISCScene(t)
	srv := newAS(t, 5091)
	srv.handle = func(r *ua.Request) { _ = r.Reject(486) }

	s.setIFCs(1, ifc(0, srv.URI(), cx.DefaultHandlingSessionContinued,
		methodIs("INVITE"), caseIs(cx.SessionCaseTerminatingRegistered)))

	a := s.caller(0, false, testue.Config{Transport: sip.UDP})
	b := s.caller(1, false, testue.Config{Transport: sip.UDP})

	failed(t, s.ctx(), invite(t, a, subscriberAt(1).tel, testue.CallOptions{}), 486)

	r := srv.next(t)
	if v := r.Header.Get("P-Served-User"); v != "<"+subscriberAt(1).tel+">;sescase=term;regstate=reg" {
		t.Fatalf("AS got P-Served-User %q, want the callee, registered", v)
	}

	select {
	case c := <-b.MMTel().Calls():
		t.Fatalf("callee got the call %s", callID(c))
	case <-time.After(200 * time.Millisecond):
	}
}

// TS 24.229 clause 5.4.3.2
// RFC 3261 section 16.7
func TestDefaultHandling(t *testing.T) {
	unavailable := newAS(t, 5093)
	unavailable.handle = func(r *ua.Request) { _ = r.Reject(503) }

	orig := []cx.ServicePointTrigger{methodIs("INVITE"), caseIs(cx.SessionCaseOriginatingRegistered)}

	for _, tt := range []struct {
		name     string
		server   string
		handling cx.DefaultHandling
		code     int
	}{
		{"silent, session continued", silent, cx.DefaultHandlingSessionContinued, 0},
		{"refused, session continued", refused, cx.DefaultHandlingSessionContinued, 0},
		{"503, session continued", unavailable.URI(), cx.DefaultHandlingSessionContinued, 500},
		{"silent, session terminated", silent, cx.DefaultHandlingSessionTerminated, 408},
		{"refused, session terminated", refused, cx.DefaultHandlingSessionTerminated, 500},
		{"503, session terminated", unavailable.URI(), cx.DefaultHandlingSessionTerminated, 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newISCScene(t)
			s.setIFCs(0, ifc(0, tt.server, tt.handling, orig...))

			a := s.caller(0, false, testue.Config{Transport: sip.UDP})
			b := s.caller(1, false, testue.Config{Transport: sip.UDP})

			c := invite(t, a, subscriberAt(1).tel, testue.CallOptions{})

			if tt.code != 0 {
				failed(t, s.ctx(), c, tt.code)
				return
			}

			bc := incoming(t, b, c)
			if err := bc.Reject(486); err != nil {
				t.Fatal(err)
			}

			failed(t, s.ctx(), c, 486)
		})
	}
}

// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.1.7A
// TS 23.218 clause 6.3
func TestThirdPartyRegister(t *testing.T) {
	s := newISCScene(t)
	srv := newAS(t, 5091)

	info := "<mcptt-server/>"
	c := ifc(0, srv.URI(), cx.DefaultHandlingSessionContinued, methodIs("REGISTER"))
	c.ApplicationServer.ServiceInfo = &info
	c.ApplicationServer.IncludeRegisterRequest = true
	c.ApplicationServer.IncludeRegisterResponse = true
	s.setIFCs(0, c)

	u := s.newUE(false, testue.Config{Transport: sip.UDP})
	s.register(u)

	r := srv.next(t)

	to, _ := r.Header.To()
	if r.Method != "REGISTER" || to.URI.String() != subscriberAt(0).msisdn {
		t.Fatalf("AS got:\n%s\nwant a REGISTER to the first identity not barred", r)
	}

	contact, err := sip.ParseAddress(r.Header.Get("Contact"))
	if err != nil {
		t.Fatal(err)
	}

	if v, _ := contact.Params.Get("+g.3gpp.registration-token"); !strings.HasPrefix(v, `"<`) {
		t.Errorf("Contact %s, want a registration token", contact)
	}

	parts, err := r.Content().Parts()
	if err != nil || len(parts) != 3 {
		t.Fatalf("body parts %d, %v, want 3", len(parts), err)
	}

	if parts[0].MediaType() != "application/3gpp-ims+xml" || !strings.Contains(string(parts[0].Body), "<service-info>&lt;mcptt-server/&gt;</service-info>") {
		t.Errorf("first part:\n%s\nwant the service information", parts[0].Body)
	}

	register, err := sip.Parse(parts[1].Body)

	req, isRequest := register.(*sip.Request)
	if err != nil || !isRequest || req.Method != "REGISTER" || parts[1].MediaType() != "message/sip" {
		t.Fatalf("second part:\n%s\nwant the UE's REGISTER (%v)", parts[1].Body, err)
	}

	ok, err := sip.Parse(parts[2].Body)

	res, isResponse := ok.(*sip.Response)
	if err != nil || !isResponse || res.StatusCode != 200 {
		t.Fatalf("third part:\n%s\nwant the 200 (%v)", parts[2].Body, err)
	}

	contacts, err := res.Header.Contacts()
	if err != nil || len(contacts) != 1 {
		t.Fatalf("200 Contacts %v, %v", contacts, err)
	}

	if want, _ := contacts[0].Params.Get("expires"); r.Header.Get("Expires") != want {
		t.Errorf("Expires %s, want %s as in the 200", r.Header.Get("Expires"), want)
	}

	icid, err := sip.ParseParams(req.Header.Get("P-Charging-Vector"))
	if err != nil {
		t.Fatal(err)
	}

	if v, _ := icid.Get("icid-value"); r.Header.Get("P-Charging-Vector") != "icid-value="+v+";orig-ioi="+domain || v == "" {
		t.Errorf("P-Charging-Vector %q, want the icid-value %q of the REGISTER and orig-ioi=%s",
			r.Header.Get("P-Charging-Vector"), v, domain)
	}

	if err := u.Deregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	dereg := srv.next(t)
	if dereg.Header.Get("Expires") != "0" || len(dereg.Body) != 0 || dereg.Header.Has("P-Charging-Vector") {
		t.Fatalf("AS got:\n%s\nwant a deregistration without a body nor P-Charging-Vector", dereg)
	}

	first, _ := r.Header.CSeq()
	next, _ := dereg.Header.CSeq()

	if dereg.Header.CallID() != r.Header.CallID() || next.Seq != first.Seq+1 {
		t.Errorf("deregistration Call-ID %s, CSeq %d, want %s and %d", dereg.Header.CallID(), next.Seq, r.Header.CallID(), first.Seq+1)
	}
}

// TS 23.218 clause 5.2.3
// TS 24.229 clause 5.4.1.5
// TS 29.228 clause 6.1.3
func TestThirdPartyDeregistrationByTheNetwork(t *testing.T) {
	for _, tt := range []struct {
		reason cx.ReasonCode
		dereg  bool
	}{
		{cx.ReasonPermanentTermination, true},
		{cx.ReasonNewServerAssigned, false},
	} {
		t.Run(tt.reason.String(), func(t *testing.T) {
			s := newISCScene(t)
			srv := newAS(t, 5091)

			s.setIFCs(0, ifc(0, srv.URI(), cx.DefaultHandlingSessionContinued,
				methodIs("REGISTER"), headerIs("Contact", `\+g\.3gpp\.mcptt`)))

			s.mcpttUE(0, nil)

			if r := srv.next(t); r.Header.Get("Expires") == "0" {
				t.Fatalf("AS got:\n%s\nwant the registration", r)
			}

			if _, err := s.hss.RTR(s.ctx(), cx.DeregistrationReason{Code: tt.reason}, impi, subscriberAt(0).msisdn); err != nil {
				t.Fatal(err)
			}

			if !tt.dereg {
				srv.none(t, 500*time.Millisecond)
				return
			}

			if r := srv.next(t); r.Method != "REGISTER" || r.Header.Get("Expires") != "0" {
				t.Fatalf("AS got:\n%s\nwant the deregistration", r)
			}
		})
	}
}

// TS 24.229 clause 5.4.0
// RFC 3263
func TestASByDomainName(t *testing.T) {
	s := newISCScene(t)
	srv := newAS(t, 5091)

	s.setIFCs(0,
		ifc(0, "sip:localhost:5091", cx.DefaultHandlingSessionTerminated, methodIs("REGISTER")),
		ifc(1, "sip:localhost:5091", cx.DefaultHandlingSessionTerminated, methodIs("MESSAGE"),
			caseIs(cx.SessionCaseOriginatingRegistered)),
	)

	a := s.mcpttUE(0, nil)

	if r := srv.next(t); r.Method != "REGISTER" || r.URI.String() != "sip:localhost:5091" {
		t.Fatalf("AS got:\n%s\nwant the third-party REGISTER to its domain name", r)
	}

	if res := mcpttMessage(t, a, subscriberAt(1).tel); res.StatusCode != 200 {
		t.Fatalf("MESSAGE got %s, want the AS's 200", res.StartLine())
	}

	if r := srv.next(t); r.Method != "MESSAGE" || !strings.HasPrefix(r.Header.Get("Route"), "<sip:localhost:5091;lr>") {
		t.Fatalf("AS got:\n%s\nwant the MESSAGE routed to its domain name", r)
	}
}

// TS 24.229 clause 5.4.1.7
func TestThirdPartyRegisterFailure(t *testing.T) {
	t.Run("session continued", func(t *testing.T) {
		s := newISCScene(t)
		s.setIFCs(0, ifc(0, silent, cx.DefaultHandlingSessionContinued, methodIs("REGISTER")))

		u := s.newUE(false, testue.Config{T1: fastT1})
		s.subscribed(u)
		noNotify(t, u, 2*asTimeout)
	})

	t.Run("session terminated", func(t *testing.T) {
		s := newISCScene(t)
		s.setIFCs(0, ifc(0, silent, cx.DefaultHandlingSessionTerminated, methodIs("REGISTER")))

		u := s.newUE(false, testue.Config{T1: fastT1})
		s.subscribed(u)

		nextNotify(t, u).want(t, "terminated;reason=rejected", reginfo.Terminated, reginfo.Terminated, reginfo.Rejected)

		eventually(t, "the HSS to forget the registration", func() bool {
			sub, _ := s.hss.Subscriber(impi)
			return sub.State == hsstest.NotRegistered
		})
	})

	t.Run("session terminated, AS outside the trust domain", func(t *testing.T) {
		s := newISCScene(t)
		s.setIFCs(0, ifc(0, "sip:192.0.2.1:5060", cx.DefaultHandlingSessionTerminated, methodIs("REGISTER")))

		s.register(s.newUE(false, testue.Config{T1: fastT1}))

		eventually(t, "the HSS to forget the registration", func() bool {
			sub, _ := s.hss.Subscriber(impi)
			return sub.State == hsstest.NotRegistered
		})
	})
}

// TS 23.228 clause 5.4.12.2
// TS 24.229 clause 5.3.2.1
// TS 24.229 clause 5.4.3.3
func TestPSIRouting(t *testing.T) {
	s := newISCScene(t)
	srv, direct := newAS(t, 5091), newAS(t, 5092)

	psi := "sip:mcptt-orig-pf@" + domain
	s.hss.Add(hsstest.Subscriber{
		IMPI: "mcptt-psi@" + domain,
		IMPUs: []cx.ProfileIdentity{
			{Identity: psi, Type: cx.IdentityDistinctPSI},
			{Identity: "sip:group!.*!@" + domain, Type: cx.IdentityWildcardedPSI},
		},
		IFCs: []cx.InitialFilterCriteria{ifc(0, srv.URI(), cx.DefaultHandlingSessionContinued,
			caseIs(cx.SessionCaseTerminatingUnregistered))},
		UnregisteredServices: true,
	})
	s.hss.Add(hsstest.Subscriber{
		IMPI:                 "mcptt-cf@" + domain,
		IMPUs:                []cx.ProfileIdentity{{Identity: "sip:mcptt-cf@" + domain, Type: cx.IdentityDistinctPSI}},
		UnregisteredServices: true,
		AS:                   direct.URI(),
	})

	a := s.mcpttUE(0, nil)

	for _, tt := range []struct {
		target string
		to     *as
	}{
		{psi, srv},
		{"sip:group42@" + domain, srv},
		{"sip:mcptt-cf@" + domain, direct},
	} {
		t.Run(tt.target, func(t *testing.T) {
			if res := mcpttMessage(t, a, tt.target); res.StatusCode != 200 {
				t.Fatalf("MESSAGE got %s, want 200", res.StartLine())
			}

			r := tt.to.next(t)
			if r.URI.String() != tt.target || string(r.Body) != "hello" {
				t.Fatalf("AS got:\n%s\nwant the MESSAGE to %s", r, tt.target)
			}

			if ids := asserted(t, r); len(ids) == 0 || ids[0] != subscriberAt(0).msisdn {
				t.Errorf("P-Asserted-Identity %v, want the caller", ids)
			}
		})
	}

	s.hss.Add(hsstest.Subscriber{
		IMPI:                 "mcptt-none@" + domain,
		IMPUs:                []cx.ProfileIdentity{{Identity: "sip:mcptt-none@" + domain, Type: cx.IdentityDistinctPSI}},
		UnregisteredServices: true,
	})

	if res := mcpttMessage(t, a, "sip:mcptt-none@"+domain); res.StatusCode != 480 {
		t.Fatalf("MESSAGE to a PSI without iFCs got %s, want 480", res.StartLine())
	}

	s.hss.Add(hsstest.Subscriber{
		IMPI:                 "mcptt-barred@" + domain,
		IMPUs:                []cx.ProfileIdentity{{Identity: "sip:mcptt-barred@" + domain, Type: cx.IdentityDistinctPSI, Barred: true}},
		UnregisteredServices: true,
	})

	for _, target := range []string{"sip:mcptt-unknown@" + domain, "sip:mcptt-barred@" + domain} {
		if res := mcpttMessage(t, a, target); res.StatusCode != 404 {
			t.Fatalf("MESSAGE to %s got %s, want 404", target, res.StartLine())
		}
	}
}

// TS 23.228 clause 5.4.12.2
// TS 24.229 clause 5.4.3.3
func TestInviteToAPSI(t *testing.T) {
	s := newISCScene(t)
	srv := newAS(t, 5091)
	srv.handle = func(r *ua.Request) { _ = r.Reject(486) }

	psi := "sip:mcptt-orig-pf@" + domain
	s.hss.Add(hsstest.Subscriber{
		IMPI:  "mcptt-psi@" + domain,
		IMPUs: []cx.ProfileIdentity{{Identity: psi, Type: cx.IdentityDistinctPSI}},
		IFCs: []cx.InitialFilterCriteria{ifc(0, srv.URI(), cx.DefaultHandlingSessionContinued,
			methodIs("INVITE"), caseIs(cx.SessionCaseTerminatingUnregistered))},
		UnregisteredServices: true,
	})

	a := s.caller(0, false, testue.Config{Transport: sip.UDP})

	failed(t, s.ctx(), invite(t, a, psi, testue.CallOptions{}), 486)

	if r := srv.next(t); r.Method != "INVITE" || r.URI.String() != psi ||
		r.Header.Get("P-Served-User") != "<"+psi+">;sescase=term;regstate=unreg" {
		t.Fatalf("AS got:\n%s\nwant the INVITE to the PSI, served unregistered", r)
	}
}

// TS 24.229 clause 5.4.3.1
// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.7.3
func TestASOriginated(t *testing.T) {
	s := newISCScene(t)
	srv := newAS(t, 5091)
	other := newAS(t, 5092)
	other.handle = other.relay

	s.hss.Add(hsstest.Subscriber{
		IMPI:  "mcptt-psi@" + domain,
		IMPUs: []cx.ProfileIdentity{{Identity: "sip:mcptt-tpf@" + domain, Type: cx.IdentityDistinctPSI}},
		IFCs: []cx.InitialFilterCriteria{ifc(0, other.URI(), cx.DefaultHandlingSessionContinued,
			caseIs(cx.SessionCaseOriginatingUnregistered), methodIs("MESSAGE"))},
		UnregisteredServices: true,
	})

	got := make(chan *sip.Request, 4)

	s.mcpttUE(0, nil)
	s.mcpttUE(1, got)

	message := func(from, target, route string) *sip.Request {
		uri, _ := sip.ParseURI(target)

		req := sip.NewRequest("MESSAGE", uri)
		req.Header = nil
		req.Header.Add("Route", "<"+route+">")
		req.Header.Add("From", "<"+from+">")
		req.Header.Add("To", "<"+target+">")
		req.Header.Add("P-Asserted-Identity", "<"+from+">")
		req.Header.Add("Accept-Contact", "*;+g.3gpp.mcptt;require;explicit")
		req.Header.Add("P-Asserted-Service", icsiMCPTT)
		req.SetBody("text/plain", []byte(from))

		return req
	}

	for _, tt := range []struct {
		name, from, route string
		alias             string
		via               *as
	}{
		{"on behalf of a user", subscriberAt(0).msisdn, scscfURI + ";orig", subscriberAt(0).tel, nil},
		{"on behalf of a PSI, through its iFCs", "sip:mcptt-tpf@" + domain, scscfURI + ";orig", "", other},
		{"to a served user", "sip:mcptt-tpf@" + domain, scscfURI, "", nil},
		{"on behalf of a PSI, through the I-CSCF", "sip:mcptt-tpf@" + domain, icscfURI + ";orig", "", other},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if res := srv.send(t, message(tt.from, subscriberAt(1).msisdn, tt.route)); res == nil || res.StatusCode != 200 {
				t.Fatalf("MESSAGE got %v, want 200", res)
			}

			want := []string{tt.from}
			if tt.alias != "" {
				want = append(want, tt.alias)
			}

			m := received(t, got)
			if ids := asserted(t, m); !slices.Equal(ids, want) || string(m.Body) != tt.from {
				t.Fatalf("callee got:\n%s\nwant the MESSAGE asserting %v", m, want)
			}

			if tt.via != nil {
				if r := tt.via.next(t); r.Header.Get("P-Served-User") != "<"+tt.from+">;sescase=orig;regstate=unreg" {
					t.Fatalf("AS of the iFC got:\n%s\nwant the PSI served, unregistered", r)
				}
			}
		})
	}
}
