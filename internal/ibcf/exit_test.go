package ibcf

import (
	"context"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

func TestExitCall(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1",
		f("P-Access-Network-Info", "3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=0010100010019B01"),
		f("History-Info", "<sip:bob@"+home+">;index=1"),
		f("Feature-Caps", "*;+g.3gpp.srvcc"),
		f("Resource-Priority", "wps.4"),
		f("P-Visited-Network-ID", "visited.example"),
		f("P-Charging-Function-Addresses", "ccf=192.0.2.10"),
		f("Session-ID", "ab30317f1a784dc48ff824d0d3715d86"),
	)

	in := s.carrier.next("invite").req
	m := in.Message()

	checks := map[string]string{
		"Request-URI":  m.URI.String(),
		"From":         "",
		"To":           values(m, "To"),
		"PAI":          values(m, "P-Asserted-Identity"),
		"Session-ID":   values(m, "Session-ID"),
		"Max-Forwards": values(m, "Max-Forwards"),
		"stripped":     values(m, "P-Access-Network-Info") + values(m, "History-Info") + values(m, "Feature-Caps") + values(m, "Resource-Priority") + values(m, "P-Visited-Network-ID") + values(m, "P-Charging-Vector") + values(m, "P-Charging-Function-Addresses") + values(m, "Route"),
		"Contact":      values(m, "Contact"),
		"flow":         m.Flow.Remote.String(),
	}

	from, _ := m.Header.From()
	checks["From"] = from.Display + " " + from.URI.String()

	want := map[string]string{
		"Request-URI":  "sip:" + carol + "@sip.carrier.example;user=phone",
		"From":         `"Alice" sip:` + alice + "@" + home + ";user=phone",
		"To":           "<sip:" + carol + "@sip.carrier.example;user=phone>",
		"PAI":          `"Alice" <sip:` + alice + "@" + home + ";user=phone>",
		"Session-ID":   "ab30317f1a784dc48ff824d0d3715d86",
		"Max-Forwards": "69",
		"stripped":     "",
		"Contact":      "<sip:" + externalAddr.String() + ">",
		"flow":         externalAddr.String(),
	}

	for k, v := range want {
		if checks[k] != v {
			t.Errorf("trunk INVITE %s = %q, want %q", k, checks[k], v)
		}
	}

	if !hasSDP(m, "offer-1") {
		t.Errorf("trunk INVITE without the offer: %q", m.Body)
	}

	if m.Header.CallID() == "" {
		t.Fatal("no Call-ID")
	}

	_ = in.Progress(ua.Reply{Code: 180, Header: sip.Header{{Name: "Reason", Value: "Q.850;cause=16"}}})

	ring := s.ims.next("progress").res
	if ring.StatusCode != 180 || values(ring, "P-Charging-Vector") != "icid-value=icid-1" {
		t.Errorf("ringing %s with P-Charging-Vector %q", ring.StartLine(), values(ring, "P-Charging-Vector"))
	}

	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	if !hasSDP(e.msg, "answer-1") || values(e.msg, "Contact") != "<sip:"+internalAddr.String()+">" {
		t.Errorf("caller's 200: %s", e.msg)
	}

	s.ims.next("answer")
	s.carrier.next("established")

	done := make(chan *sip.Response, 1)
	_ = e.s.Bye(context.Background(), ua.Change{}, func(res *sip.Response, _ error) { done <- res })

	s.carrier.next("bye")
	s.carrier.next("ended")

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("BYE: %v", res)
	}

	s.ims.next("ended")
	s.settle(s.ims, s.carrier)
}

func TestExitByeFromTrunk(t *testing.T) {
	s := newScene(t, true)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	s.ims.next("answer")

	ce := s.carrier.next("established")

	_ = ce.s.Bye(context.Background(), ua.Change{Header: sip.Header{{Name: "Reason", Value: "Q.850;cause=16"}}}, nil)

	bye := s.ims.next("bye")
	if bye.s != e.s || values(bye.msg, "P-Charging-Vector") != "icid-value=icid-1" {
		t.Errorf("BYE to the caller: %s", bye.msg)
	}

	s.ims.next("ended")
	s.carrier.next("ended")
	s.settle(s.ims, s.carrier)
}

func TestExitPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted bool
		privacy string
		pai     bool
		anon    bool
	}{
		{"untrusted id", false, "id", false, true},
		{"trusted id", true, "id", true, true},
		{"untrusted user", false, "user", true, true},
		{"untrusted none", false, "none", true, false},
		{"untrusted", false, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(t, tc.trusted)

			var extra []sip.Field
			if tc.privacy != "" {
				extra = append(extra, f("Privacy", tc.privacy))
			}

			s.call("offer-1", append(extra, f("Subject", "secret"))...)

			in := s.carrier.next("invite").req
			m := in.Message()

			if got := m.Header.Has("P-Asserted-Identity"); got != tc.pai {
				t.Errorf("P-Asserted-Identity %q, want present %t", values(m, "P-Asserted-Identity"), tc.pai)
			}

			from, _ := m.Header.From()
			if got := from.URI.String() == "sip:anonymous@anonymous.invalid"; got != tc.anon {
				t.Errorf("From %s, want anonymous %t", from.URI, tc.anon)
			}

			if values(m, "Privacy") != tc.privacy {
				t.Errorf("Privacy %q, want %q", values(m, "Privacy"), tc.privacy)
			}

			if got, want := m.Header.Has("Subject"), tc.privacy != "user"; got != want {
				t.Errorf("Subject present %t, want %t", got, want)
			}

			_ = in.Reject(486)

			s.ims.next("failed")
			s.carrier.next("ended")
		})
	}
}

func TestExitNoCallerNumber(t *testing.T) {
	s := newScene(t, false)

	s.ims.invite("tel:"+carol, "offer-1", f("Route", "<sip:"+internalAddr.String()+";lr>"),
		f("P-Asserted-Identity", "<sip:alice@"+home+">"))

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 403 {
		t.Fatalf("got %+v, want 403", e.res)
	}

	s.carrier.none()
}

func TestExitNoRoute(t *testing.T) {
	s := newScene(t, false)
	s.mu.Lock()
	s.st.SIPRoutes = nil
	s.mu.Unlock()

	s.call("offer-1")

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 404 {
		t.Fatalf("got %+v, want 404", e.res)
	}
}

func TestExitWithoutOffer(t *testing.T) {
	s := newScene(t, false)

	s.call("")

	in := s.carrier.next("invite").req
	if in.Message().Header.Has("Content-Type") {
		t.Fatalf("trunk INVITE with a body: %s", in.Message())
	}

	_ = in.Accept(sdpOf("offer-b")...)

	e := s.ims.all("offer", "established")["offer"]
	if string(e.sdp) != "offer-b" {
		t.Fatalf("caller's offer %q", e.sdp)
	}

	_ = e.offer.Answer(context.Background(), ua.Change{Bodies: sdpOf("answer-a")})

	got := s.carrier.all("established", "answer")
	if string(got["answer"].sdp) != "answer-a" {
		t.Fatalf("trunk answer %q", got["answer"].sdp)
	}
}

func TestExitCancel(t *testing.T) {
	s := newScene(t, false)

	o := s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})

	s.ims.next("progress")

	_ = o.Cancel(context.Background(), ua.Change{})

	s.carrier.next("ended")

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 487 {
		t.Fatalf("got %+v, want 487", e.res)
	}

	s.settle(s.ims, s.carrier)
}

func TestExitReliableProgress(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1", f("Supported", "100rel"))

	in := s.carrier.next("invite").req
	if !in.Message().Header.HasOptionTag("Supported", sip.OptionTag100rel) {
		t.Fatalf("trunk INVITE without 100rel: %s", in.Message())
	}

	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})

	p := s.ims.all("progress", "answer")["progress"]
	if !p.res.Header.HasOptionTag("Require", sip.OptionTag100rel) || !hasSDP(p.res, "answer-1") {
		t.Fatalf("caller's 183: %s", p.res)
	}

	_ = in.Accept()

	s.ims.next("established")
	s.carrier.next("established")
}

func TestExitReinviteFromTrunk(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	s.ims.next("established")
	s.ims.next("answer")

	ce := s.carrier.next("established")

	done := make(chan *sip.Response, 1)
	_ = ce.s.Reinvite(context.Background(), ua.Change{Bodies: sdpOf("offer-2")}, func(res *sip.Response, _ error) { done <- res })

	r := s.ims.next("request").req
	if offer, _ := r.Offer(); string(offer) != "offer-2" || r.Message().Method != "INVITE" {
		t.Fatalf("caller's re-INVITE: %s", r.Message())
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-2")})

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 || !hasSDP(res, "answer-2") {
		t.Fatalf("re-INVITE: %v", res)
	}
}
