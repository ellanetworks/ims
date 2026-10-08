package pcscf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/internal/rxpolicy"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

var (
	imsIdentity  = diameter.Identity{OriginHost: "ims." + homeDomain, OriginRealm: homeDomain}
	pcrfIdentity = diameter.Identity{OriginHost: "pcrf.epc.test", OriginRealm: "epc.test"}
)

type fakePCRF struct {
	t    *testing.T
	seq  atomic.Int64
	reqs chan *diameter.Message

	mu     sync.Mutex
	answer func(ctx context.Context, req *diameter.Message) (*diameter.Message, error)
}

func newFakePCRF(t *testing.T) *fakePCRF {
	return &fakePCRF{t: t, reqs: make(chan *diameter.Message, 64)}
}

func (f *fakePCRF) backend() *rxpolicy.Backend {
	return rxpolicy.New(rxpolicy.Config{
		Diameter: f, PCRF: rxpolicy.PCRF{ID: "pcrf", Host: pcrfIdentity.OriginHost, Realm: pcrfIdentity.OriginRealm},
	})
}

func (f *fakePCRF) config(timeout time.Duration) func(*Config) {
	return func(c *Config) {
		c.Policy = Policy{Backend: f.backend(), Timeout: timeout}
	}
}

// rxReAuth delivers an RAR as the Diameter server would, through the Rx backend.
func (p *PCSCF) rxReAuth(session string, r rx.ReAuthRequest) bool {
	b, ok := p.cfg.Policy.Backend.(*rxpolicy.Backend)
	if !ok {
		return p.Notify(session, policy.Event{})
	}

	return b.ReAuth(session, r)
}

// rxAbortSession delivers an ASR as the Diameter server would, through the Rx backend.
func (p *PCSCF) rxAbortSession(session string, r rx.AbortSessionRequest) (func(), bool) {
	b, ok := p.cfg.Policy.Backend.(*rxpolicy.Backend)
	if !ok {
		return p.Abort(session, policy.Abort{})
	}

	return b.AbortSession(session, r)
}

func refClass(r db.PCSCFRegistration) [][]byte {
	var class [][]byte
	if r.Policy.Ref != "" {
		_ = json.Unmarshal([]byte(r.Policy.Ref), &class)
	}

	return class
}

func (f *fakePCRF) Identity() diameter.Identity { return imsIdentity }

func (f *fakePCRF) NewSessionID() string {
	return fmt.Sprintf("%s;1;%d", imsIdentity.OriginHost, f.seq.Add(1))
}

func (f *fakePCRF) Do(ctx context.Context, peerID string, req *diameter.Message, _ ...diameter.DoOption) (*diameter.Message, error) {
	if peerID != "pcrf" && peerID != "pcrf-1" {
		return nil, diameter.ErrUnknownPeer
	}

	f.reqs <- req

	f.mu.Lock()

	answer := f.answer
	f.mu.Unlock()

	if answer == nil {
		return succeed(req)
	}

	return answer(ctx, req)
}

func (f *fakePCRF) answerWith(a func(ctx context.Context, req *diameter.Message) (*diameter.Message, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.answer = a
}

func (f *fakePCRF) holdAA() (release func()) {
	ch := make(chan struct{})

	f.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandAA {
			select {
			case <-ch:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		return succeed(req)
	})

	var once sync.Once

	return func() { once.Do(func() { close(ch) }) }
}

func succeed(req *diameter.Message) (*diameter.Message, error) {
	switch req.CommandCode {
	case rx.CommandAA:
		return rx.NewAAAnswer(req, pcrfIdentity, rx.AAAnswer{})
	case rx.CommandSessionTermination:
		return rx.NewSessionTerminationAnswer(req, pcrfIdentity, rx.SessionTerminationAnswer{})
	}

	return nil, fmt.Errorf("unexpected command %d", req.CommandCode)
}

func (f *fakePCRF) next() *diameter.Message {
	f.t.Helper()

	select {
	case m := <-f.reqs:
		return m
	case <-time.After(5 * time.Second):
		f.t.Fatal("no Rx request")
		return nil
	}
}

func (f *fakePCRF) none() {
	f.t.Helper()

	select {
	case m := <-f.reqs:
		f.t.Fatalf("unexpected Rx request %d for %s", m.CommandCode, tgpp.ParseEnvelope(m).SessionID)
	case <-time.After(quiet):
	}
}

func (f *fakePCRF) aar() (string, rx.AARequest) {
	f.t.Helper()

	m := f.next()
	if m.CommandCode != rx.CommandAA {
		f.t.Fatalf("Rx request %d, want an AAR", m.CommandCode)
	}

	r, err := rx.ParseAARequest(m)
	if err != nil {
		f.t.Fatalf("ParseAARequest: %v", err)
	}

	return tgpp.ParseEnvelope(m).SessionID, r
}

func (f *fakePCRF) str() (string, rx.TerminationCause) {
	f.t.Helper()

	m := f.next()
	if m.CommandCode != rx.CommandSessionTermination {
		f.t.Fatalf("Rx request %d, want an STR", m.CommandCode)
	}

	r, err := rx.ParseSessionTerminationRequest(m)
	if err != nil {
		f.t.Fatalf("ParseSessionTerminationRequest: %v", err)
	}

	return tgpp.ParseEnvelope(m).SessionID, r.Cause
}

func (f *fakePCRF) wantSTR(session string, cause rx.TerminationCause) {
	f.t.Helper()

	if id, got := f.str(); id != session || got != cause {
		f.t.Fatalf("STR %s %s, want %s %s", id, got, session, cause)
	}
}

func newRxScene(t *testing.T, timeout time.Duration) (*regScene, *fakePCRF) {
	t.Helper()

	f := newFakePCRF(t)

	return newRegScene(t, f.config(timeout)), f
}

func (s *regScene) record() (db.PCSCFRegistration, bool) {
	return s.p.regs.get(regKey{impi: testIMPI, ue: ueAddr})
}

func (s *regScene) wantSession(id string) {
	s.t.Helper()

	eventually(s.t, "the Rx session "+id+" on the stored record", func() bool {
		regs, err := s.store.ListPCSCFRegistrations(context.Background())
		return err == nil && len(regs) == 1 && regs[0].Policy.ID == id
	})
}

func (s *regScene) reregister(expires int) {
	s.t.Helper()

	req, f := s.register(func(r *sip.Request) { r.Header.Set("Expires", strconv.Itoa(expires)) })
	answerRegister(s.icscf, s.scscf.Addr(), req, f, expires)
	wantStatus(s.t, first(s.ue.RecvResponse()), 200)
}

func TestRxSessionOnInitialRegistration(t *testing.T) {
	s, pcrf := newRxScene(t, 0)
	release := pcrf.holdAA()
	t.Cleanup(release)

	req, f := s.register(nil)

	pcrf.none()

	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)

	wantStatus(t, first(s.ue.RecvResponse()), 200)

	m := pcrf.next()
	if m.CommandCode != rx.CommandAA {
		t.Fatalf("Rx request %d, want an AAR", m.CommandCode)
	}

	env := tgpp.ParseEnvelope(m)
	if env.DestinationHost != pcrfIdentity.OriginHost || env.DestinationRealm != pcrfIdentity.OriginRealm ||
		env.Origin.OriginHost != imsIdentity.OriginHost {
		t.Errorf("envelope = %+v, want from the IMS to the PCRF", env)
	}

	if a, ok := m.Find(rx.AVPRxRequestType, tgpp.VendorID); !ok || a.Flags&diameter.AVPFlagMandatory != 0 {
		t.Errorf("Rx-Request-Type = %+v, %v; want it with the M bit cleared", a, ok)
	}

	got, err := rx.ParseAARequest(m)
	if err != nil {
		t.Fatalf("ParseAARequest: %v", err)
	}

	initial, control, signalling := rx.RequestInitial, rx.MediaControl, rx.FlowUsageAFSignalling
	want := rx.AARequest{
		MediaComponents: []rx.MediaComponent{{
			Number:        0,
			Type:          &control,
			SubComponents: []rx.MediaSubComponent{{FlowNumber: 0, FlowUsage: &signalling}},
		}},
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer},
		FramedIPAddress: ueAddr,
		RequestType:     &initial,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AAR = %+v, want %+v", got, want)
	}

	if r, ok := s.record(); !ok || r.Policy.ID != env.SessionID {
		t.Fatalf("record = %+v, want the pending session %s", r, env.SessionID)
	}

	release()
	s.wantSession(env.SessionID)

	if sub, _ := s.icscf.RecvRequest(); sub.Method != "SUBSCRIBE" {
		t.Fatalf("I-CSCF got %s, want the P-CSCF's SUBSCRIBE", sub.Method)
	}
}

func TestRxAARForIPv6(t *testing.T) {
	pcrf := newFakePCRF(t)
	b := pcrf.backend()

	ue := netip.MustParseAddr("2001:db8::1")

	if _, err := b.OpenSignalling(t.Context(), b.NewSessionID(), policy.Signalling{UE: ue}, false); err != nil {
		t.Fatalf("OpenSignalling: %v", err)
	}

	m := pcrf.next()

	r, err := rx.ParseAARequest(m)
	if err != nil || r.FramedIPv6Address != ue || r.FramedIPAddress.IsValid() {
		t.Fatalf("AAR = %+v, %v; want Framed-IPv6-Prefix %s only", r, err, ue)
	}

	if a, ok := m.Find(diameter.AVPFramedIPv6Prefix, 0); !ok || len(a.Data) != 18 || a.Data[1] != 128 {
		t.Fatalf("Framed-IPv6-Prefix = %x, want a /128", a.Data)
	}
}

func TestRxReRegistrationKeepsTheSession(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.reregister(600)
	pcrf.none()
	s.wantSession(id)
}

func TestRxReAuthenticationKeepsTheSession(t *testing.T) {
	pcrf := newFakePCRF(t)
	s, u := newIPsecRegScene(t, pcrf.config(0))
	s.registerOverIPsec(u)

	id, r := pcrf.aar()
	if r.FramedIPAddress != ueAddr {
		t.Fatalf("Framed-IP-Address = %s, want %s", r.FramedIPAddress, ueAddr)
	}

	next := u.rekeyed(t)
	next.uc = siptest.NewSocket(t, netip.AddrPortFrom(ueAddr, 0))

	u.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) { r.Header.Set("Contact", ueContact(u)) }))

	req, f, _ := s.forwarded()
	s.answer(req, f, 401)

	res, _ := u.us.RecvResponse()
	wantStatus(t, res, 401)

	next.uc.Send(sip.UDP, s.ps, next.register(t, u.us.Addr(), "c4c4", func(r *sip.Request) {
		r.Header.Add("Security-Verify", s.securityServer(res).String())
		r.Header.Set("Contact", ueContact(u))
	}))

	req, f, _ = s.forwarded()
	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	pcrf.none()

	if reg, ok := s.p.regs.get(regKey{impi: testIMPI, ue: ueAddr}); !ok || reg.Policy.ID != id {
		t.Fatalf("record = %+v, want the session %s kept", reg, id)
	}
}

func TestRxPartialDeregistrationKeepsTheSession(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	const other = "sip:other@" + homeDomain

	req, f := s.register(nil)
	answerRegisterWith(s, req, f, testIMPU, testTel)

	sub, sf := s.icscf.RecvRequest()
	answerSubscribe(s.icscf, s.scscf, sub, sf, 600000)

	id, _ := pcrf.aar()

	setTo := func(r *sip.Request) {
		r.Header.Set("To", "<"+other+">")
		r.Header.Set("Authorization", `Digest username="`+testIMPI+`", realm="`+homeDomain+`", uri="sip:`+homeDomain+
			`", nonce="", response=""`)
	}

	req, f = s.register(setTo)
	answerRegisterWith(s, req, f, other)

	req, f = s.register(func(r *sip.Request) {
		setTo(r)
		r.Header.Set("Expires", "0")
	})
	answerRegisterWith(s, req, f)

	pcrf.none()
	s.wantSession(id)
}

func TestRxRefusedAAAKeepsTheRegistration(t *testing.T) {
	refusals := map[string]struct {
		answer func(ctx context.Context, req *diameter.Message) (*diameter.Message, error)
		str    rx.TerminationCause
	}{
		"refused": {answer: func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
			if req.CommandCode != rx.CommandAA {
				return succeed(req)
			}

			return rx.NewAAErrorAnswer(req, pcrfIdentity, rx.AAError{ResultError: rx.ResultError{Result: tgpp.Result{
				Code: tgpp.ResultRequestedServiceNotAuthorized, Experimental: true, VendorID: tgpp.VendorID,
			}}})
		}},
		"not negotiated": {answer: func(context.Context, *diameter.Message) (*diameter.Message, error) {
			return nil, diameter.ErrApplicationUnsupported
		}},
		"connection lost": {
			answer: func(context.Context, *diameter.Message) (*diameter.Message, error) {
				return nil, diameter.ErrNotConnected
			},
			str: rx.TerminationAdministrative,
		},
		"malformed answer": {
			answer: func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
				ans, err := succeed(req)
				if err == nil && req.CommandCode == rx.CommandAA {
					ans.AVPs = slices.DeleteFunc(ans.AVPs, func(a diameter.AVP) bool { return a.Code == diameter.AVPResultCode })
				}

				return ans, err
			},
			str: rx.TerminationBadAnswer,
		},
	}

	for name, refusal := range refusals {
		t.Run(name, func(t *testing.T) {
			pcrf := newFakePCRF(t)
			s := newRegScene(t, pcrf.config(0), slowRetry)
			pcrf.answerWith(refusal.answer)

			s.registered(600)

			id, _ := pcrf.aar()

			s.wantSession("")

			if refusal.str != 0 {
				pcrf.wantSTR(id, refusal.str)
			}

			pcrf.none()

			if _, ok := s.record(); !ok {
				t.Fatal("registration removed")
			}

			pcrf.answerWith(nil)
			s.reregister(600)

			again, _ := pcrf.aar()
			s.wantSession(again)
		})
	}
}

func TestRxAARTimeoutEndsTheSession(t *testing.T) {
	s, pcrf := newRxScene(t, 100*time.Millisecond)
	pcrf.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandAA {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		return succeed(req)
	})

	s.registered(600)

	id, _ := pcrf.aar()
	pcrf.wantSTR(id, rx.TerminationAdministrative)
	s.wantSession("")

	pcrf.answerWith(nil)
	s.reregister(600)

	again, _ := pcrf.aar()
	if again == id {
		t.Fatal("the retry reused the timed out session")
	}

	s.wantSession(again)
}

func TestRxSTROnUEDeregistration(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.reregister(0)
	pcrf.wantSTR(id, rx.TerminationLogout)

	eventually(t, "the session to be forgotten after its STA", func() bool {
		_, ok := s.p.policy.lookup(id)
		return !ok
	})
}

func TestRxSTROnNetworkDeregistration(t *testing.T) {
	pcrf := newFakePCRF(t)
	s, u := newIPsecRegScene(t, pcrf.config(0))
	_, o := s.registerOverIPsec(u)

	id, _ := pcrf.aar()

	info := reginfo(0, regevent.Terminated, regevent.Terminated,
		map[string]string{"sip:ue@" + u.us.Addr().String(): regevent.Terminated})

	for i := range info.Registrations {
		info.Registrations[i].Contacts[0].Event = regevent.Deactivated
	}

	wantStatus(t, o.notify(t, "terminated;reason=deactivated", info), 200)

	pcrf.wantSTR(id, rx.TerminationAdministrative)
}

func TestRxSTROnLocalExpiry(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.clock.Advance(599 * time.Second)
	pcrf.none()

	s.clock.Advance(time.Second)
	pcrf.wantSTR(id, rx.TerminationAuthExpired)

	if regs := s.p.regs.all(); len(regs) != 0 {
		t.Fatalf("registrations = %+v, want the expired one removed", regs)
	}

	if regs, _ := s.store.ListPCSCFRegistrations(context.Background()); len(regs) != 0 {
		t.Fatalf("stored = %+v, want the expired registration deleted", regs)
	}
}

func TestRxSTRForRegistrationsExpiredAtRestart(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.p.Close()
	s.clock.Advance(600 * time.Second)
	pcrf.none()

	s.restart()
	pcrf.wantSTR(id, rx.TerminationAuthExpired)
}

func TestRxSTRWaitsForTheAAA(t *testing.T) {
	s, pcrf := newRxScene(t, 0)
	release := pcrf.holdAA()
	t.Cleanup(release)

	s.registered(600)

	id, _ := pcrf.aar()

	s.reregister(0)
	pcrf.none()

	release()
	pcrf.wantSTR(id, rx.TerminationLogout)
}

func TestRxUnknownSessionAtTheSTA(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		return rx.NewAnswer(req, pcrfIdentity, tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0), nil
	})

	s.reregister(0)
	pcrf.wantSTR(id, rx.TerminationLogout)

	eventually(t, "the session to be forgotten", func() bool {
		_, ok := s.p.policy.lookup(id)
		return !ok
	})
}

func (s *regScene) terminating(path sip.URI) {
	s.t.Helper()

	r := siptest.NewRequest("MESSAGE", "sip:ue@"+s.ue.Addr().String(), sip.UDP, s.scscf.Addr())
	r.Header.Set("To", "<"+testIMPU+">")
	r.Header.Add("Route", "<"+path.String()+">")
	s.scscf.Send(sip.UDP, s.pcscf, r)
}

func (s *regScene) wantSignallingLost(lost bool) {
	s.t.Helper()

	eventually(s.t, fmt.Sprintf("signalling lost = %v", lost), func() bool {
		r, ok := s.record()
		if !ok || r.SignallingLost != lost {
			return false
		}

		regs, err := s.store.ListPCSCFRegistrations(context.Background())

		return err == nil && len(regs) == 1 && regs[0].SignallingLost == lost
	})
}

func TestRxReAuthMarksTheSignallingLost(t *testing.T) {
	// TS 29.514 §4.2.5.10 reports signalling path loss as FAILED_RESOURCES_ALLOCATION.
	for _, action := range []rx.SpecificAction{
		rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer, rx.ActionIndicationOfFailedResourcesAllocation,
	} {
		t.Run(action.String(), func(t *testing.T) {
			s, pcrf := newRxScene(t, 0)

			path, _, _ := s.registered(600)
			id, _ := pcrf.aar()

			if !s.p.rxReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{action}}) {
				t.Fatal("ReAuth reported an unknown session")
			}

			s.wantSignallingLost(true)

			s.terminating(path)
			wantStatus(t, first(s.scscf.RecvResponse()), 500)

			if r, _ := s.record(); r.Policy.ID != id {
				t.Fatalf("session = %q, want %s kept", r.Policy.ID, id)
			}

			pcrf.none()

			s.reregister(600)
			s.wantSignallingLost(false)

			s.terminating(path)

			if req, _ := s.ue.RecvRequest(); req.Method != "MESSAGE" {
				t.Fatalf("UE got %s, want the MESSAGE", req.Method)
			}
		})
	}
}

func TestRxReAuthWithOtherActions(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()

	if !s.p.rxReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfRecoveryOfBearer}}) {
		t.Fatal("ReAuth reported an unknown session")
	}

	if r, _ := s.record(); r.SignallingLost {
		t.Fatal("signalling lost set by another action")
	}

	if s.p.rxReAuth("ims.test;9;9", rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}) {
		t.Fatal("ReAuth accepted an unknown session")
	}
}

func TestRxRequestFromTheUERestoresTheSignalling(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.p.rxReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer}})

	s.wantSignallingLost(true)

	s.ue.Send(sip.UDP, s.pcscf, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, s.ue.Addr()))
	s.scscf.RecvRequest()

	s.wantSignallingLost(false)
}

func TestRxRequestOnTheSAsRestoresTheSignalling(t *testing.T) {
	pcrf := newFakePCRF(t)
	s, u := newIPsecRegScene(t, pcrf.config(0))
	s.registerOverIPsec(u)

	id, _ := pcrf.aar()
	s.p.rxReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}})

	if r, _ := s.p.regs.get(regKey{impi: testIMPI, ue: ueAddr}); !r.SignallingLost {
		t.Fatal("signalling lost not set")
	}

	u.uc.Send(sip.UDP, s.ps, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, u.us.Addr()))
	s.scscf.RecvRequest()

	if r, _ := s.p.regs.get(regKey{impi: testIMPI, ue: ueAddr}); r.SignallingLost {
		t.Fatal("signalling lost kept after a request on the UE's security associations")
	}
}

func TestRxAbortSession(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	terminate, ok := s.p.rxAbortSession(id, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if !ok {
		t.Fatal("AbortSession reported an unknown session")
	}

	s.wantSession("")
	s.wantSignallingLost(true)

	pcrf.none()

	terminate()
	pcrf.wantSTR(id, rx.TerminationAdministrative)

	eventually(t, "the session to be forgotten after its STA", func() bool {
		_, ok := s.p.policy.lookup(id)
		return !ok
	})

	if _, ok := s.p.rxAbortSession(id, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased}); ok {
		t.Fatal("AbortSession accepted the ended session")
	}

	s.reregister(600)
	s.wantSignallingLost(false)

	again, _ := pcrf.aar()
	s.wantSession(again)
}

// An abort during shutdown could not be followed by its termination: it is refused, and the session stays stored
// for the restart to end.
func TestAbortSessionAfterClose(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.p.Close()

	if _, ok := s.p.rxAbortSession(id, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased}); ok {
		t.Fatal("AbortSession accepted after Close")
	}

	s.wantSession(id)
	pcrf.none()
}

func TestRxAbortSessionWhileTheAAAIsOutstanding(t *testing.T) {
	s, pcrf := newRxScene(t, 0)
	release := pcrf.holdAA()
	t.Cleanup(release)

	s.registered(600)

	id, _ := pcrf.aar()

	terminate, ok := s.p.rxAbortSession(id, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if !ok {
		t.Fatal("AbortSession did not find the pending session")
	}

	terminate()
	pcrf.none()

	release()
	pcrf.wantSTR(id, rx.TerminationAdministrative)

	if _, ok := s.record(); !ok {
		t.Fatal("registration removed by the ASR")
	}
}

func TestRxSessionsReestablishedAfterRestart(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.restart()

	pcrf.wantSTR(id, rx.TerminationAdministrative)

	again, r := pcrf.aar()
	if again == id || r.FramedIPAddress != ueAddr {
		t.Fatalf("AAR %s for %s after the restart, want a new session for %s", again, r.FramedIPAddress, ueAddr)
	}

	s.wantSession(again)

	if s.p.rxReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}) {
		t.Fatal("the old session still known")
	}

	if !s.p.rxReAuth(again, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}) {
		t.Fatal("the new session unknown")
	}

	s.wantSignallingLost(true)
	pcrf.none()
}

func TestRxSessionPendingAtShutdownEndedAfterRestart(t *testing.T) {
	s, pcrf := newRxScene(t, 200*time.Millisecond)
	release := pcrf.holdAA()
	t.Cleanup(release)

	s.registered(600)

	id, _ := pcrf.aar()

	s.restart()
	pcrf.answerWith(nil)

	pcrf.wantSTR(id, rx.TerminationAdministrative)

	again, _ := pcrf.aar()
	s.wantSession(again)
}

func TestRxNoSessionForEmergencyRegistrations(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	req, f := s.register(func(r *sip.Request) { r.Header.Set("Contact", "<sip:ue@"+s.ue.Addr().String()+";sos>") })
	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(t, first(s.ue.RecvResponse()), 200)

	pcrf.none()

	if r, ok := s.record(); !ok || r.Policy.ID != "" {
		t.Fatalf("record = %+v, %v; want an emergency registration without an Rx session", r, ok)
	}
}

func TestRxSessionForARegistrationBesideAnEmergencyContact(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	req, f := s.register(nil)

	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Add("Service-Route", "<sip:orig@"+s.scscf.Addr().String()+";lr>")
	res.Header.Add("P-Associated-URI", "<"+testIMPU+">")
	res.Header.Add("Contact", s.contact()+";expires=600")
	res.Header.Add("Contact", "<sip:sos@"+s.ue.Addr().String()+";sos>;expires=600")
	s.icscf.Send(f.Transport, f.Remote, res)
	wantStatus(t, first(s.ue.RecvResponse()), 200)

	id, _ := pcrf.aar()
	s.wantSession(id)
}

func TestRxSignallingLostDuringARegistration(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()

	s.p.rxReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer}})
	s.wantSignallingLost(true)

	req, f := s.register(nil)
	s.wantSignallingLost(false)

	s.p.rxReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}})
	s.wantSignallingLost(true)

	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)
	wantStatus(t, first(s.ue.RecvResponse()), 200)

	s.wantSignallingLost(true)
}

func TestRxSnapshotOfARemovedRecordHasNoSession(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	snapshot, _ := s.record()

	s.clock.Advance(600 * time.Second)
	pcrf.wantSTR(id, rx.TerminationAuthExpired)

	s.p.regs.save(snapshot)

	if r, _ := s.record(); r.Policy.ID != "" {
		t.Fatalf("session = %q, want the ended session not brought back", r.Policy.ID)
	}

	s.reregister(600)

	again, _ := pcrf.aar()
	s.wantSession(again)
}

func (f *fakePCRF) classAA(class [][]byte) <-chan [][]byte {
	strs := make(chan [][]byte, 8)

	f.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		switch req.CommandCode {
		case rx.CommandAA:
			return rx.NewAAAnswer(req, pcrfIdentity, rx.AAAnswer{Class: class})
		case rx.CommandSessionTermination:
			str, err := rx.ParseSessionTerminationRequest(req)
			if err != nil {
				return nil, err
			}

			strs <- str.Class
		}

		return succeed(req)
	})

	return strs
}

func TestRxClassEchoedInTheSTR(t *testing.T) {
	class := [][]byte{[]byte("pcrf-state"), {0xff, 0x00}}

	s, pcrf := newRxScene(t, 0)
	strs := pcrf.classAA(class)

	s.registered(600)

	id, _ := pcrf.aar()

	eventually(t, "the Class on the stored record", func() bool {
		regs, err := s.store.ListPCSCFRegistrations(context.Background())
		return err == nil && len(regs) == 1 && regs[0].Policy.ID == id && reflect.DeepEqual(refClass(regs[0]), class)
	})

	s.reregister(0)
	pcrf.wantSTR(id, rx.TerminationLogout)

	if got := <-strs; !reflect.DeepEqual(got, class) {
		t.Fatalf("STR Class = %q, want the AAA's %q", got, class)
	}
}

func TestRxClassSurvivesRestart(t *testing.T) {
	class := [][]byte{[]byte("pcrf-state")}

	s, pcrf := newRxScene(t, 0)
	strs := pcrf.classAA(class)

	s.registered(600)

	id, _ := pcrf.aar()

	eventually(t, "the Class on the record", func() bool {
		r, ok := s.record()
		return ok && reflect.DeepEqual(refClass(r), class)
	})

	s.restart()
	pcrf.wantSTR(id, rx.TerminationAdministrative)

	if got := <-strs; !reflect.DeepEqual(got, class) {
		t.Fatalf("STR Class after the restart = %q, want %q", got, class)
	}
}

// A session stored for another policy function is not terminated here: it is dropped and opened again.
func TestRestartReopensTheSessionOfAnotherEndpoint(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	r, _ := s.record()
	r.Policy.Endpoint = "pcf"

	if _, err := s.store.SavePCSCFRegistration(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	s.restart()

	again, _ := pcrf.aar()
	if again == id {
		t.Fatalf("AAR for %s, want a new session", again)
	}

	s.wantSession(again)

	if r, _ := s.record(); r.Policy.Endpoint != "rx:"+pcrfIdentity.OriginHost {
		t.Fatalf("endpoint = %q, want the PCRF", r.Policy.Endpoint)
	}

	pcrf.none()
}

// The endpoint is the PCRF, not the local name of its peer: renaming the peer keeps the stored sessions.
func TestRestartWithARenamedPeerTerminatesTheSession(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	s.p.cfg.Policy.Backend = rxpolicy.New(rxpolicy.Config{
		Diameter: pcrf, PCRF: rxpolicy.PCRF{ID: "pcrf-1", Host: pcrfIdentity.OriginHost, Realm: pcrfIdentity.OriginRealm},
	})

	s.restart()

	pcrf.wantSTR(id, rx.TerminationAdministrative)

	again, _ := pcrf.aar()
	s.wantSession(again)
}

func TestRxShutdownLetsTheSTRFinish(t *testing.T) {
	s, pcrf := newRxScene(t, time.Second)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	release := make(chan struct{})
	result := make(chan error, 1)

	pcrf.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		<-release

		result <- ctx.Err()

		return succeed(req)
	})

	s.reregister(0)
	pcrf.wantSTR(id, rx.TerminationLogout)

	closed := make(chan struct{})

	go func() {
		s.p.Close()
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close returned with the STR in flight")
	case <-time.After(quiet):
	}

	close(release)

	if err := <-result; err != nil {
		t.Fatalf("STR context = %v, want it alive until answered", err)
	}

	<-closed
}

func TestSourceIndexFollowsTheRecords(t *testing.T) {
	rs := newRegistrations(nil, fakeClock{siptest.NewClock()}, time.Minute, slog.New(slog.DiscardHandler))

	a := netip.AddrPortFrom(ueAddr, 5060)
	b := netip.AddrPortFrom(ueAddr, 5070)
	r := db.PCSCFRegistration{
		IMPI: testIMPI, FlowToken: "t", UEAddress: a, PCSCFAddress: loopback, ExpiresAt: testEpoch.Add(time.Hour),
	}

	rs.save(r)

	if k, ok := rs.sourceKey(a); !ok || k != (regKey{impi: testIMPI, ue: ueAddr}) {
		t.Fatalf("sourceKey(%s) = %v, %v", a, k, ok)
	}

	r.UEAddress = b
	rs.save(r)

	if _, ok := rs.sourceKey(a); ok {
		t.Fatal("the old port still indexed after the record moved")
	}

	if _, ok := rs.sourceKey(b); !ok {
		t.Fatal("the new port not indexed")
	}

	r.Protected = true
	rs.save(r)

	if _, ok := rs.sourceKey(b); ok {
		t.Fatal("a protected record indexed by its source")
	}

	r.Protected = false
	rs.save(r)
	rs.remove(regKey{impi: testIMPI, ue: ueAddr})

	if _, ok := rs.sourceKey(b); ok || len(rs.bySource) != 0 {
		t.Fatalf("index = %v after the removal", rs.bySource)
	}
}

func TestRxShutdownDuringTheRestoreKeepsTheSession(t *testing.T) {
	s, pcrf := newRxScene(t, 200*time.Millisecond)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	var held atomic.Bool

	pcrf.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandSessionTermination && held.CompareAndSwap(false, true) {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		return succeed(req)
	})

	s.restart()
	pcrf.wantSTR(id, rx.TerminationAdministrative)

	s.restart()

	pcrf.wantSTR(id, rx.TerminationAdministrative)

	again, _ := pcrf.aar()
	s.wantSession(again)
}

func TestTerminationCauseOfMixedEvents(t *testing.T) {
	for _, events := range [][]regevent.Event{
		{regevent.Unregistered, regevent.Deactivated},
		{regevent.Deactivated, regevent.Unregistered},
		{regevent.Expired, regevent.Rejected, regevent.Unregistered},
	} {
		var e regevent.Event
		for _, ev := range events {
			e = graver(e, ev)
		}

		if c := terminationCause(e); c != policy.TerminationAdministrative {
			t.Errorf("%v: cause %s, want administrative", events, c)
		}
	}

	if c := terminationCause(graver(graver("", regevent.Expired), regevent.Unregistered)); c != policy.TerminationLogout {
		t.Errorf("expired and unregistered: cause %s, want logout", c)
	}
}

func TestNoRxWithoutAPCRF(t *testing.T) {
	s := newRegScene(t)

	s.registered(600)

	if r, _ := s.record(); r.Policy.ID != "" {
		t.Fatalf("session = %q without a PCRF", r.Policy.ID)
	}

	if _, known := s.p.rxAbortSession("ims.test;1;1", rx.AbortSessionRequest{}); known || s.p.rxReAuth("ims.test;1;1", rx.ReAuthRequest{}) {
		t.Fatal("a session found without a PCRF")
	}
}

func slowRetry(c *Config) { c.Policy.TerminationRetry = time.Hour }

func fastRetry(c *Config) { c.Policy.TerminationRetry = 20 * time.Millisecond }

func failSTRs(n int64) func(context.Context, *diameter.Message) (*diameter.Message, error) {
	var failed atomic.Int64

	return func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandSessionTermination && failed.Add(1) <= n {
			return nil, diameter.ErrNotConnected
		}

		return succeed(req)
	}
}

func TestRxSTRRetriedUntilAnswered(t *testing.T) {
	pcrf := newFakePCRF(t)
	s := newRegScene(t, pcrf.config(100*time.Millisecond), fastRetry)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	pcrf.answerWith(failSTRs(2))
	s.reregister(0)

	for range 3 {
		pcrf.wantSTR(id, rx.TerminationLogout)
	}

	pcrf.none()
}

// overloadOnce fails the first termination as an overloaded policy function does (TS 29.500 §6.4.2).
type overloadOnce struct {
	policy.Backend
	backoff time.Duration
	failed  atomic.Bool
}

func (b *overloadOnce) Terminate(ctx context.Context, id, ref string, cause policy.Termination, wait bool) error {
	if !b.failed.Swap(true) {
		return &policy.Error{Kind: policy.ErrRefused, Transient: true, Backoff: b.backoff, Err: errors.New("overloaded")}
	}

	return b.Backend.Terminate(ctx, id, ref, cause, wait)
}

func TestSTRWaitsForTheBackoff(t *testing.T) {
	const backoff = 300 * time.Millisecond

	pcrf := newFakePCRF(t)
	s := newRegScene(t, pcrf.config(100*time.Millisecond), fastRetry, func(c *Config) {
		c.Policy.Backend = &overloadOnce{Backend: c.Policy.Backend, backoff: backoff}
	})

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	start := time.Now()

	s.reregister(0)
	pcrf.wantSTR(id, rx.TerminationLogout)

	if d := time.Since(start); d < backoff {
		t.Fatalf("STR retried after %s, want at least the %s backoff", d, backoff)
	}

	pcrf.none()
}

func TestRxRestoreKeepsTheSessionUntilTheSTA(t *testing.T) {
	pcrf := newFakePCRF(t)
	s := newRegScene(t, pcrf.config(100*time.Millisecond), fastRetry)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	release := make(chan struct{})

	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	pcrf.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandSessionTermination {
			select {
			case <-release:
			default:
				return nil, diameter.ErrNotConnected
			}
		}

		return succeed(req)
	})

	s.restart()

	pcrf.wantSTR(id, rx.TerminationAdministrative)
	pcrf.wantSTR(id, rx.TerminationAdministrative)
	s.wantSession(id)

	close(release)

	for {
		m := pcrf.next()
		if m.CommandCode == rx.CommandSessionTermination {
			continue
		}

		if m.CommandCode != rx.CommandAA {
			t.Fatalf("Rx request %d, want an AAR", m.CommandCode)
		}

		again := tgpp.ParseEnvelope(m).SessionID
		if again == id {
			t.Fatal("the AAR reused the old session")
		}

		s.wantSession(again)

		return
	}
}

func TestRxShutdownStopsTheSTRRetries(t *testing.T) {
	s, pcrf := newRxScene(t, 100*time.Millisecond)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	pcrf.answerWith(failSTRs(1 << 30))
	s.reregister(0)
	pcrf.wantSTR(id, rx.TerminationLogout)

	closed := make(chan struct{})

	go func() {
		s.p.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked by the STR retries")
	}
}

// RFC 6733 §8.16: the P-CSCF keeps its sessions across a restart, so its requests carry no Origin-State-Id.
func TestRxWithoutOriginStateID(t *testing.T) {
	pcrf := newFakePCRF(t)
	s := newRegScene(t, pcrf.config(0))

	noState := func(m *diameter.Message) {
		t.Helper()

		if _, ok := m.Find(diameter.AVPOriginStateID, 0); ok {
			t.Fatalf("Rx request %d with an Origin-State-Id", m.CommandCode)
		}
	}

	s.registered(600)

	aar := pcrf.next()
	noState(aar)
	s.wantSession(tgpp.ParseEnvelope(aar).SessionID)

	s.reregister(0)
	noState(pcrf.next())
}
