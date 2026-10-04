package pcscf

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/proxy"
)

// gateAA holds every AA-Request until the gate opens, then answers with answer.
func gateAA(f *fakePCRF, answer func(context.Context, *diameter.Message) (*diameter.Message, error)) (open func()) {
	gate := make(chan struct{})

	f.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandAA {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		return answer(ctx, req)
	})

	return func() { close(gate) }
}

// TS 24.229 §5.2.7.2: a 2xx held behind a refused answer never reaches the caller.
func TestCallRefusedWithA2xxQueued(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	got, f := s.originate(t, u)

	open := gateAA(pcrf, refuseAA)

	tag := sip.NewTag()
	s.coreAnswer(got, f, 183, tag, "5000")
	pcrf.aar()

	s.scscf.Send(f.Transport, f.Remote, s.coreResponse(got, 200, tag))
	u.us.RecvNone(quiet)

	open()

	for _, method := range []string{"ACK", "BYE"} {
		if req, _ := s.scscf.RecvRequest(); req.Method != method {
			t.Fatalf("S-CSCF got %s, want the %s", req.Method, method)
		}
	}

	wantStatus(t, first(u.us.RecvResponse()), 500)
	u.us.RecvNone(quiet)
}

// RFC 3264, TS 24.229 §5.2.7.2: a refused answer in the ACK still reaches the callee, then the call is released.
func TestCallMediaRefusedInTheAck(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	invite, got, f := s.originateWith(t, u, false)

	pcrf.answerWith(refuseAA)

	ok := s.coreResponse(got, 200, sip.NewTag())
	ok.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, ok)

	res, _ := u.us.RecvResponse()
	wantStatus(t, res, 200)

	ud, err := dialog.NewUAC(invite, res)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := ud.NewAck(invite)
	if err != nil {
		t.Fatal(err)
	}

	ack.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	s.ueSend(u, ack)

	pcrf.aar()

	fwd, _ := s.scscf.RecvRequest()
	if fwd.Method != "ACK" || len(fwd.Body) == 0 {
		t.Fatalf("S-CSCF got %s with %d bytes of body, want the UE's ACK with its answer", fwd.Method, len(fwd.Body))
	}

	if req, _ := s.scscf.RecvRequest(); req.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", req.Method)
	}

	if req, _ := u.us.RecvRequest(); req.Method != "BYE" {
		t.Fatalf("UE got %s, want the BYE", req.Method)
	}

	pcrf.none()
}

// TS 29.214 Annex A.3.1: an UPDATE exchange within a forked early dialogue is SEVERAL_DIALOGUES.
func TestForkedCallEarlyUpdate(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	invite, got, f := s.originateWith(t, u, true)

	one := s.coreResponse(got, 183, "one")
	one.Header.Add("Require", "100rel")
	one.Header.Add("RSeq", "1")
	one.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, one)
	pcrf.aar()

	early, _ := u.us.RecvResponse()
	wantStatus(t, early, 183)

	s.coreAnswer(got, f, 183, "two", "6000")
	pcrf.aar()
	wantStatus(t, first(u.us.RecvResponse()), 183)

	ud, err := dialog.NewUAC(invite, early)
	if err != nil {
		t.Fatal(err)
	}

	update, err := ud.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	update.Header.Add("Contact", ueContact(u))
	update.SetBody("application/sdp", sdpBody(ueAddr.String(), "4002"))
	s.ueSend(u, update)

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != "UPDATE" {
		t.Fatalf("S-CSCF got %s, want the UPDATE", fwd.Method)
	}

	ok := sip.NewResponse(fwd, 200, "")
	ok.SetBody("application/sdp", sdpBody("192.0.2.9", "5002"))
	s.scscf.Send(ff.Transport, ff.Remote, ok)

	_, aar := pcrf.aar()
	if aar.SIPForkingIndication != rx.ForkingSeveralDialogues {
		t.Errorf("SIP-Forking-Indication %s, want SEVERAL_DIALOGUES", aar.SIPForkingIndication)
	}

	wantStatus(t, first(u.us.RecvResponse()), 200)
}

// TS 29.214 §4.4.4: the SIP side gives up at the call timeout; a late successful AA-Answer is followed by an STR.
func TestCallMediaLateAnswerEndsTheSession(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t, func(c *Config) { c.Rx.CallTimeout = 100 * time.Millisecond })
	got, f := s.originate(t, u)

	open := gateAA(pcrf, func(_ context.Context, req *diameter.Message) (*diameter.Message, error) { return succeed(req) })

	s.coreAnswer(got, f, 183, sip.NewTag(), "5000")

	session, _ := pcrf.aar()

	if req, _ := s.scscf.RecvRequest(); req.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", req.Method)
	}

	wantStatus(t, first(u.us.RecvResponse()), 500)
	pcrf.none()

	open()
	pcrf.wantSTR(session, rx.TerminationLogout)
}

// TS 29.214 §4.4.4: a call session is terminated at shutdown.
func TestCallSessionEndsAtShutdown(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	s.p.Close()

	pcrf.wantSTR(e.session, rx.TerminationAdministrative)
}

// TS 29.214 §4.4.4: no STR before a successful initial AA-Answer.
func TestCallEndedBeforeItsFirstAAR(t *testing.T) {
	s, _, pcrf, _ := newRxIPsecScene(t)

	c := &call{ue: proxy.Caller, rx: s.p.newCallRx(regKey{testIMPI, ueAddr}, nil, "")}
	c.rx.session = s.p.rx.callSession(c.rx)

	s.p.callEnded(c)
	pcrf.none()
}

// TS 29.214 Annex A.1, RFC 3262 §3: a 2xx repeating a pending answer is not a new answer.
func TestCallRepeatedAnswerAuthorisedOnce(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	got, f := s.originate(t, u)

	release := pcrf.holdAA()

	tag := sip.NewTag()

	progress := s.coreResponse(got, 183, tag)
	progress.Header.Add("Require", "100rel")
	progress.Header.Add("RSeq", "1")
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)
	pcrf.aar()

	s.coreAnswer(got, f, 200, tag, "5000")
	u.us.RecvNone(quiet)

	release()

	wantStatus(t, first(u.us.RecvResponse()), 183)
	wantStatus(t, first(u.us.RecvResponse()), 200)
	pcrf.none()
}

// TS 24.229 §5.2.7.3: the access network charging information goes in the first reliable response from the UE.
func TestTerminatingChargingInfoOnTheFirstReliableResponse(t *testing.T) {
	s, u, pcrf, token := newRxIPsecScene(t)

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode != rx.CommandAA {
			return succeed(req)
		}

		return rx.NewAAAnswer(req, pcrfIdentity, rx.AAAnswer{
			AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{{Value: []byte{0x01}}},
			AccessNetworkChargingAddress:     netip.MustParseAddr("192.0.2.50"),
		})
	})

	path := "sip:" + token + "@" + s.pcscf.String() + ";lr"

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", path, func(r *sip.Request) {
		r.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	}))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()
	tag := sip.NewTag()

	respond := func(code int, sdp bool) {
		res := sip.NewResponse(got, code, "")
		_ = res.Header.SetToTag(tag)
		res.Header.Add("Contact", ueContact(u))

		for _, v := range got.Header.Values("Record-Route") {
			res.Header.Add("Record-Route", v)
		}

		if sdp {
			res.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
		}

		u.us.Send(sip.UDP, f.Remote, res)
	}

	respond(183, true)
	pcrf.aar()

	early, _ := s.scscf.RecvResponse()
	wantStatus(t, early, 183)

	if strings.Contains(early.Header.Get("P-Charging-Vector"), "pdngw") {
		t.Errorf("unreliable 183 P-Charging-Vector = %q, want no access network information", early.Header.Get("P-Charging-Vector"))
	}

	respond(200, false)

	ok, _ := s.scscf.RecvResponse()
	wantStatus(t, ok, 200)

	if !strings.Contains(ok.Header.Get("P-Charging-Vector"), `pdngw=192.0.2.50;eps-info="eps-item=1;eps-sig=no;ecid=01`) {
		t.Errorf("200 P-Charging-Vector = %q, want the access network information", ok.Header.Get("P-Charging-Vector"))
	}
}

// TS 24.229 §5.2.8.1.1: an abort for insufficient bearer resources during setup is a media loss.
func TestCallAbortedForBearerResourcesDuringSetup(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishEarly(t, u, pcrf)

	terminate, _ := s.p.AbortSession(e.session, rx.AbortSessionRequest{Cause: rx.AbortInsufficientBearerResources})

	cancel, _ := s.scscf.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", cancel.Method)
	}

	wantReason503(t, cancel)
	wantStatus(t, first(u.us.RecvResponse()), 500)

	terminate()
	pcrf.wantSTR(e.session, rx.TerminationAdministrative)
	pcrf.none()
}
