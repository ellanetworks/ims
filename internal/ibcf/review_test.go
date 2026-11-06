package ibcf

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

func TestExitHeaderAllowlist(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		s := newScene(t, trusted)

		s.call("offer-1",
			f("Remote-Party-ID", "<sip:+18005551234@x>;party=calling;screen=yes;privacy=off"),
			f("Diversion", "<tel:+18005551234>;reason=unconditional"),
			f("P-Charge-Info", "<sip:+18005559999@x>"),
			f("Identity", "abc.def.ghi;info=<https://cert.example/x.pem>"),
			f("Priority", "psap-callback"),
			f("P-Access-Network-Info", "3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=0010100010019B01"),
			f("Cellular-Network-Info", "3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=0010100010019B01"),
			f("P-Asserted-Service", "urn:urn-7:3gpp-service.ims.icsi.mmtel"),
			f("Feature-Caps", "*;+g.3gpp.srvcc"),
			f("History-Info", "<sip:bob@"+home+">;index=1"),
			f("Call-Info", "<http://"+home+"/alice.jpg>;purpose=icon"),
			f("Warning", `399 scscf.`+home+` "internal"`),
			f("Privacy", "id"),
		)

		m := s.carrier.next("invite").req.Message()

		for _, n := range []string{
			"Remote-Party-ID", "Diversion", "P-Charge-Info", "Identity", "Priority", "P-Access-Network-Info", "Cellular-Network-Info",
			"P-Asserted-Service", "Feature-Caps", "History-Info", "Call-Info", "Warning",
		} {
			if m.Header.Has(n) {
				t.Errorf("trusted %t: %s %q sent to the trunk", trusted, n, values(m, n))
			}
		}

		if got := m.Header.Has("P-Asserted-Identity"); got != trusted {
			t.Errorf("trusted %t: P-Asserted-Identity %q", trusted, values(m, "P-Asserted-Identity"))
		}

		if from, _ := m.Header.From(); from.URI.String() != "sip:anonymous@anonymous.invalid" || values(m, "Privacy") != "id" {
			t.Errorf("trusted %t: From %s, Privacy %q", trusted, from, values(m, "Privacy"))
		}
	}
}

func TestExitOnlyFromSCSCF(t *testing.T) {
	s := newScene(t, false)
	rogue := newParty(t, s.net, "rogue", netip.MustParseAddrPort("10.0.0.9:5060"))

	b, err := launch(t, Config{
		HomeDomain: home,
		Internal:   []netip.AddrPort{netip.MustParseAddrPort("10.0.0.2:5085")},
		External:   []netip.AddrPort{netip.MustParseAddrPort("203.0.113.2:5060")},
		ICSCF:      []netip.AddrPort{imsAddr},
		SCSCF:      []netip.AddrPort{imsAddr},
		Trust:      trust.New(nil, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}),
		BGCF:       bgcf.New(s.settings, nil),
		Settings:   s.settings,
	}, s.net.Config(transaction.Config{}), false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { s.net.Await(func() { _ = b.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1) })

	rogue.invite("tel:+19005550000", "offer-1", f("Route", "<sip:10.0.0.2:5085;lr>"), f("P-Asserted-Identity", "<tel:+15550009999>"))

	if e := rogue.next("failed"); e.res == nil || e.res.StatusCode != 403 {
		t.Fatalf("got %v, want 403", e.res)
	}

	s.carrier.none()
}

func TestEntryInternalHeaders(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		s := newScene(t, trusted)

		s.dial(s.carrier, "offer-1",
			f("P-Asserted-Identity", "<sip:+18005551234@x;user=phone>"),
			f("P-Served-User", "<sip:"+alice+"@"+home+">;sescase=orig;regstate=reg"),
			f("P-Private-Network-Indication", "corp.example"),
			f("P-Profile-Key", "<sip:psi@"+home+">"),
			f("P-Called-Party-ID", "<sip:bank@"+home+">"),
			f("Remote-Party-ID", "<sip:+18005551234@x>;party=calling"),
			f("Priority", "psap-callback"),
			f("P-Charging-Vector", "icid-value=chosen-by-carrier"),
		)

		m := s.ims.next("invite").req.Message()

		for _, n := range []string{"P-Served-User", "P-Private-Network-Indication", "P-Profile-Key", "P-Called-Party-ID", "Remote-Party-ID", "Priority"} {
			if m.Header.Has(n) {
				t.Errorf("trusted %t: %s %q reached the IMS", trusted, n, values(m, n))
			}
		}

		if got := m.Header.Has("P-Asserted-Identity"); got != trusted {
			t.Errorf("trusted %t: P-Asserted-Identity %q", trusted, values(m, "P-Asserted-Identity"))
		}

		if got := m.Header.ICID() == "chosen-by-carrier"; got != trusted {
			t.Errorf("trusted %t: P-Charging-Vector %q", trusted, values(m, "P-Charging-Vector"))
		}
	}
}

func TestEntryTrustedOverUDP(t *testing.T) {
	s := newScene(t, true)
	s.carrier.tr = sip.UDP

	s.dial(s.carrier, "offer-1")

	if e := s.carrier.next("failed"); e.res == nil || e.res.StatusCode != 403 {
		t.Fatalf("got %v, want 403", e.res)
	}

	s.ims.none()
}

func TestExitByeFromTrunkServerOutsideCIDRs(t *testing.T) {
	s := newScene(t, false)
	tr := s.trunk
	tr.InboundCIDRs = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	s.setTrunk(tr)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	s.ims.next("established")
	s.ims.next("answer")

	ce := s.carrier.next("established")

	done := make(chan *sip.Response, 1)
	_ = ce.s.Bye(context.Background(), ua.Change{}, func(res *sip.Response, _ error) { done <- res })

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("carrier BYE on an outgoing call: %v", res)
	}

	s.ims.next("bye")
}

func TestExitInDialogFromAnotherTrunk(t *testing.T) {
	s := newScene(t, false)

	other := s.trunk
	other.ID, other.Name, other.InboundCIDRs = "other", "Other", []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}

	s.mu.Lock()
	s.st.Trunks = append(s.st.Trunks, other)
	s.mu.Unlock()

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	s.ims.next("established")
	s.ims.next("answer")

	ce := s.carrier.next("established")
	id := ce.s.ID()

	rogue := newParty(t, s.net, "rogue", strangerAddr)

	bye := sip.NewRequest("BYE", sip.URI{Scheme: "sip", Host: externalAddr.Addr().String(), Port: externalAddr.Port()})
	bye.Header.Add("From", "<sip:carrier@x>;tag="+id.LocalTag)
	bye.Header.Add("To", "<sip:ibcf@x>;tag="+id.RemoteTag)
	bye.Header.Add("Call-ID", id.CallID)
	bye.Header.Add("CSeq", "9 BYE")

	done := make(chan *sip.Response, 1)
	_ = rogue.a.Request(context.Background(), bye, func(res *sip.Response, _ error) { done <- res })

	s.net.Quiesce()

	select {
	case res := <-done:
		if res != nil && res.StatusCode < 300 {
			t.Fatalf("BYE from another source: %v", res)
		}
	default:
	}

	s.ims.none()
}

func TestExitCallIDReuse(t *testing.T) {
	s := newScene(t, false)

	o := s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Reject(420, sip.Field{Name: "Unsupported", Value: "foo"})

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 500 {
		t.Fatalf("420 from the trunk relayed as %v, want 500", e.res)
	}

	req := o.Request().Clone()
	req.Header.Del("Via")

	cseq, _ := req.Header.CSeq()
	cseq.Seq++
	req.Header.Set("CSeq", cseq.String())

	if _, err := s.ims.a.Invite(context.Background(), req, s.ims.handler()); err != nil {
		t.Fatal(err)
	}

	s.carrier.until("invite")
}

func TestExitCarrierForks(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1", f("Supported", "100rel"))

	in := s.carrier.next("invite").req
	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-a")})

	fk, err := in.Fork(s.carrier.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = fk.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-b")})

	s.net.Quiesce()

	_ = fk.Accept(sdpOf("answer-b")...)

	var second *ua.Session

	for {
		e := s.ims.until("progress")
		if hasSDP(e.res, "answer-b") {
			second = e.s
			break
		}
	}

	if e := s.ims.until("established"); e.s != second {
		t.Errorf("caller's call established on another early dialog than the answering fork's")
	}
}

func TestExitCancelRace(t *testing.T) {
	s := newScene(t, false)

	o := s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})

	s.ims.next("progress")

	_ = o.Cancel(context.Background(), ua.Change{})
	_ = in.Accept(sdpOf("answer-1")...)

	s.net.Quiesce()
	s.settle(s.ims, s.carrier)
}

func TestExitReinviteAndUpdateFromIMS(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	for i, m := range []string{"INVITE", "UPDATE"} {
		done := make(chan *sip.Response, 1)
		cb := func(res *sip.Response, _ error) { done <- res }
		offer, answer := sdpOf("offer-"+m), sdpOf("answer-"+m)

		if m == "INVITE" {
			_ = e.s.Reinvite(context.Background(), ua.Change{Bodies: offer}, cb)
		} else {
			_ = e.s.Update(context.Background(), ua.Change{Bodies: offer}, cb)
		}

		r := s.carrier.next("request").req
		if r.Message().Method != m || r.Message().Flow.Remote != externalAddr {
			t.Fatalf("%d: trunk got %s from %s", i, r.Message().Method, r.Message().Flow.Remote)
		}

		_ = r.Respond(ua.Reply{Code: 200, Bodies: answer})

		s.net.Quiesce()

		if res := <-done; res == nil || res.StatusCode != 200 || !hasSDP(res, "answer-"+m) {
			t.Fatalf("%s: %v", m, res)
		}

		s.ims.next("answer")
	}
}

func TestExitDualStackTrunk(t *testing.T) {
	s := newScene(t, false)

	s.ibcf.cfg.Resolve = func(context.Context, sip.URI, []sip.Transport) ([]sip.Server, error) {
		return []sip.Server{
			{Transport: sip.UDP, Addr: netip.MustParseAddrPort("[2001:db8::1]:5060")},
			{Transport: sip.UDP, Addr: carrierAddr},
		}, nil
	}

	s.call("offer-1")
	s.carrier.next("invite")
}

func TestExitLongRing(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1")

	in := s.carrier.next("invite").req

	for range 4 {
		_ = in.Progress(ua.Reply{Code: 180})

		s.ims.next("progress")
		s.net.Advance(55 * time.Second)
	}

	s.ims.none()
	s.net.Advance(timerC)

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 408 {
		t.Fatalf("caller got %v, want 408 from Timer C", e.res)
	}

	s.carrier.next("ended")
}
