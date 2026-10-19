package testue

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

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

	return m, Service{
		ICSI:     icsiMCPTT,
		Features: sip.FeatureSet{sip.NewFeature("+g.3gpp.mcptt"), sip.NewFeature("audio")},
		Allow:    []string{"INVITE", "UPDATE", "MESSAGE", "INFO", "REFER"},
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

	for _, m := range []string{"INVITE", "PRACK", "INFO", "REFER", "NOTIFY", "OPTIONS"} {
		if !slices.Contains(u.allow, m) {
			t.Errorf("Allow %v without %s", u.allow, m)
		}
	}
}

// A UE without MMTel registers only the feature tags of its services.
func TestMCPTTOnlyContact(t *testing.T) {
	u, _, _ := serviceUE(t, Config{NoMMTel: true})

	contact, _ := sip.ParseAddress(u.contact(5060))
	fs, _ := contact.Features()

	if icsis := fs.Tokens("g.3gpp.icsi-ref"); !slices.Equal(icsis, []string{icsiMCPTT}) || fs.Has("g.3gpp.smsip") {
		t.Errorf("Contact %s", contact)
	}

	if u.MMTel() != nil {
		t.Error("MMTel on a UE without it")
	}
}

func TestServiceConfig(t *testing.T) {
	_, s := newMCPTTService()

	for name, services := range map[string][]Service{
		"no ICSI":      {{}},
		"twice":        {s, s},
		"MMTel twice":  {{ICSI: ICSIMMTel}},
		"own icsi-ref": {{ICSI: icsiMCPTT, Features: sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", "x")}}},
		"own instance": {{ICSI: icsiMCPTT, Features: sip.FeatureSet{sip.NewFeature("+sip.instance")}}},
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
	u, m, net := serviceUE(t, Config{NoMMTel: true})

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
	if err := u.Originate(req, icsiMCPTT); err != nil {
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

	if err := u.Originate(sip.NewRequest("INVITE", req.URI), "urn:urn-7:3gpp-service.ims.icsi.unknown"); err == nil {
		t.Error("originated for an unknown service")
	}
}
