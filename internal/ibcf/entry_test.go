package ibcf

import (
	"context"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

func TestEntryCall(t *testing.T) {
	s := newScene(t, false)

	s.dial(s.carrier, "offer-1",
		f("P-Asserted-Identity", "<sip:"+carol+"@sip.carrier.example;user=phone>"),
		f("P-Charging-Vector", "icid-value=foreign"),
		f("P-Charging-Function-Addresses", "ccf=192.0.2.10"),
		f("Feature-Caps", "*;+g.3gpp.srvcc"),
		f("P-Access-Network-Info", "3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=0010100010019B01"),
		f("Privacy", "none"),
	)

	in := s.ims.next("invite").req
	m := in.Message()

	if want := "sip:" + alice + "@" + home + ";user=phone"; m.URI.String() != want {
		t.Errorf("Request-URI %s, want %s", m.URI, want)
	}

	if m.Flow.Remote != internalAddr {
		t.Errorf("sent from %s, want %s", m.Flow.Remote, internalAddr)
	}

	for _, name := range []string{"P-Asserted-Identity", "P-Charging-Function-Addresses", "Feature-Caps", "P-Access-Network-Info", "Route"} {
		if m.Header.Has(name) {
			t.Errorf("%s: %q kept", name, values(m, name))
		}
	}

	icid := m.Header.ICID()
	if icid == "" || icid == "foreign" {
		t.Errorf("P-Charging-Vector %q, want a new ICID", values(m, "P-Charging-Vector"))
	}

	if got := values(m, "Accept-Contact"); got != acceptMMTel {
		t.Errorf("Accept-Contact %q, want %q", got, acceptMMTel)
	}

	if values(m, "Privacy") != "none" || !hasSDP(m, "offer-1") {
		t.Errorf("INVITE to the I-CSCF: %s", m)
	}

	from, _ := m.Header.From()
	if from.URI.String() != "sip:"+carol+"@sip.carrier.example;user=phone" || from.Display != `"Carol"` {
		t.Errorf("From %s", from)
	}

	_ = in.Progress(ua.Reply{Code: 180, Header: sip.Header{{Name: "P-Asserted-Identity", Value: "<sip:alice@" + home + ">"}}})

	ring := s.carrier.next("progress").res
	if values(ring, "P-Asserted-Identity") != "<sip:alice@"+home+">" || ring.Header.Has("P-Charging-Vector") {
		t.Errorf("ringing towards the trunk: %s", ring)
	}

	_ = in.Accept(sdpOf("answer-1")...)

	e := s.carrier.next("established")
	if !hasSDP(e.msg, "answer-1") || values(e.msg, "Contact") != "<sip:"+externalAddr.String()+">" {
		t.Errorf("trunk 200: %s", e.msg)
	}

	s.carrier.next("answer")
	ims := s.ims.next("established")

	_ = ims.s.Bye(context.Background(), ua.Change{}, nil)

	s.carrier.next("bye")
	s.carrier.next("ended")
	s.ims.next("ended")
	s.settle(s.ims, s.carrier)
}

func TestEntryTrusted(t *testing.T) {
	s := newScene(t, true)

	s.dial(s.carrier, "offer-1",
		f("P-Asserted-Identity", "<tel:"+carol+">"),
		f("P-Asserted-Identity", `"Carol" <sip:`+carol+`@sip.carrier.example;user=phone>`),
		f("P-Asserted-Identity", "<sip:other@sip.carrier.example>"),
		f("P-Charging-Vector", "icid-value=foreign;orig-ioi=carrier"),
	)

	m := s.ims.next("invite").req.Message()

	want := `"Carol" <sip:` + carol + `@sip.carrier.example;user=phone>, <tel:` + carol + ">"
	if got := values(m, "P-Asserted-Identity"); got != want {
		t.Errorf("P-Asserted-Identity %q, want %q", got, want)
	}

	if m.Header.ICID() != "foreign" {
		t.Errorf("P-Charging-Vector %q", values(m, "P-Charging-Vector"))
	}
}

func TestEntryRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		from   func(s *scene) *party
		header []sip.Field
		setup  func(s *scene)
		code   int
	}{
		{name: "unknown source", from: func(s *scene) *party { return newParty(s.t, s.net, "stranger", strangerAddr) }},
		{name: "disabled trunk", setup: func(s *scene) {
			tr := s.trunk
			tr.Enabled = false
			s.setTrunk(tr)
		}, code: 503},
		{name: "orig", header: []sip.Field{f("Route", "<sip:"+externalAddr.String()+";lr;orig>")}, code: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(t, false)

			from := s.carrier
			if tc.from != nil {
				from = tc.from(s)
			}

			if tc.setup != nil {
				tc.setup(s)
			}

			s.dial(from, "offer-1", tc.header...)

			if tc.code == 0 {
				from.none()
				s.ims.none()

				return
			}

			if e := from.next("failed"); e.res == nil || e.res.StatusCode != tc.code {
				t.Fatalf("got %v, want %d", e.res, tc.code)
			}

			s.ims.none()
		})
	}
}

func TestEntryNotANumber(t *testing.T) {
	s := newScene(t, false)

	s.carrier.invite("sip:alice@"+externalAddr.String(), "offer-1")

	if e := s.carrier.next("failed"); e.res == nil || e.res.StatusCode != 404 {
		t.Fatalf("got %v, want 404", e.res)
	}
}

func TestEntryWithoutOffer(t *testing.T) {
	s := newScene(t, false)

	s.dial(s.carrier, "")

	in := s.ims.next("invite").req
	if _, offered := in.Offer(); offered {
		t.Fatalf("INVITE to the I-CSCF with an offer: %s", in.Message())
	}

	_ = in.Accept(sdpOf("offer-ue")...)

	e := s.carrier.all("offer", "established")["offer"]
	if string(e.sdp) != "offer-ue" {
		t.Fatalf("trunk offer %q", e.sdp)
	}

	_ = e.offer.Answer(context.Background(), ua.Change{Bodies: sdpOf("answer-carrier")})

	if got := s.ims.all("established", "answer"); string(got["answer"].sdp) != "answer-carrier" {
		t.Fatalf("UE answer %q", got["answer"].sdp)
	}
}

func TestEntryRejectedWithReason(t *testing.T) {
	s := newScene(t, false)

	s.dial(s.carrier, "offer-1")

	in := s.ims.next("invite").req
	_ = in.Reject(486, sip.Field{Name: "Reason", Value: "Q.850;cause=17"})

	e := s.carrier.next("failed")
	if e.res == nil || e.res.StatusCode != 486 || e.res.Header.Has("Reason") {
		t.Fatalf("trunk response: %v", e.res)
	}
}

func TestEntryCancel(t *testing.T) {
	s := newScene(t, false)

	o := s.dial(s.carrier, "offer-1")

	in := s.ims.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})

	s.carrier.next("progress")

	_ = o.Cancel(context.Background(), ua.Change{})

	s.ims.next("ended")

	if e := s.carrier.next("failed"); e.res == nil || e.res.StatusCode != 487 {
		t.Fatalf("got %v, want 487", e.res)
	}

	s.settle(s.ims, s.carrier)
}

func TestEntryOptions(t *testing.T) {
	s := newScene(t, false)

	req := sip.NewRequest("OPTIONS", sip.URI{Scheme: "sip", Host: externalAddr.Addr().String(), Port: externalAddr.Port()})
	req.Header.Add("From", "<sip:carrier@sip.carrier.example>")
	req.Header.Add("To", "<sip:"+externalAddr.String()+">")

	done := make(chan *sip.Response, 1)

	if err := s.carrier.a.Request(context.Background(), req, func(res *sip.Response, _ error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	s.net.Quiesce()

	res := <-done
	if res == nil || res.StatusCode != 200 || !strings.Contains(values(res, "Allow"), "INVITE") {
		t.Fatalf("OPTIONS: %v", res)
	}
}

func TestEntryBodies(t *testing.T) {
	isup := []byte{0x01, 0x00, 0x49}

	for _, tc := range []struct {
		name        string
		disposition string
		code        int
	}{
		{"required", "", 415},
		{"optional", "signal;handling=optional", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(t, false)

			req := sip.NewRequest("INVITE", sip.URI{Scheme: "sip", User: alice, Host: externalAddr.Addr().String(), Port: externalAddr.Port(), Params: sip.Params{{Name: "user", Value: "phone"}}})
			req.Header.Add("From", "<sip:"+carol+"@sip.carrier.example;user=phone>")
			req.Header.Add("To", "<sip:"+alice+"@"+externalAddr.String()+";user=phone>")

			part := sip.NewBodyPart("application/isup;version=itu-t92+", isup)
			if tc.disposition != "" {
				part.Header.Add("Content-Disposition", tc.disposition)
			}

			req.SetBodies(ua.SDP([]byte("offer-1")), part)

			if _, err := s.carrier.a.Invite(context.Background(), req, s.carrier.handler()); err != nil {
				t.Fatal(err)
			}

			if tc.code != 0 {
				e := s.carrier.next("failed")
				if e.res == nil || e.res.StatusCode != tc.code || values(e.res, "Accept") != accepted {
					t.Fatalf("got %v, want %d with Accept", e.res, tc.code)
				}

				s.ims.none()

				return
			}

			m := s.ims.next("invite").req.Message()
			if m.Header.Get("Content-Type") != "application/sdp" || !hasSDP(m, "offer-1") {
				t.Fatalf("INVITE to the I-CSCF: %s", m)
			}
		})
	}
}

func TestEntryIdentityOfTheCallee(t *testing.T) {
	for _, tc := range []struct {
		privacy string
		pai     bool
	}{{"", true}, {"id", false}, {"none", true}} {
		s := newScene(t, false)

		s.dial(s.carrier, "offer-1")

		in := s.ims.next("invite").req

		h := sip.Header{{Name: "P-Asserted-Identity", Value: "<tel:" + alice + ">"}}
		if tc.privacy != "" {
			h.Add("Privacy", tc.privacy)
		}

		_ = in.Progress(ua.Reply{Code: 180, Header: h})

		ring := s.carrier.next("progress").res
		if got := ring.Header.Has("P-Asserted-Identity"); got != tc.pai {
			t.Errorf("Privacy %q: P-Asserted-Identity %q", tc.privacy, values(ring, "P-Asserted-Identity"))
		}
	}
}
