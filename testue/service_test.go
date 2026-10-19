package testue

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// An MCPTT client as a service of the UE (TS 24.379 §7.2.1), with what the tests saw of it.
const icsiMCPTT = "urn:urn-7:3gpp-service.ims.icsi.mcptt"

type mcpttService struct {
	invites  chan *sip.Request
	requests chan *sip.Request
}

func newMCPTTService() (*mcpttService, Service) {
	m := &mcpttService{invites: make(chan *sip.Request, 4), requests: make(chan *sip.Request, 4)}

	icsi := sip.NewFeature("+g.3gpp.icsi-ref", icsiMCPTT)
	tag := sip.NewFeature("+g.3gpp.mcptt")

	return m, Service{
		Name:     "mcptt",
		ICSI:     icsiMCPTT,
		Features: sip.FeatureSet{tag, sip.NewFeature("audio")},
		// TS 24.379 §10.1.1.2.1.1 steps 5, 7.
		AcceptContact: []sip.CallerPreference{
			{Features: sip.FeatureSet{tag}, Require: true, Explicit: true},
			{Features: sip.FeatureSet{icsi}, Require: true, Explicit: true},
		},
		Allow:     []string{"INVITE", "UPDATE", "MESSAGE", "INFO", "REFER"},
		Supported: []string{"tdialog"},
		// An offer with the control channel (TS 24.380 §4.3.3.1).
		Match: func(req *sip.Request) bool {
			body, ok := offerOf(req)
			return ok && strings.Contains(string(body), "udp MCPTT")
		},
		RegisterBodies: func() []sip.BodyPart {
			return []sip.BodyPart{sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo/>"))}
		},
		OnInvite: func(r *ua.Request) ua.Handler {
			m.invites <- r.Message().Clone()

			_ = r.Reject(603)

			return ua.Handler{}
		},
		OnRequest: func(r *ua.Request) {
			m.requests <- r.Message().Clone()

			_ = r.Respond(ua.Reply{Code: 202})
		},
	}
}

// serviceUE is a UE with the MCPTT service, whose P-CSCF is a socket.
func serviceUE(t *testing.T, cfg Config) (*UE, *mcpttService, *siptest.Socket) {
	t.Helper()

	m, s := newMCPTTService()
	cfg.Services = append(cfg.Services, s)

	pcscf := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	return newCallUE(t, "001010000000001", pcscf.Addr(), cfg), m, pcscf
}

// TS 24.379 §7.2.1, TS 24.229 §5.1.1.2.1: one Contact with the feature tags of every service, the ICSIs in one
// g.3gpp.icsi-ref, a tag both share once; the Allow of the UE has the methods of each.
func TestServicesRegisteredContact(t *testing.T) {
	u, _, _ := serviceUE(t, Config{})

	contact, err := sip.ParseAddress(u.contact(5060))
	if err != nil {
		t.Fatal(err)
	}

	fs, err := contact.Features()
	if err != nil {
		t.Fatal(err)
	}

	if icsis := fs.Tokens("g.3gpp.icsi-ref"); !slices.Equal(icsis, []string{ICSIMMTel, icsiMCPTT}) {
		t.Errorf("ICSIs %v", icsis)
	}

	for _, tag := range []string{"sip.audio", "g.3gpp.mcptt", "g.3gpp.smsip"} {
		if !fs.Has(tag) {
			t.Errorf("Contact %s without %s", contact, tag)
		}
	}

	if n := len(slices.DeleteFunc(slices.Clone(contact.Params), func(p sip.Param) bool { return p.Name != "audio" })); n != 1 {
		t.Errorf("Contact %s has audio %d times", contact, n)
	}

	// TS 24.229 §5.1.2A.1.1: the Contact of a dialog has its service's tags only; smsip, of no dialog, none.
	call, _ := u.Contact(MMTelService)
	if cs, _ := call.Features(); cs.Has("g.3gpp.smsip") || cs.Has("g.3gpp.mcptt") || !cs.Has("sip.audio") {
		t.Errorf("MMTel Contact %s", call)
	}

	for _, m := range []string{"INVITE", "PRACK", "INFO", "REFER", "NOTIFY", "OPTIONS"} {
		if !slices.Contains(u.allow, m) {
			t.Errorf("Allow %v without %s", u.allow, m)
		}
	}
}

// A UE without MMTel registers only the feature tags of its services.
func TestMCPTTOnlyContact(t *testing.T) {
	u, _, _ := serviceUE(t, Config{NoMMTel: true, NoSMS: true})

	contact, _ := sip.ParseAddress(u.contact(5060))
	fs, _ := contact.Features()

	if icsis := fs.Tokens("g.3gpp.icsi-ref"); !slices.Equal(icsis, []string{icsiMCPTT}) || fs.Has("g.3gpp.smsip") {
		t.Errorf("Contact %s", contact)
	}

	if u.MMTel() != nil || u.SMS() != nil {
		t.Error("MMTel or SMS on a UE without them")
	}
}

func TestServiceConfig(t *testing.T) {
	_, s := newMCPTTService()

	for name, services := range map[string][]Service{
		"no name":             {{ICSI: icsiMCPTT}},
		"nothing to register": {{Name: "x"}},
		"twice":               {s, s},
		"MMTel ICSI twice":    {{Name: "x", ICSI: ICSIMMTel}},
		"SMS name twice":      {{Name: SMSService, ICSI: icsiMCPTT}},
		"own icsi-ref":        {{Name: "x", ICSI: icsiMCPTT, Features: sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", "x")}}},
		"own instance":        {{Name: "x", ICSI: icsiMCPTT, RegisterFeatures: sip.FeatureSet{sip.NewFeature("+sip.instance")}}},
	} {
		cfg := Config{
			IMSI: imsi, IMEI: imei, Plain: true, PCSCF: netip.AddrPortFrom(loopback, 5060), Local: loopback,
			Services: services,
		}

		if u, err := New(cfg); err == nil {
			_ = u.Close()

			t.Errorf("%s: New succeeded", name)
		}
	}
}

// incomingRequest is an initial request of the network to the UE, with Accept-Contact values.
func incomingRequest(u *UE, from *siptest.Socket, method string, accept ...string) *sip.Request {
	req := sip.NewRequest(method, sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(loopback), Port: u.Unprotected().Port()})
	req.Header.Add("Via", sip.NewVia(sip.UDP, from.Addr()).String())
	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<sip:mcptt-server@example.com>;tag="+sip.NewTag())
	req.Header.Add("To", "<"+u.IMPU()+">")
	req.Header.Add("Call-ID", sip.NewTag()+"@example.com")
	req.Header.Add("CSeq", "1 "+method)

	if method == "INVITE" {
		req.Header.Add("Contact", "<sip:mcptt-server@"+from.Addr().String()+">")
	}

	for _, a := range accept {
		req.Header.Add("Accept-Contact", a)
	}

	return req
}

// The final response of the UE to req.
func finalTo(t *testing.T, s *siptest.Socket, req *sip.Request) *sip.Response {
	t.Helper()

	for {
		res, _ := s.RecvResponse()
		if res.StatusCode >= 200 && res.Header.CallID() == req.Header.CallID() {
			return res
		}
	}
}

// RFC 3841 §7.2.4, TS 24.379 §10.1.1.3.1: the UE gives an initial request to the service its Accept-Contact prefers,
// and the requests that prefer none to MMTel.
func TestServiceRouting(t *testing.T) {
	u, m, net := serviceUE(t, Config{})

	mcptt := []string{
		"*;+g.3gpp.mcptt;require;explicit",
		`*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt";require;explicit`,
	}

	for _, c := range []struct {
		name   string
		method string
		accept []string
		code   int
		mcptt  bool
	}{
		{"MCPTT INVITE", "INVITE", mcptt, 603, true},
		{"MCPTT MESSAGE", "MESSAGE", mcptt, 202, true},
		{"MMTel INVITE", "INVITE", []string{`*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`}, 486, false},
		{"INVITE of no service", "INVITE", nil, 486, false},
		{"SMS", "MESSAGE", []string{"*;+g.3gpp.smsip"}, 200, false},
		{"OPTIONS", "OPTIONS", mcptt, 200, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := incomingRequest(u, net, c.method, c.accept...)
			net.Send(sip.UDP, u.Unprotected(), req)

			if res := finalTo(t, net, req); res.StatusCode != c.code {
				t.Fatalf("got %s, want %d", res.StartLine(), c.code)
			}

			got := len(m.invites) + len(m.requests)
			for len(m.invites) > 0 {
				<-m.invites
			}

			for len(m.requests) > 0 {
				<-m.requests
			}

			if (got == 1) != c.mcptt {
				t.Errorf("MCPTT service took %d requests", got)
			}
		})
	}
}

// RFC 3841 §7.2.4: a request that requires a service the UE does not host goes to none, and gets a 480; one whose
// method no service takes, a 405.
func TestServiceRequired(t *testing.T) {
	u, m, net := serviceUE(t, Config{NoMMTel: true, NoSMS: true})

	req := incomingRequest(u, net, "INVITE", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";require;explicit`)
	net.Send(sip.UDP, u.Unprotected(), req)

	if res := finalTo(t, net, req); res.StatusCode != 480 || len(m.invites) != 0 {
		t.Fatalf("got %s, %d to the service", res.StartLine(), len(m.invites))
	}

	// MCPTT takes MESSAGE, but the SMS requires a service the UE does not host.
	msg := incomingRequest(u, net, "MESSAGE", "*;+g.3gpp.smsip;require;explicit")
	net.Send(sip.UDP, u.Unprotected(), msg)

	if res := finalTo(t, net, msg); res.StatusCode != 480 || len(m.requests) != 0 {
		t.Fatalf("got %s, %d to the service", res.StartLine(), len(m.requests))
	}

	// RFC 3261 §21.4.6: a method no service takes gets a 405 whose Allow leaves it out.
	pub := incomingRequest(u, net, "PUBLISH")
	net.Send(sip.UDP, u.Unprotected(), pub)

	res := finalTo(t, net, pub)
	if allow := res.Header.Get("Allow"); res.StatusCode != 405 || strings.Contains(allow, "PUBLISH") || !strings.Contains(allow, "INVITE") {
		t.Fatalf("got %s, Allow %q", res.StartLine(), allow)
	}
}

// TS 24.229 §5.1.2A.1.1, TS 24.379 §10.1.1.2.1.1: an initial request of a service has the Route to the P-CSCF, the
// identity of the UE, the ICSI in P-Preferred-Service, and the Contact of the service.
func TestServiceOriginate(t *testing.T) {
	u, _, pcscf := serviceUE(t, Config{})

	req := sip.NewRequest("INVITE", sip.URI{Scheme: "sip", User: "group", Host: "mcptt.example.com"})
	if err := u.Originate(req, "mcptt"); err != nil {
		t.Fatal(err)
	}

	req.Header.Add("To", "<sip:group@mcptt.example.com>")

	if _, err := u.Agent().Invite(req, ua.Handler{}); err != nil {
		t.Fatal(err)
	}

	got, _ := pcscf.RecvRequest()

	if route := got.Header.Get("Route"); route != "<"+preloadedRoute(u.unprotectedFlow()).String()+">" {
		t.Errorf("Route %s", route)
	}

	if ac := got.Header.Values("Accept-Contact"); len(ac) != 2 || ac[0] != "*;+g.3gpp.mcptt;require;explicit" ||
		ac[1] != `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt";require;explicit` {
		t.Errorf("Accept-Contact %q", ac)
	}

	if !got.Header.HasOptionTag("Supported", "tdialog") {
		t.Errorf("Supported %q", got.Header.Values("Supported"))
	}

	if pps := got.Header.Get("P-Preferred-Service"); pps != icsiMCPTT {
		t.Errorf("P-Preferred-Service %s", pps)
	}

	if ppi := got.Header.Get("P-Preferred-Identity"); ppi != "<"+u.IMPU()+">" {
		t.Errorf("P-Preferred-Identity %s", ppi)
	}

	contacts, err := got.Header.Contacts()
	if err != nil || len(contacts) != 1 {
		t.Fatalf("Contact %v, %v", contacts, err)
	}

	contact := contacts[0]

	fs, _ := contact.Features()
	if icsis := fs.Tokens("g.3gpp.icsi-ref"); !slices.Equal(icsis, []string{icsiMCPTT}) || !fs.Has("g.3gpp.mcptt") ||
		fs.Has("g.3gpp.smsip") {
		t.Errorf("Contact %s", contact)
	}

	if err := u.Originate(sip.NewRequest("INVITE", req.URI), "unknown"); err == nil {
		t.Error("originated for an unknown service")
	}
}

// TS 24.379 §7.2.1 NOTE 1, TS 24.229 §5.1.1.4.1: a service enabled or disabled re-registers the UE with or without its
// feature tags and REGISTER bodies; a disabled one originates nothing and takes no request.
func TestServiceEnable(t *testing.T) {
	n := newNetwork(t)

	_, s := newMCPTTService()
	s.Disabled = true

	u, _ := n.newUE(Config{Services: []Service{s}})
	n.register(u)

	if err := u.Originate(sip.NewRequest("INVITE", sip.URI{Scheme: "sip", Host: "x"}), "mcptt"); !errors.Is(err, ErrServiceDisabled) {
		t.Fatalf("Originate while disabled: %v", err)
	}

	for _, on := range []bool{true, false} {
		done := start(func(ctx context.Context) error {
			if on {
				return u.Enable(ctx, "mcptt")
			}

			return u.Disable(ctx, "mcptt")
		})

		req, f := n.recv(n.ps)

		contacts, _ := req.Header.Contacts()
		fs, _ := contacts[0].Features()

		mcptt := slices.Contains(fs.Tokens("g.3gpp.icsi-ref"), icsiMCPTT) && fs.Has("g.3gpp.mcptt")
		body := strings.Contains(string(req.Body), "<mcpttinfo/>")

		if mcptt != on || body != on || !slices.Contains(fs.Tokens("g.3gpp.icsi-ref"), ICSIMMTel) {
			t.Fatalf("enabled %v: Contact %s, body %q", on, contacts[0], req.Body)
		}

		n.ok(req, f, n.pc, 3600)

		if err := wait(t, done); err != nil {
			t.Fatal(err)
		}
	}

	// Already disabled: nothing to register.
	if err := u.Disable(t.Context(), "mcptt"); err != nil {
		t.Fatal(err)
	}

	n.ps.RecvNone(100 * time.Millisecond)

	if err := u.Enable(t.Context(), "nothing"); !errors.Is(err, ErrUnknownService) {
		t.Errorf("Enable(nothing) = %v", err)
	}
}

// TS 24.229 §5.1.2A.2: an initial request that prefers no service goes to the one its contents match; RFC 3261
// §8.2.2.3: the option tags of a service are not another's; a predicate that does not parse prefers nothing it meant.
func TestServiceByContents(t *testing.T) {
	u, m, net := serviceUE(t, Config{})

	invite := func(body string, header ...sip.Field) *sip.Request {
		req := incomingRequest(u, net, "INVITE")
		req.SetBody("application/sdp", []byte(body))

		for _, f := range header {
			req.Header.Add(f.Name, f.Value)
		}

		net.Send(sip.UDP, u.Unprotected(), req)

		return req
	}

	channel := "v=0\r\nm=application 20032 udp MCPTT\r\n"

	if res := finalTo(t, net, invite(channel)); res.StatusCode != 603 || len(m.invites) != 1 {
		t.Fatalf("control channel offer: %s", res.StartLine())
	}

	<-m.invites

	if res := finalTo(t, net, invite("v=0\r\nm=audio 4000 RTP/AVP 0\r\n")); res.StatusCode != 486 || len(m.invites) != 0 {
		t.Fatalf("audio offer: %s", res.StartLine())
	}

	sms := incomingRequest(u, net, "MESSAGE")
	sms.SetBody(SMSContentType, []byte{1})
	net.Send(sip.UDP, u.Unprotected(), sms)

	if res := finalTo(t, net, sms); res.StatusCode != 200 || len(m.requests) != 0 {
		t.Fatalf("SMS: %s", res.StartLine())
	}

	res := finalTo(t, net, invite(channel, sip.Field{Name: "Require", Value: "precondition"}))
	if res.StatusCode != 420 || res.Header.Get("Unsupported") != "precondition" || len(m.invites) != 0 {
		t.Fatalf("precondition required of MCPTT: %s, Unsupported %q", res.StartLine(), res.Header.Get("Unsupported"))
	}

	if res := finalTo(t, net, invite(channel, sip.Field{Name: "Accept-Contact", Value: "*;+g.3gpp.mcptt=\"unterminated;require"})); res.StatusCode != 400 {
		t.Fatalf("malformed Accept-Contact: %s", res.StartLine())
	}
}
