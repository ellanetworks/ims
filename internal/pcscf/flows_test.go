package pcscf

import (
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
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

// registerFlows registers the UE's flows 1 and 2, and returns their flow tokens.
func (s *regScene) registerFlows() (string, string) {
	s.t.Helper()

	req, f := s.register(s.withFlow(1, nil))
	answerRegisterWith(s, req, f, testIMPU, testTel)

	sub, sf := s.icscf.RecvRequest()
	answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	req2, f2 := s.register(s.withFlow(2, nil))
	answerRegisterWith(s, req2, f2, testIMPU, testTel)

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

	req, f = s.register(s.withFlow(2, nil))
	answerRegisterWith(s, req, f, testIMPU, testTel)

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
