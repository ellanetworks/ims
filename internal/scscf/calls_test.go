package scscf

import (
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const reason480 = `SIP;cause=480;text="Temporarily Unavailable"`

// bye receives the next request on s, wants it a BYE with a Reason of 480, and answers it.
func bye(t *testing.T, s *siptest.Socket) {
	t.Helper()

	for {
		req, f := s.RecvRequest()
		if req.Method == "ACK" {
			continue
		}

		if req.Method != "BYE" || req.Header.Get("Reason") != reason480 {
			t.Fatalf("got %s with Reason %q, want a BYE with %q", req.StartLine(), req.Header.Get("Reason"), reason480)
		}

		s.Send(f.Transport, f.Remote, sip.NewResponse(req, 200, ""))

		return
	}
}

// answered sets up a call toward the UE and returns once the I-CSCF side ACKed its 200.
func (sh *sessionHarness) answered(t *testing.T) {
	t.Helper()

	invite := sh.terminating("INVITE", testTel)
	sh.icscf.Send(sip.UDP, sh.scscf, invite)

	got, f := sh.term.RecvRequest()
	reply(t, sh.term, got, f, 200, "ue")

	ok := final(t, sh.icscf)
	if ok.StatusCode != 200 {
		t.Fatalf("got %s", ok.StartLine())
	}

	ack := siptest.NewRequest("ACK", "sip:callee@"+sh.term.Addr().String(), sip.UDP, sh.icscf.Addr())
	ack.Header.Set("Call-ID", invite.Header.CallID())
	ack.Header.Set("From", invite.Header.Get("From"))
	ack.Header.Set("To", ok.Header.Get("To"))
	ack.Header.Set("CSeq", "1 ACK")
	ack.Header.Add("Route", ok.Header.Values("Record-Route")[0])
	sh.icscf.Send(sip.UDP, sh.scscf, ack)

	if req, _ := sh.term.RecvRequest(); req.Method != "ACK" {
		t.Fatalf("UE got %s, want the ACK", req.StartLine())
	}
}

// TS 24.229 §5.4.1.4 step 5
func TestDeregistrationReleasesTerminatingCall(t *testing.T) {
	sh := newSessionHarness(t)
	sh.answered(t)

	sh.ue.register(registerOptions{expires: "0"})

	bye(t, sh.term)
	bye(t, sh.icscf)
}

// TS 24.229 §5.4.1.2.2 step 4A a
func TestNewContactReleasesCalls(t *testing.T) {
	sh := newSessionHarness(t)
	sh.answered(t)

	sh.ue.register(registerOptions{contact: "<sip:001010000000001@127.0.0.1:5999>"})

	bye(t, sh.term)
	bye(t, sh.icscf)
}

// TS 24.229 §5.4.1.4 step 5: the call the UE made is released toward both parties.
func TestDeregistrationReleasesOriginatingCall(t *testing.T) {
	sh := newSessionHarness(t)

	invite, ok := sh.call(t)
	ack := sh.inDialog("ACK", invite, ok, ok.Header.Values("Record-Route")[0])
	ack.Header.Set("CSeq", "1 ACK")
	sh.orig.Send(sip.UDP, sh.scscf, ack)

	if req, _ := sh.icscf.RecvRequest(); req.Method != "ACK" {
		t.Fatalf("callee got %s, want the ACK", req.StartLine())
	}

	sh.ue.register(registerOptions{expires: "0"})

	bye(t, sh.icscf)
	bye(t, sh.orig)
}

// A refresh from the same contact leaves its calls alone.
func TestRefreshKeepsCalls(t *testing.T) {
	sh := newSessionHarness(t)
	sh.answered(t)

	sh.ue.register(registerOptions{})

	sh.term.RecvNone(50 * time.Millisecond)
	sh.icscf.RecvNone(50 * time.Millisecond)
}

// Deregistering one contact of a forked call cancels its branch; the other contacts ring on.
func TestDeregistrationCancelsItsBranch(t *testing.T) {
	sh := newSessionHarness(t)
	sh.addBinding(t, "sip:tablet@127.0.0.1:5999", "")

	invite := sh.terminating("INVITE", testTel)
	sh.icscf.Send(sip.UDP, sh.scscf, invite)

	got := sh.receive(t, 2)
	phone, tablet := got[sh.ue.contact], got["sip:tablet@127.0.0.1:5999"]

	if phone.req == nil || tablet.req == nil {
		t.Fatalf("INVITEs to %v", got)
	}

	reply(t, sh.term, phone.req, phone.f, 180, "phone")
	reply(t, sh.term, tablet.req, tablet.f, 180, "tablet")

	sh.ue.register(registerOptions{contact: "<sip:tablet@127.0.0.1:5999>", expires: "0"})

	cancel, f := sh.term.RecvRequest()
	if cancel.Method != "CANCEL" || cancel.URI.String() != "sip:tablet@127.0.0.1:5999" || cancel.Header.Get("Reason") != reason480 {
		t.Fatalf("got %s with Reason %q, want a CANCEL to the tablet", cancel.StartLine(), cancel.Header.Get("Reason"))
	}

	sh.term.Send(sip.UDP, f.Remote, sip.NewResponse(cancel, 200, ""))
	reply(t, sh.term, tablet.req, tablet.f, 487, "tablet")
	reply(t, sh.term, phone.req, phone.f, 200, "phone")

	for {
		res := final(t, sh.icscf)
		if res.StatusCode >= 200 {
			if res.StatusCode != 200 {
				t.Fatalf("got %s, want the phone's 200", res.StartLine())
			}

			return
		}
	}
}

// TS 24.229 §5.4.1.5
func TestNetworkDeregistrationReleasesCalls(t *testing.T) {
	for _, reason := range []cx.ReasonCode{cx.ReasonPermanentTermination, cx.ReasonServerChange, cx.ReasonNewServerAssigned} {
		t.Run(reason.String(), func(t *testing.T) {
			sh := newSessionHarness(t)
			sh.answered(t)

			if _, err := sh.reg.Terminate(t.Context(), cx.RegistrationTerminationRequest{
				PrivateIdentity: testIMPI,
				Reason:          cx.DeregistrationReason{Code: reason},
			}); err != nil {
				t.Fatal(err)
			}

			bye(t, sh.term)
			bye(t, sh.icscf)
		})
	}
}

// TS 24.229 §5.4.5.1.2A
func TestExpiryReleasesCalls(t *testing.T) {
	sh := newSessionHarness(t)
	sh.answered(t)

	sh.clock.Advance(2 * time.Hour)

	bye(t, sh.term)
	bye(t, sh.icscf)
}

// TS 24.229 §5.4.1.2.2 step 6d ii: a flow replaced by a new one has its calls released.
func TestFlowReplaceReleasesCalls(t *testing.T) {
	sh := newSessionHarness(t)
	contact := sh.ue.flowContact(sh.ue.contact, "1")

	sh.ue.path = "<sip:flow1@" + sh.term.Addr().String() + ";lr;ob>"
	sh.ue.register(registerOptions{contact: contact})
	sh.ue.register(registerOptions{contact: "<" + sh.ue.contact + ">", expires: "0"})

	sh.answered(t)

	sh.ue.register(registerOptions{contact: contact})
	sh.term.RecvNone(50 * time.Millisecond)

	sh.ue.path = "<sip:flow2@" + sh.term.Addr().String() + ";lr;ob>"
	sh.ue.register(registerOptions{contact: contact})

	bye(t, sh.term)
	bye(t, sh.icscf)
}

// A call cancelled before it was forked leaves no dialog in the calls index: one that never began
// would never end.
func TestCancelledCallsLeaveNoIndex(t *testing.T) {
	sh := newSessionHarness(t)

	for range 20 {
		invite := sh.terminating("INVITE", testTel)

		cancel, err := sip.NewCancel(invite)
		if err != nil {
			t.Fatal(err)
		}

		sh.icscf.Send(sip.UDP, sh.scscf, invite)
		sh.icscf.Send(sip.UDP, sh.scscf, cancel)
	}

	unbegun := func() int {
		sh.reg.calls.mu.Lock()
		defer sh.reg.calls.mu.Unlock()

		n := 0

		for d := range sh.reg.calls.byDialog {
			if d.CallID() == "" {
				n++
			}
		}

		return n
	}

	for cancelled := 0; cancelled < 20; {
		if res, _ := sh.icscf.RecvResponse(); res.StatusCode == 200 {
			if cseq, _ := res.Header.CSeq(); cseq.Method == "CANCEL" {
				cancelled++
			}
		}
	}

	deadline := time.Now().Add(5 * time.Second)

	for unbegun() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d dialogs that never began left in the calls index", unbegun())
		}

		time.Sleep(20 * time.Millisecond)
	}
}
