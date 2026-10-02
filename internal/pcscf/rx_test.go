package pcscf

import (
	"context"
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
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

var (
	imsIdentity  = diameter.Identity{OriginHost: "ims." + homeDomain, OriginRealm: homeDomain}
	pcrfIdentity = diameter.Identity{OriginHost: "pcrf.epc.test", OriginRealm: "epc.test"}
)

// fakePCRF is the P-CSCF's Diameter node with a PCRF behind it. It records
// every request and answers with answer, or with success.
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

func (f *fakePCRF) config(timeout time.Duration) func(*Config) {
	return func(c *Config) {
		c.Rx = Rx{
			Diameter: f,
			PCRF:     PCRF{ID: "pcrf", Host: pcrfIdentity.OriginHost, Realm: pcrfIdentity.OriginRealm},
			Timeout:  timeout,
		}
	}
}

func (f *fakePCRF) Identity() diameter.Identity { return imsIdentity }

func (f *fakePCRF) NewSessionID() string {
	return fmt.Sprintf("%s;1;%d", imsIdentity.OriginHost, f.seq.Add(1))
}

func (f *fakePCRF) Do(ctx context.Context, peerID string, req *diameter.Message, _ ...diameter.DoOption) (*diameter.Message, error) {
	if peerID != "pcrf" {
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

// holdAA makes the next AAs wait for release before succeeding.
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
	return s.p.regs.get(testIMPI, ueAddr)
}

func (s *regScene) wantSession(id string) {
	s.t.Helper()

	eventually(s.t, "the Rx session "+id+" on the stored record", func() bool {
		regs, err := s.store.ListPCSCFRegistrations(context.Background())
		return err == nil && len(regs) == 1 && regs[0].RxSessionID == id
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
	answerRegister(s.icscf, s.scscf.Addr(), req, f, 600)

	// The 200 reaches the UE while the AAR is unanswered.
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

	if r, ok := s.record(); !ok || r.RxSessionID != env.SessionID {
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
	c := newRxClient(Rx{Diameter: pcrf, PCRF: PCRF{ID: "pcrf", Host: pcrfIdentity.OriginHost, Realm: pcrfIdentity.OriginRealm}},
		slog.New(slog.DiscardHandler))
	t.Cleanup(c.close)

	ue := netip.MustParseAddr("2001:db8::1")
	s := c.begin(regKey{testIMPI, ue})

	if _, err := c.aar(s, 0); err != nil {
		t.Fatalf("aar: %v", err)
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

	if reg, ok := s.p.regs.get(testIMPI, ueAddr); !ok || reg.RxSessionID != id {
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
		// str is the cause of the STR that follows, for an AAR the PCRF may
		// have authorized (RFC 6733 §7.2, §8.4); 0 for none.
		str rx.TerminationCause
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
			s, pcrf := newRxScene(t, 0)
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

	if _, ok := s.p.rx.lookup(id); ok {
		t.Fatal("session kept after its STR")
	}
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
		_, ok := s.p.rx.lookup(id)
		return !ok
	})
}

// terminating sends an initial request towards the UE through its Path.
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
	for _, action := range []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer} {
		t.Run(action.String(), func(t *testing.T) {
			s, pcrf := newRxScene(t, 0)

			path, _, _ := s.registered(600)
			id, _ := pcrf.aar()

			if !s.p.ReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{action}}) {
				t.Fatal("ReAuth reported an unknown session")
			}

			s.wantSignallingLost(true)

			s.terminating(path)
			wantStatus(t, first(s.scscf.RecvResponse()), 500)

			if r, _ := s.record(); r.RxSessionID != id {
				t.Fatalf("session = %q, want %s kept", r.RxSessionID, id)
			}

			pcrf.none()

			s.reregister(600)
			s.wantSignallingLost(false)

			s.terminating(path)
			s.fallback.NextRequest()
		})
	}
}

func TestRxReAuthWithOtherActions(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()

	if !s.p.ReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfRecoveryOfBearer}}) {
		t.Fatal("ReAuth reported an unknown session")
	}

	if r, _ := s.record(); r.SignallingLost {
		t.Fatal("signalling lost set by another action")
	}

	if s.p.ReAuth("ims.test;9;9", rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}) {
		t.Fatal("ReAuth accepted an unknown session")
	}
}

func TestRxRequestFromTheUERestoresTheSignalling(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.p.ReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer}})

	s.wantSignallingLost(true)

	s.ue.Send(sip.UDP, s.pcscf, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, s.ue.Addr()))
	s.fallback.NextRequest()

	s.wantSignallingLost(false)
}

func TestRxRequestOnTheSAsRestoresTheSignalling(t *testing.T) {
	pcrf := newFakePCRF(t)
	s, u := newIPsecRegScene(t, pcrf.config(0))
	s.registerOverIPsec(u)

	id, _ := pcrf.aar()
	s.p.ReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}})

	if r, _ := s.p.regs.get(testIMPI, ueAddr); !r.SignallingLost {
		t.Fatal("signalling lost not set")
	}

	u.uc.Send(sip.UDP, s.ps, siptest.NewRequest("OPTIONS", "sip:"+homeDomain, sip.UDP, u.us.Addr()))
	fallbackRequest(t, s)

	if r, _ := s.p.regs.get(testIMPI, ueAddr); r.SignallingLost {
		t.Fatal("signalling lost kept after a request on the UE's security associations")
	}
}

func TestRxAbortSession(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()
	s.wantSession(id)

	terminate, ok := s.p.AbortSession(id, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if !ok {
		t.Fatal("AbortSession reported an unknown session")
	}

	s.wantSession("")
	s.wantSignallingLost(true)

	// The STR waits for the ASA to be sent (TS 29.214 §4.4.6.1).
	pcrf.none()

	terminate()
	pcrf.wantSTR(id, rx.TerminationAdministrative)

	if _, ok := s.p.AbortSession(id, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased}); ok {
		t.Fatal("AbortSession accepted the ended session")
	}

	s.reregister(600)
	s.wantSignallingLost(false)

	again, _ := pcrf.aar()
	s.wantSession(again)
}

func TestRxAbortSessionWhileTheAAAIsOutstanding(t *testing.T) {
	s, pcrf := newRxScene(t, 0)
	release := pcrf.holdAA()
	t.Cleanup(release)

	s.registered(600)

	id, _ := pcrf.aar()

	terminate, ok := s.p.AbortSession(id, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
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

	// The PCRF may have dropped it on the new Origin-State-Id (RFC 6733
	// §8.16): it is closed, and a new one opened.
	pcrf.wantSTR(id, rx.TerminationAdministrative)

	again, r := pcrf.aar()
	if again == id || r.FramedIPAddress != ueAddr {
		t.Fatalf("AAR %s for %s after the restart, want a new session for %s", again, r.FramedIPAddress, ueAddr)
	}

	s.wantSession(again)

	if s.p.ReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}) {
		t.Fatal("the old session still known")
	}

	if !s.p.ReAuth(again, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}) {
		t.Fatal("the new session unknown")
	}

	s.wantSignallingLost(true)
	pcrf.none()
}

func TestRxSessionPendingAtShutdownEndedAfterRestart(t *testing.T) {
	s, pcrf := newRxScene(t, 0)
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

	if r, ok := s.record(); !ok || r.RxSessionID != "" {
		t.Fatalf("record = %+v, %v; want an emergency registration without an Rx session", r, ok)
	}
}

func TestRxSignallingLostDuringARegistration(t *testing.T) {
	s, pcrf := newRxScene(t, 0)

	s.registered(600)

	id, _ := pcrf.aar()

	s.p.ReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer}})
	s.wantSignallingLost(true)

	// A new REGISTER clears it on arrival (TS 24.229 §5.2.6.4.3 NOTE 1).
	req, f := s.register(nil)
	s.wantSignallingLost(false)

	// Lost again before the 200: the 200 keeps it.
	s.p.ReAuth(id, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}})
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

	// A partial deregistration saving the snapshot after the removal.
	s.p.regs.save(snapshot)

	if r, _ := s.record(); r.RxSessionID != "" {
		t.Fatalf("session = %q, want the ended session not brought back", r.RxSessionID)
	}

	s.reregister(600)

	again, _ := pcrf.aar()
	s.wantSession(again)
}

// classAA answers AARs with Class values, and records the STRs' Class.
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
		return err == nil && len(regs) == 1 && regs[0].RxSessionID == id && reflect.DeepEqual(regs[0].RxClass, class)
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
		return ok && reflect.DeepEqual(r.RxClass, class)
	})

	s.restart()
	pcrf.wantSTR(id, rx.TerminationAdministrative)

	if got := <-strs; !reflect.DeepEqual(got, class) {
		t.Fatalf("STR Class after the restart = %q, want %q", got, class)
	}
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

		if c := terminationCause(e); c != rx.TerminationAdministrative {
			t.Errorf("%v: cause %s, want DIAMETER_ADMINISTRATIVE", events, c)
		}
	}

	if c := terminationCause(graver(graver("", regevent.Expired), regevent.Unregistered)); c != rx.TerminationLogout {
		t.Errorf("expired and unregistered: cause %s, want DIAMETER_LOGOUT", c)
	}
}

func TestNoRxWithoutAPCRF(t *testing.T) {
	s := newRegScene(t)

	s.registered(600)

	if r, _ := s.record(); r.RxSessionID != "" {
		t.Fatalf("session = %q without a PCRF", r.RxSessionID)
	}

	if _, known := s.p.AbortSession("ims.test;1;1", rx.AbortSessionRequest{}); known || s.p.ReAuth("ims.test;1;1", rx.ReAuthRequest{}) {
		t.Fatal("a session found without a PCRF")
	}
}
