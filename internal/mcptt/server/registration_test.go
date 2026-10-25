package server

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/sip"
)

// bobIMSI is a public user identity of bob not configured for him, of his implicit registration set: one derived of
// his IMSI (TS 23.003 §13.4B), the To of a third-party REGISTER from an S-CSCF that copies the UE's (TS 24.229
// §5.4.1.7 c allows either).
const bobIMSI = "sip:001010000000002@ims.example.com"

// featureCaps is a request of a registration token in its Feature-Caps (TS 24.229 §7.9A.8).
func featureCaps(token string) *sip.Request {
	req := sip.NewRequest("INVITE", sip.URI{Scheme: "sip", Host: "mcptt.example.com"})
	req.Header.Set("Feature-Caps", `*;+g.3gpp.registration-token="`+token+`"`)

	return req
}

// TS 24.229 §5.4.1.7 c, §5.4.1.7A, §5.4.1.2.2F b, §5.4.2.1.2 step 3; TS 24.379 §10.1.1.3.1.1 step 2, §9.2.2.2.3
// step 2: the To of the third-party REGISTER is one public user identity of the implicit registration set, and the
// client is served at any of them, as the P-CSCF asserts the default one (§5.2.6.3.1): the set of the
// P-Associated-URI of the 200 to the client's REGISTER (Ella IMS), else of the third-party REGISTER (Kamailio), else
// of the IMPUs the reg event package reports registered. The service authorisation is at the set of the third-party
// REGISTER: a To not configured for the user is authorised by another IMPU of the set.
func TestImplicitRegistrationSet(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pauHeader bool
		regEvent  bool
	}{
		{name: "200 of the client's REGISTER"},
		{name: "third-party REGISTER", pauHeader: true},
		{name: "reg event", regEvent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, registrar.ThroughIMS)
			s := h.scscf
			s.pauHeader, s.regEvent = tc.pauHeader, tc.regEvent

			// Of the reg event only, the To is the IMPU configured: the service authorisation precedes the NOTIFYs.
			to := bobIM
			if !tc.regEvent {
				to, s.pau = bobIMSI, []string{bobTel, bobIM, bobIMSI}
			}

			ue := h.client(to)

			if res := s.send(s.register(to, "", 600, ue.register("sip:ims.example.com", 600, info(token(bob), client1)))); res.StatusCode != 200 {
				t.Fatalf("third-party REGISTER: %d", res.StatusCode)
			}

			s.pump()

			if tc.regEvent {
				if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, bobTel)}, client1); ok {
					t.Fatal("served at an IMPU not reported")
				}

				s.notify(bobIM, 1, "full", `<registration aor="`+bobIMSI+`" id="r1" state="active"/>`+
					`<registration aor="`+bobTel+`" id="r2" state="active"/><registration aor="`+bobIM+`" id="r3" state="active"/>`)
			}

			for _, id := range []string{bobTel, bobIM, bobIMSI} {
				if b, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, id)}, client1); !ok || b.MCPTTID.String() != bob || b.IMPU.String() != to {
					t.Errorf("served user of %s: %+v", id, b)
				}
			}

			if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok {
				t.Error("served at another IMPU")
			}

			// The affiliation PUBLISH of the default public user identity, a tel URI.
			if res := s.send(s.forward(ue.publish(bob, client1, 4294967295, police), bobTel)); res.StatusCode != 200 {
				t.Errorf("PUBLISH: %d", res.StatusCode)
			}
		})
	}
}

// TS 24.229 §5.4.1.7 l, §5.7.1.1; TS 24.379 §7.3.2: a reregistration of the UE of a binding, by its instance ID, of
// another registration token and S-CSCF, a new registration whose deregistration the server missed: the binding takes
// them, and its expiry; the requests of the former token are no longer of it. The reg event subscription moves to
// the new S-CSCF, the former one ended.
func TestReregistrationNewToken(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s, s2 := h.scscf, h.scscf2
	s.regEvent, s2.regEvent = true, true

	ue := h.client(aliceIM)
	ue.instance = "urn:uuid:00000000-0000-1000-8000-000000000001"

	if res := s.send(s.register(aliceIM, "7", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	s.pump()

	if s.subs[aliceIM] == nil {
		t.Fatal("not subscribed")
	}

	h.n.Advance(500 * time.Second)

	if res := s2.send(s2.register(aliceIM, "8", 600, ue.register("sip:ims.example.com", 600))); res.StatusCode != 200 {
		t.Fatalf("reregistration: %d", res.StatusCode)
	}

	s.pump()
	s2.pump()

	b, ok := h.s.Registrar().Resolve(featureCaps("8"), []sip.URI{uri(t, aliceIM)}, client1)
	if !ok || b.Token != "8" || b.SCSCF.HostPort() != s2.p.Addr().String() {
		t.Fatalf("binding %+v", b)
	}

	if _, ok := h.s.Registrar().Resolve(featureCaps("7"), []sip.URI{uri(t, aliceIM)}, client1); ok {
		t.Error("served at the former token")
	}

	if !slices.Contains(s.unsubscribed, aliceIM) || s2.subs[aliceIM] == nil {
		t.Errorf("subscriptions: ended %v at the former S-CSCF, at the new %v", s.unsubscribed, s2.subs)
	}

	h.n.Advance(500 * time.Second)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok || !h.affiliated(alice, client1, fire) {
		t.Fatal("the binding expired")
	}
}

// TS 24.229 §5.7.1.3B: a request for a standalone transaction, as an affiliation PUBLISH, is of the registration of
// its registration token: a UE of the IMPU publishes for its own client only.
func TestPublishRegistrationToken(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	ue1, ue2 := h.client(aliceIM), h.client(aliceIM)

	for i, ue := range []*client{ue1, ue2} {
		c := []string{client1, client2}[i]
		if res := s.send(s.register(aliceIM, []string{"t1", "t2"}[i], 600, ue.register("sip:ims.example.com", 600, info(token(alice), c)))); res.StatusCode != 200 {
			t.Fatalf("UE %d: %d", i, res.StatusCode)
		}
	}

	publish := func(token string) int {
		req := s.forward(ue2.publish(alice, client1, 4294967295, police), aliceIM)
		req.Header.Set("Feature-Caps", `*;+g.3gpp.registration-token="`+token+`"`)

		return s.send(req).StatusCode
	}

	if code := publish("t2"); code != 403 || h.affiliated(alice, client1, police) {
		t.Fatalf("PUBLISH for the client of another registration: %d", code)
	}

	if code := publish("t1"); code != 200 || !h.affiliated(alice, client1, police) {
		t.Fatalf("PUBLISH of its registration: %d", code)
	}
}

// TS 24.229 §5.7.1.1 a, c: the third-party REGISTER of a tel URI IMPU, of its SIP URI in its implicit registration set:
// the reg event subscription is to the SIP URI, and a deregistration is told by it; the server unsubscribes once the
// IMPU has no binding.
func TestRegEventTelURI(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	s.regEvent = true
	s.pau = []string{bobTel, bobIM}

	ue1, ue2 := h.client(bobTel), h.client(bobTel)
	ue1.instance = "urn:uuid:00000000-0000-1000-8000-000000000001"

	for i, ue := range []*client{ue1, ue2} {
		c := []string{client1, client2}[i]
		if res := s.send(s.register(bobTel, "", 600, ue.register("sip:ims.example.com", 600, info(token(bob), c)))); res.StatusCode != 200 {
			t.Fatalf("UE %d: %d", i, res.StatusCode)
		}
	}

	s.pump()

	if s.subs[bobIM] == nil {
		t.Fatalf("subscriptions %v", s.subs)
	}

	if res := s.send(s.register(bobTel, "", 0, nil)); res.StatusCode != 200 || len(h.s.Registrar().Bindings(uri(t, bob))) != 2 {
		t.Fatalf("deregistration: %d", res.StatusCode)
	}

	s.notify(bobIM, 1, "partial", `<registration aor="`+bobTel+`" id="r1" state="active">`+terminated("c9", ue1.instance, "", 0)+`</registration>`)

	if bs := h.s.Registrar().Bindings(uri(t, bob)); len(bs) != 1 || bs[0].ClientID != client2 {
		t.Fatalf("bindings %+v", bs)
	}

	s.notify(bobIM, 2, "partial", `<registration aor="`+bobTel+`" id="r1" state="terminated"/>`)
	s.pump()

	if len(h.s.Registrar().Bindings(uri(t, bob))) != 0 || !slices.Equal(s.unsubscribed, []string{bobIM}) {
		t.Fatalf("unsubscribed %v", s.unsubscribed)
	}
}

// RFC 6665 §4.1.3, §4.4.1; TS 24.229 §5.7.1.1 NOTE 5: a NOTIFY of Subscription-State terminated, all registrations
// terminated, ends the subscription and the bindings: the server sends no SUBSCRIBE to end it (which the S-CSCF would
// answer with a 481), and the next third-party REGISTER subscribes again.
func TestRegEventTerminated(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	s.regEvent = true
	ue := h.client(aliceIM)

	if res := s.send(s.register(aliceIM, "t1", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("REGISTER: %d", res.StatusCode)
	}

	s.pump()

	if res := s.send(s.register(aliceIM, "", 0, nil)); res.StatusCode != 200 || len(h.s.Registrar().Bindings(uri(t, alice))) != 1 {
		t.Fatalf("deregistration: %d", res.StatusCode)
	}

	s.state = "terminated;reason=noresource"
	s.notify(aliceIM, 1, "full", `<registration aor="`+aliceIM+`" id="r1" state="terminated"/>`)
	h.n.Quiesce()
	s.pump()

	if len(h.s.Registrar().Bindings(uri(t, alice))) != 0 || len(s.unsubscribed) != 0 {
		t.Fatalf("unsubscribed %v", s.unsubscribed)
	}

	s.state = ""
	delete(s.subs, aliceIM)

	if res := s.send(s.register(aliceIM, "t2", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("REGISTER again: %d", res.StatusCode)
	}

	s.pump()

	if s.subs[aliceIM] == nil {
		t.Error("not subscribed again")
	}
}

// TS 24.229 §5.4.1.2.2 step 5 d, §5.4.2.1.2 NOTE 6, §5.1.1.2.1; RFC 5626 §4.2: the MCPTT client and the device's VoLTE
// client share the instance ID of its IMEI, on registration flows of their own reg-ids. VoLTE's REGISTER, without the
// MCPTT feature tags, is of another registration, as is its flow terminated in the reg event: the client stays logged
// on. Its own flow terminated logs it off.
func TestFlowsOfOneInstance(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	s.regEvent = true

	const imei = "urn:gsma:imei:35693803-564380-0"

	mcptt, volte := h.client(aliceIM), h.client(aliceIM)
	mcptt.instance, mcptt.regID = imei, "1"
	volte.instance, volte.regID = imei, "2"

	if res := s.send(s.register(aliceIM, "t1", 600, mcptt.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 || !h.affiliated(alice, client1, fire) {
		t.Fatalf("MCPTT: %d", res.StatusCode)
	}

	s.pump()

	for _, tok := range []string{"t2", ""} {
		plain := volte.register("sip:ims.example.com", 600)
		plain.Header.Set("Contact", fmt.Sprintf(`<sip:ue@%s>;+sip.instance="<%s>";reg-id=2;+g.3gpp.icsi-ref="urn%%3Aurn-7%%3A3gpp-service.ims.icsi.mmtel"`, volte.p.Addr(), imei))

		if res := s.send(s.register(aliceIM, tok, 600, plain)); res.StatusCode != 200 || !h.affiliated(alice, client1, fire) {
			t.Fatalf("VoLTE of token %q: %d, the client logged off", tok, res.StatusCode)
		}
	}

	s.notify(aliceIM, 1, "partial", active(terminatedFlow("c2", imei, "2", "", 0)))

	if !h.affiliated(alice, client1, fire) {
		t.Fatal("VoLTE's flow terminated logged the client off")
	}

	s.notify(aliceIM, 2, "partial", active(terminatedFlow("c1", imei, "1", "", 0)))

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok || h.affiliated(alice, client1, fire) {
		t.Fatal("the client's flow terminated, it is still logged on")
	}
}
