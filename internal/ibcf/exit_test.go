package ibcf

import (
	"context"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/internal/settings"
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
		{"untrusted header", false, "header", false, true},
		{"trusted header", true, "header", true, true},
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

func TestExitOfferAfterOptionalPartOfUnknownEncoding(t *testing.T) {
	s := newScene(t, false)

	optional := sip.NewBodyPart("application/x-unknown", []byte("x"))
	optional.Header.Set("Content-Encoding", "gzip")
	optional.Header.Set("Content-Disposition", "render;handling=optional")

	target, _ := sip.ParseURI("tel:" + carol)
	req := sip.NewRequest("INVITE", target)

	for _, fl := range []sip.Field{
		f("Route", "<sip:"+internalAddr.String()+";lr>"),
		f("From", "<sip:alice@"+home+">"),
		f("To", "<tel:"+carol+">"),
		f("P-Asserted-Identity", "<tel:"+alice+">"),
	} {
		req.Header.Add(fl.Name, fl.Value)
	}

	req.SetBodies(optional, ua.SDP([]byte("offer-1")))

	if _, err := s.ims.a.Invite(context.Background(), req, s.ims.handler()); err != nil {
		t.Fatal(err)
	}

	in := s.carrier.next("invite").req
	if m := in.Message(); !hasSDP(m, "offer-1") {
		t.Fatalf("trunk INVITE without the offer: %s", m)
	}

	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.all("established", "answer")
	if string(e["answer"].sdp) != "answer-1" {
		t.Fatalf("caller's answer %q", e["answer"].sdp)
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

const bob = "+15550002000"

func forwardedHistory(served string) []sip.Field {
	return []sip.Field{
		f("History-Info", "<sip:"+bob+"@"+home+";user=phone"+served+">;index=1"),
		f("History-Info", "<sip:"+carol+"@"+home+";user=phone;cause=302>;index=1.1;mp=1"),
	}
}

func TestExitForwarded(t *testing.T) {
	const dave = "+442071234567"

	for _, tc := range []struct {
		name    string
		trusted bool
		header  []sip.Field
		from    string
		served  string
		pai     string
		privacy string
	}{
		{
			name:    "caller with an asserted identity",
			trusted: false,
			header: append(forwardedHistory(""), f("From", `"Dave" <sip:`+dave+`@sip.other.example;user=phone>`),
				f("P-Asserted-Identity", "<tel:"+dave+">")),
			from:   "sip:" + dave + "@" + home + ";user=phone",
			served: "sip:" + bob + "@" + home + ";user=phone",
			pai:    "<sip:" + bob + "@" + home + ";user=phone>",
		},
		{
			name:    "caller without an asserted identity",
			trusted: false,
			header:  append(forwardedHistory(""), f("From", `"Dave" <sip:`+dave+`@sip.other.example;user=phone>`)),
			from:    "sip:" + dave + "@" + home + ";user=phone",
			served:  "sip:" + bob + "@" + home + ";user=phone",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
		},
		{
			name:    "with a caller of no number",
			trusted: true,
			header:  append(forwardedHistory(""), f("From", "<sip:dave@sip.other.example>")),
			from:    "sip:dave@sip.other.example",
			served:  "sip:" + bob + "@" + home + ";user=phone",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
		},
		{
			name:    "private forwarder to an untrusted trunk",
			trusted: false,
			header:  append(forwardedHistory("?Privacy=history"), f("From", "<sip:"+dave+"@sip.other.example>")),
			from:    "sip:" + dave + "@" + home + ";user=phone",
			served:  "sip:anonymous@anonymous.invalid",
			privacy: "id",
		},
		{
			name:    "diversion on a response",
			trusted: false,
			header:  append(forwardedHistory("?Reason=SIP%3Bcause%3D486"), f("From", "<sip:"+dave+"@sip.other.example>")),
			from:    "sip:" + dave + "@" + home + ";user=phone",
			served:  "sip:" + bob + "@" + home + ";user=phone?Reason=SIP%3Bcause%3D486",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
		},
		{
			name:    "private forwarder to a trusted trunk",
			trusted: true,
			header:  append(forwardedHistory("?Privacy=history"), f("From", "<sip:"+dave+"@sip.other.example>")),
			from:    "sip:" + dave + "@" + home + ";user=phone",
			served:  "sip:" + bob + "@" + home + ";user=phone?Privacy=history",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
			privacy: "id",
		},
		{
			name:    "private forwarder and a caller with no privacy",
			trusted: true,
			header:  append(forwardedHistory("?Privacy=history"), f("From", "<sip:"+dave+"@sip.other.example>"), f("Privacy", "none")),
			from:    "sip:" + dave + "@" + home + ";user=phone",
			served:  "sip:" + bob + "@" + home + ";user=phone?Privacy=history",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
			privacy: "id",
		},
		{
			name:    "history privacy towards an untrusted trunk",
			trusted: false,
			header:  append(forwardedHistory(""), f("From", "<sip:"+dave+"@sip.other.example>"), f("Privacy", "history")),
			from:    "sip:" + dave + "@" + home + ";user=phone",
			served:  "sip:anonymous@anonymous.invalid",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
		},
		{
			name:    "caller with identity privacy",
			trusted: false,
			header:  append(forwardedHistory(""), f("From", "<sip:"+dave+"@sip.other.example>"), f("Privacy", "id;history")),
			from:    "sip:anonymous@anonymous.invalid",
			served:  "sip:anonymous@anonymous.invalid",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
		},
		{
			name:    "caller with header privacy",
			trusted: true,
			header:  append(forwardedHistory(""), f("From", "<sip:"+dave+"@sip.other.example>"), f("Privacy", "header;user")),
			from:    "sip:anonymous@anonymous.invalid",
			served:  "sip:" + bob + "@" + home + ";user=phone",
			pai:     "<sip:" + bob + "@" + home + ";user=phone>",
			privacy: "user",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(t, tc.trusted)

			s.ims.invite("tel:"+carol, "offer-1", append([]sip.Field{
				f("Route", "<sip:"+internalAddr.String()+";lr>"),
				f("P-Charging-Vector", "icid-value=icid-1"),
			}, tc.header...)...)

			in := s.carrier.next("invite").req
			m := in.Message()

			from, _ := m.Header.From()
			if from.URI.String() != tc.from {
				t.Errorf("From %s, want %s", from.URI, tc.from)
			}

			if got := values(m, "P-Asserted-Identity"); got != tc.pai {
				t.Errorf("P-Asserted-Identity %q, want %q", got, tc.pai)
			}

			want := "<" + tc.served + ">;index=1, <sip:" + carol + "@sip.carrier.example;user=phone;cause=302>;index=1.1;mp=1"
			if got := values(m, "History-Info"); got != want {
				t.Errorf("History-Info %q, want %q", got, want)
			}

			if got := values(m, "Privacy"); got != tc.privacy {
				t.Errorf("Privacy %q, want %q", got, tc.privacy)
			}

			_ = in.Reject(486)

			s.ims.next("failed")
			s.carrier.next("ended")
		})
	}
}

func TestExitForwardedByNoNumber(t *testing.T) {
	s := newScene(t, false)

	s.ims.invite("tel:"+carol, "offer-1", f("Route", "<sip:"+internalAddr.String()+";lr>"),
		f("History-Info", "<sip:bob@"+home+">;index=1"),
		f("History-Info", "<sip:"+carol+"@"+home+";user=phone>;index=1.1;mp=1"),
		f("P-Asserted-Identity", "<tel:"+alice+">"))

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 403 {
		t.Fatalf("got %+v, want 403", e.res)
	}

	s.carrier.none()
}

func TestExitNotForwardedWithHistory(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1", f("History-Info", "<sip:"+carol+"@"+home+";user=phone>;index=1"),
		f("History-Info", "<sip:"+carol+"@"+home+";user=phone>;index=1.1;rc=1"))

	m := s.carrier.next("invite").req.Message()

	if m.Header.Has("History-Info") || values(m, "P-Asserted-Identity") != `"Alice" <sip:`+alice+"@"+home+";user=phone>" {
		t.Fatalf("trunk INVITE: %s", m)
	}
}

func TestExitMaxForwards(t *testing.T) {
	for _, tc := range []struct {
		value string
		code  int
	}{{"0", 483}, {"x", 400}} {
		s := newScene(t, false)

		req := sip.NewRequest("INVITE", sip.URI{Scheme: "tel", User: carol})
		for _, h := range []sip.Field{
			f("Route", "<sip:"+internalAddr.String()+";lr>"),
			f("From", "<sip:alice@"+home+">;tag=a"),
			f("To", "<tel:"+carol+">"),
			f("P-Asserted-Identity", "<tel:"+alice+">"),
			f("Max-Forwards", tc.value),
		} {
			req.Header.Add(h.Name, h.Value)
		}

		if _, err := s.ims.a.Invite(context.Background(), req, s.ims.handler()); err != nil {
			t.Fatal(err)
		}

		if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != tc.code {
			t.Fatalf("Max-Forwards %s: got %+v, want %d", tc.value, e.res, tc.code)
		}

		s.carrier.none()
	}
}

func TestExitEarlyMediaOnlyToTrustedTrunks(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		s := newScene(t, trusted)

		s.call("offer-1", f("P-Early-Media", "supported"))

		m := s.carrier.next("invite").req.Message()
		if got := m.Header.Has("P-Early-Media"); got != trusted {
			t.Errorf("trusted %t: P-Early-Media %q", trusted, values(m, "P-Early-Media"))
		}
	}
}

func TestExitChargingOfTrustedTrunk(t *testing.T) {
	s := newScene(t, true)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180, Header: sip.Header{{Name: "P-Charging-Vector", Value: "icid-value=carrier-icid;term-ioi=carrier"}}})

	ring := s.ims.next("progress").res
	if got := values(ring, "P-Charging-Vector"); got != "icid-value=icid-1;term-ioi=carrier" {
		t.Errorf("ringing with P-Charging-Vector %q", got)
	}

	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	if got := values(e.msg, "P-Charging-Vector"); got != "icid-value=icid-1" {
		t.Errorf("200 with P-Charging-Vector %q", got)
	}
}

func TestExitBodies(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	dtmf := sip.NewBodyPart("application/dtmf-relay", []byte("Signal=1\r\nDuration=160\r\n"))
	optional := sip.NewBodyPart("application/vnd.3gpp.state-and-event-info+xml", []byte("<x/>"))
	optional.Header.Add("Content-Disposition", "render;handling=optional")

	done := make(chan *sip.Response, 1)
	_ = e.s.Send(context.Background(), "INFO", ua.Change{Bodies: []sip.BodyPart{dtmf, optional}},
		func(res *sip.Response, _ error) { done <- res })

	r := s.carrier.next("request").req
	if m := r.Message(); m.Header.Get("Content-Type") != "application/dtmf-relay" || string(m.Body) != "Signal=1\r\nDuration=160\r\n" {
		t.Errorf("INFO to the trunk: %s", m)
	}

	_ = r.Respond(ua.Reply{Code: 200})

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("INFO: %v", res)
	}

	required := sip.NewBodyPart("application/vnd.3gpp.state-and-event-info+xml", []byte("<x/>"))

	_ = e.s.Send(context.Background(), "INFO", ua.Change{Bodies: []sip.BodyPart{required}},
		func(res *sip.Response, _ error) { done <- res })

	s.net.Quiesce()

	res := <-done
	if res == nil || res.StatusCode != 415 || values(res, "Accept") != accepted {
		t.Fatalf("INFO with a required body: %v", res)
	}

	s.carrier.none()
}

func TestExitAcceptsConnectionsOfTheResolvedTrunk(t *testing.T) {
	s := newScene(t, false)

	tr := s.trunk
	tr.InboundCIDRs = nil
	s.setTrunk(tr)

	from := netip.AddrPortFrom(carrierAddr.Addr(), 40000)

	if s.ibcf.Accept(externalAddr, from) {
		t.Fatal("connection of a trunk without a call admitted")
	}

	s.call("offer-1")

	in := s.carrier.next("invite").req

	if !s.ibcf.Accept(externalAddr, from) {
		t.Error("connection of the called trunk refused")
	}

	if s.ibcf.Accept(externalAddr, strangerAddr) {
		t.Error("connection of a stranger admitted")
	}

	_ = in.Reject(486)

	s.ims.next("failed")
	s.carrier.next("ended")
	s.net.Quiesce()

	if s.ibcf.Accept(externalAddr, from) {
		t.Error("connection of the trunk admitted after the call")
	}
}

func TestUnsupportedPrivacy(t *testing.T) {
	for _, tc := range []struct {
		privacy  string
		anchored bool
		want     string
	}{
		{"session;critical", true, ""},
		{"session;critical", false, "session"},
		{"session", false, ""},
		{"id;header;history;critical", false, ""},
		{"x-other;critical", true, "x-other"},
	} {
		p, err := sip.ParsePrivacy(tc.privacy)
		if err != nil {
			t.Fatal(err)
		}

		if got := unsupportedPrivacy(p, tc.anchored); got != tc.want {
			t.Errorf("%s, anchored %t: %q, want %q", tc.privacy, tc.anchored, got, tc.want)
		}
	}
}

func TestExitNoUsableTrunk(t *testing.T) {
	s := newScene(t, false)

	tr := s.trunk
	tr.Enabled = false
	s.setTrunk(tr)

	s.call("offer-1")

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 503 {
		t.Fatalf("got %+v, want 503", e.res)
	}

	s.carrier.none()
}

func TestEffectiveTrunk(t *testing.T) {
	s := newScene(t, false)

	tr := s.trunk
	tr.InboundCIDRs = nil
	tr.Domain = "sip.carrier.example"

	s.mu.Lock()
	s.st.Trunks = []settings.Trunk{tr.Effective()}
	s.mu.Unlock()

	s.dial(s.carrier, "offer-1")

	in := s.ims.next("invite").req
	_ = in.Reject(486)

	if e := s.carrier.next("failed"); e.res == nil || e.res.StatusCode != 486 {
		t.Fatalf("got %+v, want 486", e.res)
	}

	s.call("offer-1")

	if m := s.carrier.next("invite").req.Message(); m.URI.Host != "sip.carrier.example" {
		t.Fatalf("Request-URI %s", m.URI)
	}
}

// TS 24.229 clause 5.3.2.1
func TestExitSource(t *testing.T) {
	other := []netip.AddrPort{netip.MustParseAddrPort("10.0.0.9:5070")}

	tests := []struct {
		name         string
		icscf, scscf []netip.AddrPort
		accepted     bool
	}{
		{name: "S-CSCF", icscf: other, scscf: []netip.AddrPort{imsAddr}, accepted: true},
		{name: "I-CSCF", icscf: []netip.AddrPort{imsAddr}, scscf: other, accepted: true},
		{name: "other node", icscf: other, scscf: other},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSceneWith(t, false, func(c *Config) { c.ICSCF, c.SCSCF = tt.icscf, tt.scscf })

			s.call("offer-1")

			if !tt.accepted {
				if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 403 {
					t.Fatalf("got %+v, want 403", e.res)
				}

				s.carrier.none()

				return
			}

			s.carrier.next("invite")
		})
	}
}
