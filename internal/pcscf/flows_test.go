package pcscf

import (
	"errors"
	"log/slog"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const testInstance = "urn:gsma:imei:35209900-176148-1"

func flowRegKey(regID int64) regKey {
	return regKey{impi: testIMPI, ue: ueAddr, flow: flowID{instance: testInstance, regID: regID}}
}

// withFlow makes a REGISTER that of the UE's registration flow regID (RFC 5626 §4.2).
func (s *regScene) withFlow(regID int64, edit func(*sip.Request)) func(*sip.Request) {
	return func(r *sip.Request) {
		r.Header.Set("Contact", s.contact()+`;+sip.instance="<`+testInstance+`>";reg-id=`+strconv.FormatInt(regID, 10))

		if edit != nil {
			edit(r)
		}
	}
}

// over has the UE send and receive over sock while f runs: another network flow (RFC 5626 §3.1).
func (s *regScene) over(sock *siptest.Socket, f func()) {
	ue := s.ue
	s.ue = sock

	defer func() { s.ue = ue }()

	f()
}

// registerFlows registers the UE's flows 1 and 2, each over a network flow of its own, and returns their
// flow tokens.
func (s *regScene) registerFlows() (string, string) {
	s.t.Helper()

	req, f := s.register(s.withFlow(1, nil))
	answerRegisterWith(s, req, f, testIMPU, testTel)

	sub, sf := s.icscf.RecvRequest()
	answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	var req2 *sip.Request

	s.over(siptest.NewSocket(s.t, netip.AddrPortFrom(ueAddr, 0)), func() {
		var f2 sip.Flow

		req2, f2 = s.register(s.withFlow(2, nil))
		answerRegisterWith(s, req2, f2, testIMPU, testTel)
	})

	return onlyPath(s.t, req).User, onlyPath(s.t, req2).User
}

// TS 24.229 §5.2.2.1: a registration flow has its own registration, and its own IMS flow token.
func TestFlowsRegisterApart(t *testing.T) {
	s := newRegScene(t)

	one, two := s.registerFlows()
	if one == two {
		t.Fatalf("flows share the flow token %q", one)
	}

	for regID, token := range map[int64]string{1: one, 2: two} {
		if r, ok := s.p.regs.get(flowRegKey(regID)); !ok || r.FlowToken != token || r.RegID != regID || r.Instance != testInstance {
			t.Fatalf("flow %d = %+v, %v; want token %q", regID, r, ok, token)
		}
	}

	req, f := s.register(s.withFlow(1, nil))
	if p := onlyPath(t, req); p.User != one {
		t.Fatalf("flow 1 refreshed with token %q, want %q", p.User, one)
	}

	answerRegisterWith(s, req, f, testIMPU, testTel)
	s.icscf.RecvNone(quiet)
}

// TS 24.229 §5.2.5.1 steps 1A and 2A
func TestDeregisterOneFlow(t *testing.T) {
	s := newRegScene(t)
	s.registerFlows()

	req, f := s.register(s.withFlow(1, func(r *sip.Request) { r.Header.Set("Expires", "0") }))
	answerRegisterWith(s, req, f)

	if _, ok := s.p.regs.get(flowRegKey(1)); ok {
		t.Fatal("flow 1 still registered")
	}

	if _, ok := s.p.regs.get(flowRegKey(2)); !ok {
		t.Fatal("flow 2 deregistered with flow 1")
	}

	if !s.p.subs.has(testIMPI) {
		t.Fatal("subscription dropped while a flow is registered")
	}
}

// A NOTIFY that ends one flow leaves the others, though they share its contact URI (RFC 5626 §6).
func TestNotifyEndsOneFlow(t *testing.T) {
	s := newRegScene(t)

	req, f := s.register(s.withFlow(1, nil))
	answerRegisterWith(s, req, f, testIMPU, testTel)

	sub, sf := s.icscf.RecvRequest()
	o := answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	s.over(siptest.NewSocket(t, netip.AddrPortFrom(ueAddr, 0)), func() {
		req, f = s.register(s.withFlow(2, nil))
		answerRegisterWith(s, req, f, testIMPU, testTel)
	})

	contact := func(regID, state string) regevent.Contact {
		return regevent.Contact{
			ID: "c" + regID, State: state, Event: regevent.Registered, URI: "sip:ue@" + s.ue.Addr().String(), Expires: u32(600),
			UnknownParams: []regevent.UnknownParam{{Name: "+sip.instance", Value: `"<` + testInstance + `>"`}, {Name: "reg-id", Value: regID}},
		}
	}

	info := &regevent.Reginfo{Version: 1, State: "partial", Registrations: []regevent.Registration{{
		AOR: testIMPU, ID: "r1", State: regevent.Active,
		Contacts: []regevent.Contact{contact("1", regevent.Terminated), contact("2", regevent.Active)},
	}, {
		AOR: testTel, ID: "r2", State: regevent.Active,
		Contacts: []regevent.Contact{contact("1", regevent.Terminated), contact("2", regevent.Active)},
	}}}

	wantStatus(t, o.notify(t, "active;expires=600000", info), 200)

	if _, ok := s.p.regs.get(flowRegKey(1)); ok {
		t.Fatal("flow 1 still registered")
	}

	if _, ok := s.p.regs.get(flowRegKey(2)); !ok {
		t.Fatal("flow 2 ended with flow 1")
	}
}

// TS 33.203 §6.1 NOTE 2: each flow has its own security associations; a new flow leaves the others',
// while a new registration without the multiple registration mechanism replaces them.
func TestSecurityAssociationsPerFlow(t *testing.T) {
	a := newAssociations(IPsec{Kernel: ipsectest.NewKernel(), Grace: time.Minute}, slog.New(slog.DiscardHandler))
	t.Cleanup(a.close)

	set := func(portC uint16, id flowID, state saState) *saSet {
		return &saSet{
			impi: testIMPI, flow: id, state: state, inUse: true, initial: true, expires: time.Now().Add(time.Hour),
			set: ipsec.Set{
				Local:  ipsec.Endpoint{Addr: loopback, PortC: portC, PortS: 5100},
				Remote: ipsec.Endpoint{Addr: ueAddr, PortC: portC + 1000, PortS: portC + 2000},
			},
		}
	}

	one, two := flowID{testInstance, 1}, flowID{testInstance, 2}
	first, second, plain := set(5101, one, established), set(5102, two, temporary), set(5103, flowID{}, temporary)

	a.mu.Lock()
	a.add(first)
	a.add(second)
	a.mu.Unlock()

	a.registered(second, outcome{lifetime: time.Hour})

	if time.Until(first.expires) < 30*time.Minute {
		t.Fatalf("flow 1's set expires in %s after flow 2 registered", time.Until(first.expires))
	}

	for id, port := range map[flowID]uint16{one: 5101, two: 5102} {
		if f, ok := a.requestFlow(testIMPI, ueAddr, id, sip.UDP); !ok || f.Local.Port() != port {
			t.Errorf("requestFlow(%v) = %v, %v; want port %d", id, f, ok, port)
		}
	}

	a.mu.Lock()
	a.add(plain)
	a.mu.Unlock()

	a.registered(plain, outcome{lifetime: time.Hour})

	if time.Until(first.expires) > time.Minute {
		t.Fatalf("flow 1's set expires in %s after a registration without flows", time.Until(first.expires))
	}
}

func flowContact(base string, regID int) string {
	return base + `;+sip.instance="<` + testInstance + `>";reg-id=` + strconv.Itoa(regID)
}

// TS 24.229 §5.2.2.2 step 1, TS 33.203 §6.1 NOTE 2: a REGISTER for flow 2 over flow 1's security
// associations is not protected by its own; it is passed on unprotected, to be challenged.
func TestRegisterOverAnotherFlowsAssociations(t *testing.T) {
	s, u := newIPsecRegScene(t)
	flow := func(regID int) func(*sip.Request) {
		return func(r *sip.Request) { r.Header.Set("Contact", flowContact(ueContact(u), regID)) }
	}

	s.ue.Send(sip.UDP, s.pcscf, u.register(t, s.ue.Addr(), "", flow(1)))
	req, f, _ := s.forwarded()
	s.answer(req, f, 401)

	res, _ := s.ue.RecvResponse()
	wantStatus(t, res, 401)
	server := s.securityServer(res)

	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		flow(1)(r)
		r.Header.Add("Security-Verify", server.String())
	}))
	req, f, _ = s.forwarded()
	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	sub, sf := s.icscf.RecvRequest()
	answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	u2 := newUEAt(t, ueAddr, 9000)
	u.uc.Send(sip.UDP, s.ps, u.register(t, u.us.Addr(), "", func(r *sip.Request) {
		r.Header.Set("Contact", flowContact(ueContact(u2), 2))
		r.Header.Set("Security-Client", u2.securityClient())
	}))

	if _, _, integrity := s.forwarded(); integrity != "no" {
		t.Fatalf("integrity-protected=%q for flow 2 over flow 1's associations, want no", integrity)
	}
}

// TS 24.229 §5.2.5.1 step 1A: the 200 lists the other flows, at the same URI; the P-CSCF reads its flow's.
func TestFlowsSharingAURI(t *testing.T) {
	answer := func(s *regScene, req *sip.Request, f sip.Flow, expires map[int]string) {
		res := sip.NewResponse(req, 200, "")
		_ = res.Header.SetToTag(sip.NewTag())
		res.Header.Add("Service-Route", "<sip:orig@"+s.scscf.Addr().String()+";lr>")
		res.Header.Add("P-Associated-URI", "<"+testIMPU+">")

		for _, id := range []int{2, 1} {
			res.Header.Add("Contact", flowContact(s.contact(), id)+";expires="+expires[id])
		}

		s.icscf.Send(f.Transport, f.Remote, res)
		wantStatus(s.t, first(s.ue.RecvResponse()), 200)
	}

	t.Run("refresh", func(t *testing.T) {
		s := newRegScene(t)
		s.registerFlows()

		req, f := s.register(s.withFlow(1, func(r *sip.Request) { r.Header.Set("Expires", "60") }))
		answer(s, req, f, map[int]string{1: "60", 2: "3600"})

		if r, _ := s.p.regs.get(flowRegKey(1)); r.ExpiresAt.Sub(s.p.clock.Now()) > 10*time.Minute {
			t.Fatalf("flow 1 expires in %s, granted 60s", r.ExpiresAt.Sub(s.p.clock.Now()))
		}
	})

	t.Run("deregistration", func(t *testing.T) {
		s := newRegScene(t)
		s.registerFlows()

		req, f := s.register(s.withFlow(1, func(r *sip.Request) { r.Header.Set("Expires", "0") }))
		answer(s, req, f, map[int]string{1: "0", 2: "600"})

		if _, ok := s.p.regs.get(flowRegKey(1)); ok {
			t.Fatal("flow 1 still registered")
		}

		if _, ok := s.p.regs.get(flowRegKey(2)); !ok {
			t.Fatal("flow 2 deregistered")
		}
	})
}

// TS 33.203 §7.1 NOTE 10, §6.1 NOTE 2: flows of a UE share its protected server port; a new flow gets
// another client port of the P-CSCF rather than taking over another flow's associations, and is
// refused when none is left.
func TestNewFlowKeepsOtherFlowsAssociations(t *testing.T) {
	a := newAssociations(IPsec{Kernel: ipsectest.NewKernel(), Grace: time.Minute, ServerPort: 5100, ClientPorts: [2]uint16{5101, 5102}},
		slog.New(slog.DiscardHandler))
	t.Cleanup(a.close)

	newSet := func(regID int64, portC uint16, spi uint32) *saSet {
		return &saSet{
			impi: testIMPI, flow: flowID{testInstance, regID}, state: established, inUse: true, expires: time.Now().Add(time.Hour),
			set: ipsec.Set{
				Local:  ipsec.Endpoint{Addr: loopback, PortC: portC, PortS: 5100, SPIC: spi, SPIS: spi + 1},
				Remote: ipsec.Endpoint{Addr: ueAddr, PortC: 6000 + uint16(regID), PortS: 6001, SPIC: spi + 10, SPIS: spi + 11},
			},
		}
	}

	challengeFlow := func(regID int64) (*saSet, error) {
		_, err := a.challenged(challenge{
			impi: testIMPI, flow: flowID{testInstance, regID}, local: loopback, ue: ueAddr,
			offer: ipsec.Offer{
				Endpoint:  ipsec.Endpoint{PortC: 6100 + uint16(regID), PortS: 6001, SPIC: uint32(100 + 2*regID), SPIS: uint32(101 + 2*regID)},
				Integrity: "hmac-sha-1-96", Encryption: "null",
			},
		}, ipsec.Keys{CK: make([]byte, 16), IK: make([]byte, 16)})

		for s := range a.sets {
			if s.state == temporary {
				return s, err
			}
		}

		return nil, err
	}

	one := newSet(1, 5101, 1)

	a.mu.Lock()
	a.add(one)
	a.mu.Unlock()

	two, err := challengeFlow(2)
	if err != nil || one.removed || two == nil || two.set.Local.PortC != 5102 {
		t.Fatalf("challenge of flow 2: %v; flow 1's set removed %v; flow 2's %+v", err, one.removed, two)
	}

	two.state = established

	if _, err := challengeFlow(3); !errors.Is(err, errSAConflict) || one.removed || two.removed {
		t.Fatalf("challenge of flow 3: %v, want a conflict leaving the other flows' sets", err)
	}
}

// TS 24.229 §5.2.2.2: a challenge deletes the temporary sets toward the UE, whatever their flow.
func TestOneTemporarySetPerUE(t *testing.T) {
	a := newAssociations(IPsec{Kernel: ipsectest.NewKernel(), Grace: time.Minute, ServerPort: 5100, ClientPorts: [2]uint16{5101, 5102}},
		slog.New(slog.DiscardHandler))
	t.Cleanup(a.close)

	for i := range 20 {
		_, err := a.challenged(challenge{
			impi: testIMPI, flow: flowID{testInstance, int64(i + 1)}, local: loopback, ue: ueAddr,
			offer: ipsec.Offer{
				Endpoint:  ipsec.Endpoint{PortC: uint16(7000 + 2*i), PortS: uint16(7001 + 2*i), SPIC: uint32(100 + 2*i), SPIS: uint32(101 + 2*i)},
				Integrity: "hmac-sha-1-96", Encryption: "null",
			},
		}, ipsec.Keys{CK: make([]byte, 16), IK: make([]byte, 16)})
		if err != nil {
			t.Fatalf("challenge %d: %v", i, err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.sets) != 1 {
		t.Fatalf("%d sets, want the last challenge's only", len(a.sets))
	}
}

// RFC 5626 §4.2, §11.1: a flow needs a non-empty instance ID and a reg-id from 1 to 2^31 - 1, read as
// a number, like the S-CSCF's.
func TestContactFlow(t *testing.T) {
	for contact, want := range map[string]flowID{
		`<sip:ue@127.0.0.2>;+sip.instance="<urn:x>";reg-id=1`:  {"urn:x", 1},
		`<sip:ue@127.0.0.2>;+sip.instance="<urn:x>";reg-id=+1`: {"urn:x", 1},
		`<sip:ue@127.0.0.2>;+sip.instance="";reg-id=1`:         {},
		`<sip:ue@127.0.0.2>;+sip.instance;reg-id=1`:            {},
		`<sip:ue@127.0.0.2>;+sip.instance="<urn:x>";reg-id=0`:  {},
		`<sip:ue@127.0.0.2>;reg-id=1`:                          {},
	} {
		r := sip.NewRequest("REGISTER", sip.URI{Scheme: "sip", Host: "example.com"})
		r.Header.Set("Contact", contact)

		if got := registrationFlow(r); got != want {
			t.Errorf("%s: flow %+v, want %+v", contact, got, want)
		}
	}

	r := &db.PCSCFRegistration{Instance: "urn:x", RegID: 1}
	c := regevent.Contact{UnknownParams: []regevent.UnknownParam{{Name: "+sip.instance", Value: `"<urn:x>"`}, {Name: "reg-id", Value: "+1"}}}

	if !sameFlow(r, c) {
		t.Error("reg-id +1 in a NOTIFY not matched to flow 1")
	}
}

// RFC 5626 §3.1: each registration flow is a network flow of its own; a REGISTER for another flow over a
// registered one is refused, as the requests from it could not be told apart.
func TestFlowOverAnotherFlow(t *testing.T) {
	s := newRegScene(t)

	req, f := s.register(s.withFlow(1, nil))
	answerRegisterWith(s, req, f, testIMPU, testTel)

	sub, sf := s.icscf.RecvRequest()
	answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	s.cseq++

	r := siptest.NewRequest("REGISTER", "sip:"+homeDomain, sip.UDP, s.ue.Addr())
	r.Header.Set("To", "<sip:"+testIMPI+">")
	r.Header.Set("From", "<sip:"+testIMPI+">;tag="+sip.NewTag())
	r.Header.Set("Call-ID", s.callID)
	r.Header.Set("CSeq", strconv.Itoa(s.cseq)+" REGISTER")
	s.withFlow(2, nil)(r)
	s.ue.Send(sip.UDP, s.pcscf, r)

	wantStatus(t, first(s.ue.RecvResponse()), 403)
	s.icscf.RecvNone(quiet)
}
