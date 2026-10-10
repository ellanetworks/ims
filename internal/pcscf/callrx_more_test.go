package pcscf

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
)

// ueOffer sends an in-dialog request from the UE with an SDP offer, answers it from the core with an SDP
// answer, and returns the response the UE receives.
func (s *ipsecScene) ueOffer(t *testing.T, u *ue, e *callUp, method string, offer, answer []byte) *sip.Response {
	t.Helper()

	req, err := e.ue.NewRequest(method)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Add("Contact", ueContact(u))
	req.SetBody("application/sdp", offer)
	s.ueSend(u, req)

	if method == "INVITE" {
		wantStatus(t, first(u.us.RecvResponse()), 100)
	}

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != method {
		t.Fatalf("S-CSCF got %s, want the %s", fwd.Method, method)
	}

	ok := sip.NewResponse(fwd, 200, "")
	ok.Header.Add("Contact", "<sip:callee@"+s.scscf.Addr().String()+">")
	ok.SetBody("application/sdp", answer)
	s.scscf.Send(ff.Transport, ff.Remote, ok)

	res, _ := u.us.RecvResponse()

	return res
}

// TS 29.214 §4.4.1: a refused modification keeps the call and the previous authorisation.
func TestCallModificationRefusedKeepsTheCall(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	pcrf.answerWith(refuseAA)

	res := s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"), sdpBody("192.0.2.9", "5000", "a=recvonly"))
	wantStatus(t, res, 200)

	pcrf.aar()
	s.scscf.RecvNone(quiet)
	pcrf.none()
}

// TS 29.214 §4.4.1: a PCRF that lost the call's session gets a new one for the modification.
func TestCallModificationOfAnUnknownSessionOpensANewOne(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	var once sync.Once

	pcrf.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
		unknown := false

		if req.CommandCode == rx.CommandAA {
			once.Do(func() { unknown = true })
		}

		if unknown {
			return rx.NewAnswer(req, pcrfIdentity, tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0), nil
		}

		return succeed(req)
	})

	res := s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"), sdpBody("192.0.2.9", "5000", "a=recvonly"))
	wantStatus(t, res, 200)

	if id, aar := pcrf.aar(); id != e.session || *aar.RequestType != rx.RequestUpdate {
		t.Fatalf("AAR %s %s, want the update of %s", id, aar.RequestType, e.session)
	}

	id, aar := pcrf.aar()
	if id == e.session || *aar.RequestType != rx.RequestInitial || len(aar.MediaComponents) != 1 {
		t.Fatalf("AAR %s %s, want an initial one for a new session", id, aar.RequestType)
	}

	pcrf.none()

	s.p.Close()
	pcrf.wantSTR(id, rx.TerminationAdministrative)
}

// TS 29.214 Annex A.2.0, TS 29.213 Table 6.2.1/6.2.2 NOTE 3: hold and resume.
func TestCallHoldAndResume(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	wantStatus(t, s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"),
		sdpBody("192.0.2.9", "5000", "a=recvonly")), 200)

	_, hold := pcrf.aar()

	c := hold.MediaComponents[0]
	if c.FlowStatus == nil || *c.FlowStatus != rx.FlowStatusEnabledUplink || len(c.SubComponents[0].FlowDescriptions) != 2 {
		t.Errorf("hold: Flow-Status %v, flows %q, want ENABLED_UPLINK with both directions after sendrecv",
			c.FlowStatus, c.SubComponents[0].FlowDescriptions)
	}

	wantStatus(t, s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"),
		sdpBody("192.0.2.9", "5000", "a=recvonly")), 200)

	_, again := pcrf.aar()
	if d := again.MediaComponents[0].SubComponents[0].FlowDescriptions; len(d) != 1 || !strings.HasPrefix(d[0], "permit in") {
		t.Errorf("second hold flows %q, want the uplink only", d)
	}

	wantStatus(t, s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000"), sdpBody("192.0.2.9", "5000")), 200)

	_, resume := pcrf.aar()
	if fs := resume.MediaComponents[0].FlowStatus; fs == nil || *fs != rx.FlowStatusEnabled {
		t.Errorf("resume Flow-Status %v, want ENABLED", fs)
	}
}

// RFC 3261 §14, TS 29.214 Annex A.1: the answer in the 2xx to a re-INVITE.
func TestCallReinviteAnswer(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	res := s.ueOffer(t, u, e, "INVITE", sdpBody(ueAddr.String(), "4002"), sdpBody("192.0.2.9", "5002"))
	wantStatus(t, res, 200)

	id, aar := pcrf.aar()
	if id != e.session || aar.RequestType == nil || *aar.RequestType != rx.RequestUpdate {
		t.Fatalf("AAR %s %+v, want an update of %s", id, aar, e.session)
	}

	if d := aar.MediaComponents[0].SubComponents[0].FlowDescriptions; !strings.Contains(strings.Join(d, " "), "192.0.2.9 5002") {
		t.Errorf("flows %q, want the new remote port", d)
	}
}

// TS 24.229 §5.2.7.2: a refused answer in the PRACK releases the call and the PRACK gets 500.
func TestCallMediaRefusedInThePrack(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	invite, got, f := s.originateWith(t, u, false)

	progress := s.coreResponse(got, 183, sip.NewTag())
	progress.Header.Add("Require", "100rel")
	progress.Header.Add("RSeq", "1")
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	early, _ := u.us.RecvResponse()
	wantStatus(t, early, 183)

	pcrf.answerWith(refuseAA)

	ud, err := dialog.NewUAC(invite, early)
	if err != nil {
		t.Fatal(err)
	}

	prack, err := ud.NewPrack(early)
	if err != nil {
		t.Fatal(err)
	}

	prack.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	s.ueSend(u, prack)

	pcrf.aar()

	if req, _ := s.scscf.RecvRequest(); req.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", req.Method)
	}

	methods := map[string]int{}

	for range 2 {
		res, _ := u.us.RecvResponse()
		cseq, _ := res.Header.CSeq()
		methods[cseq.Method] = res.StatusCode
	}

	if methods["INVITE"] != 500 || methods["PRACK"] != 500 {
		t.Fatalf("UE got %v, want 500 to the INVITE and to the PRACK", methods)
	}
}

// RFC 3262 §5: on the terminating side the network answers the UE's offer in the PRACK.
func TestTerminatingMediaAnsweredInThePrack(t *testing.T) {
	s, u, pcrf, token := newRxIPsecScene(t)

	path := "sip:" + token + "@" + s.pcscf.String() + ";lr"
	invite := s.coreRequest(u, "INVITE", path, nil)
	s.scscf.Send(sip.UDP, s.pcscf, invite)
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()

	progress := sip.NewResponse(got, 183, "")
	_ = progress.Header.SetToTag(sip.NewTag())
	progress.Header.Add("Contact", ueContact(u))
	progress.Header.Add("Require", "100rel")
	progress.Header.Add("RSeq", "1")

	for _, v := range got.Header.Values("Record-Route") {
		progress.Header.Add("Record-Route", v)
	}

	progress.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	u.us.Send(sip.UDP, f.Remote, progress)

	early, _ := s.scscf.RecvResponse()
	wantStatus(t, early, 183)
	pcrf.none()

	release := pcrf.holdAA()

	cd, err := dialog.NewUAC(invite, early)
	if err != nil {
		t.Fatal(err)
	}

	prack, err := cd.NewPrack(early)
	if err != nil {
		t.Fatal(err)
	}

	routes, _ := prack.Header.Routes()
	setRoutes(prack, routes[1:])
	prack.Header.Prepend("Via", sip.NewVia(sip.UDP, s.scscf.Addr()).String())
	prack.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(sip.UDP, s.pcscf, prack)

	_, aar := pcrf.aar()

	u.us.RecvNone(quiet)
	release()

	if req, _ := u.us.RecvRequest(); req.Method != "PRACK" {
		t.Fatalf("UE got %s, want the PRACK", req.Method)
	}

	if cdata := aar.MediaComponents[0].CodecData; cdata[0].Direction != rx.CodecUplink || cdata[1].Direction != rx.CodecDownlink {
		t.Errorf("Codec-Data %+v, want the UE's offer and the network's answer", cdata)
	}
}

// TS 29.213 Annex B.4.1: a call cancelled after its answer was authorised ends its session.
func TestCallCancelledEndsTheSession(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishEarly(t, u, pcrf)

	cancel, err := sip.NewCancel(e.invite)
	if err != nil {
		t.Fatal(err)
	}

	u.uc.Send(sip.UDP, s.ps, cancel)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", fwd.Method)
	}

	s.scscf.Send(ff.Transport, ff.Remote, sip.NewResponse(fwd, 200, ""))
	s.scscf.Send(e.f.Transport, e.f.Remote, s.coreResponse(e.got, 487, e.tag))

	wantStatus(t, first(u.us.RecvResponse()), 487)
	pcrf.wantSTR(e.session, rx.TerminationLogout)
}

// TS 29.214 §4.4.1: Retry-Interval holds only the same service information, and only for its duration.
func TestCallMediaRetryIntervalExpires(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)

	pcrf.answerWith(refuseAAWith(tgpp.ResultRequestedServiceTemporarilyNotAuthorized, time.Minute))

	call := func(port string, sent bool) {
		t.Helper()

		got, f := s.originate(t, u)
		s.coreAnswer(got, f, 183, sip.NewTag(), port)

		if sent {
			pcrf.aar()
		} else {
			pcrf.none()
		}

		if req, _ := s.scscf.RecvRequest(); req.Method != "CANCEL" {
			t.Fatalf("S-CSCF got %s, want the CANCEL", req.Method)
		}

		wantStatus(t, first(u.us.RecvResponse()), 500)
	}

	call("5000", true)
	call("5000", false)
	call("5002", true)

	clk.Advance(time.Minute)
	call("5000", true)
}

// TS 29.214 §4.4.6.5, TS 24.229 §5.2.7.4: charging information reported later in a RAR.
func TestCallChargingInfoFromAReAuth(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	if !s.p.rxReAuth(e.session, rx.ReAuthRequest{
		SpecificActions:                  []rx.SpecificAction{rx.ActionChargingCorrelationExchange},
		AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{{Value: []byte{0x0f}}},
		AccessNetworkChargingAddress:     netip.MustParseAddr("192.0.2.50"),
	}) {
		t.Fatal("ReAuth reported an unknown session")
	}

	s.scscf.RecvNone(quiet)

	update, err := e.ue.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	update.Header.Add("Contact", ueContact(u))
	s.ueSend(u, update)

	fwd, _ := s.scscf.RecvRequest()
	if cv := fwd.Header.Get("P-Charging-Vector"); !strings.Contains(cv, `pdngw=192.0.2.50;eps-info="eps-item=1;eps-sig=no;ecid=0F"`) {
		t.Errorf("UPDATE P-Charging-Vector = %q, want the access network information", cv)
	}
}

// TS 29.214 §5.6.3: IP-CAN-Type is optional in a RAR, so a later one without it keeps the 5GS access.
func TestCallChargingInfoKeepsTheAccess(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	fiveGS := rx.IPCAN3GPP5GS

	for _, n := range []rx.AccessNetwork{{IPCANType: &fiveGS}, {}} {
		if !s.p.rxReAuth(e.session, rx.ReAuthRequest{
			SpecificActions:                  []rx.SpecificAction{rx.ActionChargingCorrelationExchange},
			AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{{Value: []byte{0x0f}}},
			AccessNetworkChargingAddress:     netip.MustParseAddr("192.0.2.50"),
			AccessNetwork:                    n,
		}) {
			t.Fatal("ReAuth reported an unknown session")
		}
	}

	update, err := e.ue.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	update.Header.Add("Contact", ueContact(u))
	s.ueSend(u, update)

	fwd, _ := s.scscf.RecvRequest()
	if cv := fwd.Header.Get("P-Charging-Vector"); !strings.Contains(cv, `smf=192.0.2.50;5gs-info="5gs-item=1;5gscid=0F"`) {
		t.Errorf("UPDATE P-Charging-Vector = %q, want the 5GS access kept", cv)
	}
}

// TS 29.214 §4.4.1: a 5002 that comes after the call timeout still gets the next exchange a new session.
func TestCallLateUnknownSessionOpensANewOne(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t, func(c *Config) { c.Policy.CallTimeout = 100 * time.Millisecond })
	e := s.establishConfirmed(t, u, pcrf)

	var once sync.Once

	open := gateAA(pcrf, func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		unknown := false

		once.Do(func() { unknown = true })

		if unknown {
			return rx.NewAnswer(req, pcrfIdentity, tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0), nil
		}

		return succeed(req)
	})

	res := s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"), sdpBody("192.0.2.9", "5000", "a=recvonly"))
	wantStatus(t, res, 200)

	if id, _ := pcrf.aar(); id != e.session {
		t.Fatalf("AAR for %s, want the update of %s", id, e.session)
	}

	open()

	for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, ok := s.p.policy.lookup(e.session); !ok {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the late 5002 did not end the session")
		}
	}

	res = s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000"), sdpBody("192.0.2.9", "5000"))
	wantStatus(t, res, 200)

	if id, aar := pcrf.aar(); id == e.session || *aar.RequestType != rx.RequestInitial {
		t.Fatalf("AAR %s %s, want an initial one for a new session", id, aar.RequestType)
	}
}

// TS 29.214 §4.4.1: the new session for a lost one has nothing to remove.
func TestCallRecoveryLeavesRemovedMediaOut(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	var once sync.Once

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		unknown := false

		once.Do(func() { unknown = true })

		if unknown {
			return rx.NewAnswer(req, pcrfIdentity, tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0), nil
		}

		return succeed(req)
	})

	res := s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "0"), sdpBody("192.0.2.9", "0"))
	wantStatus(t, res, 200)

	_, update := pcrf.aar()
	if len(update.MediaComponents) != 1 || *update.MediaComponents[0].FlowStatus != rx.FlowStatusRemoved {
		t.Fatalf("update media %+v, want the component removed", update.MediaComponents)
	}

	if _, initial := pcrf.aar(); *initial.RequestType != rx.RequestInitial || len(initial.MediaComponents) != 0 {
		t.Fatalf("recovery AAR %s with media %+v, want an initial one without the removed component",
			initial.RequestType, initial.MediaComponents)
	}
}

// TS 29.214 §5.3.13 NOTE 2a, TS 24.229 §5.2.8.1.1: failed resources allocation is a media loss.
func TestCallFailedResourcesAllocation(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishEarly(t, u, pcrf)

	s.p.rxReAuth(e.session, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfFailedResourcesAllocation}})

	s.scscf.RecvNone(quiet)
	clk.Advance(lossTimeout)

	cancel, _ := s.scscf.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", cancel.Method)
	}

	wantReason503(t, cancel)
	wantStatus(t, first(u.us.RecvResponse()), 500)
}

// TS 24.229 §6.2: media of another address family than the offer cannot be authorised.
func TestCallMixedAddressFamilies(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	got, f := s.originate(t, u)

	progress := s.coreResponse(got, 183, sip.NewTag())
	progress.SetBody("application/sdp", []byte(strings.Join([]string{
		"v=0", "o=- 1 1 IN IP6 2001:db8::9", "s=-", "c=IN IP6 2001:db8::9", "t=0 0",
		"m=audio 5000 RTP/AVP 116", "b=AS:41", "a=rtpmap:116 AMR-WB/16000",
	}, "\r\n")+"\r\n"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	if req, _ := s.scscf.RecvRequest(); req.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", req.Method)
	}

	wantStatus(t, first(u.us.RecvResponse()), 500)
	pcrf.none()
}

// RFC 6733 §5.5.4, §8.18 REFUSE_SERVICE: with no connection or no path to the PCRF of the call, the modification is
// undelivered and the call ends, both ways.
func TestCallModificationWithItsPCRFLostReleasesTheCall(t *testing.T) {
	for name, lose := range map[string]func(*diameter.Message) *diameter.Message{
		"no connection": func(*diameter.Message) *diameter.Message { return nil },
		"no path":       func(req *diameter.Message) *diameter.Message { return undeliverable(req) },
	} {
		t.Run(name, func(t *testing.T) {
			s, u, pcrf, _ := newRxIPsecScene(t)
			e := s.establishConfirmed(t, u, pcrf)

			pcrf.answerWith(func(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
				if req.CommandCode == rx.CommandAA {
					return lose(req), nil
				}

				return succeed(req)
			})

			req, err := e.ue.NewRequest("UPDATE")
			if err != nil {
				t.Fatal(err)
			}

			req.Header.Add("Contact", ueContact(u))
			req.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000", "a=sendonly"))
			s.ueSend(u, req)

			fwd, ff := s.scscf.RecvRequest()
			ok := sip.NewResponse(fwd, 200, "")
			ok.Header.Add("Contact", "<sip:callee@"+s.scscf.Addr().String()+">")
			ok.SetBody("application/sdp", sdpBody("192.0.2.9", "5000", "a=recvonly"))
			s.scscf.Send(ff.Transport, ff.Remote, ok)

			if id, aar := pcrf.aar(); id != e.session || *aar.RequestType != rx.RequestUpdate {
				t.Fatalf("AAR %s %s, want the update of %s", id, aar.RequestType, e.session)
			}

			if bye, _ := s.scscf.RecvRequest(); bye.Method != "BYE" || !strings.Contains(bye.Header.Get("Reason"), "cause=503") {
				t.Fatalf("S-CSCF got %s with Reason %q, want a BYE for cause 503", bye.Method, bye.Header.Get("Reason"))
			}

			for {
				if r, ok := u.us.Recv().Msg.(*sip.Request); ok {
					if r.Method != "BYE" {
						t.Fatalf("UE got %s, want the BYE", r.Method)
					}

					break
				}
			}

			pcrf.none()
		})
	}
}

// RFC 6733 §7.1.3: a PCRF too busy for the modification has not lost the call's session: the modification is refused
// and the call goes on.
func TestCallModificationWithItsPCRFTooBusyKeepsTheCall(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode != rx.CommandAA {
			return succeed(req)
		}

		return diameter.NewAnswer(req, pcrfIdentity, diameter.ResultTooBusy), nil
	})

	res := s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"), sdpBody("192.0.2.9", "5000", "a=recvonly"))
	wantStatus(t, res, 200)

	pcrf.aar()
	s.scscf.RecvNone(quiet)
	pcrf.none()
}

// RFC 6733 §8.18 REFUSE_SERVICE: a session lost in an early dialog ends the call at once, with the media of the
// exchange left unauthorized.
func TestEarlyCallWithItsPCRFLostIsReleased(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	invite, got, f := s.originateWith(t, u, true)

	early := s.coreResponse(got, 183, sip.NewTag())
	early.Header.Add("Require", "100rel")
	early.Header.Add("RSeq", "1")
	early.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, early)
	pcrf.aar()

	res, _ := u.us.RecvResponse()
	wantStatus(t, res, 183)

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandAA {
			return undeliverable(req), nil
		}

		return succeed(req)
	})

	ud, err := dialog.NewUAC(invite, res)
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
	ok := sip.NewResponse(fwd, 200, "")
	ok.SetBody("application/sdp", sdpBody("192.0.2.9", "5002"))
	s.scscf.Send(ff.Transport, ff.Remote, ok)

	pcrf.aar()

	cancel, _ := s.scscf.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", cancel.Method)
	}

	wantReason503(t, cancel)

	for {
		r, _ := u.us.RecvResponse()
		if r.StatusCode >= 300 {
			break
		}

		if r.StatusCode == 200 && r.Header.Get("CSeq") == update.Header.Get("CSeq") {
			t.Fatal("the UE got the 200 to its UPDATE, whose media nothing authorizes")
		}
	}

	pcrf.none()
}

// RFC 6733 §8.18 REFUSE_SERVICE: a session lost after the exchange gave up on its answer still ends the call.
func TestCallWithItsPCRFLostAfterTheCallTimeoutIsReleased(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t, func(c *Config) { c.Policy.CallTimeout = 100 * time.Millisecond })
	e := s.establishConfirmed(t, u, pcrf)

	open := gateAA(pcrf, func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		return undeliverable(req), nil
	})

	res := s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"), sdpBody("192.0.2.9", "5000", "a=recvonly"))
	wantStatus(t, res, 200)

	pcrf.aar()
	open()

	bye, _ := s.scscf.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", bye.Method)
	}

	wantReason503(t, bye)

	if r, _ := u.us.RecvRequest(); r.Method != "BYE" {
		t.Fatalf("UE got %s, want the BYE", r.Method)
	}

	pcrf.none()
}

// RFC 6733 §8.18 ALLOW_SERVICE: a modification no path takes to the PCRF of the call succeeds, and the session goes
// on unbound: the next request goes by realm, and the call is not released.
func TestCallModificationWithItsPCRFLostAllowsService(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)

	allow := func(req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandAA {
			return rx.NewAAAnswer(req, pcrfIdentity, rx.AAAnswer{SessionServerFailover: diameter.AllowService})
		}

		return succeed(req)
	}

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) { return allow(req) })

	e := s.establishConfirmed(t, u, pcrf)

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		if tgpp.ParseEnvelope(req).DestinationHost != "" {
			return undeliverable(req), nil
		}

		return allow(req)
	})

	res := s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000", "a=sendonly"), sdpBody("192.0.2.9", "5000", "a=recvonly"))
	wantStatus(t, res, 200)

	if m := pcrf.next(); tgpp.ParseEnvelope(m).DestinationHost != pcrfIdentity.OriginHost {
		t.Fatalf("first update to %q, want the PCRF of the session", tgpp.ParseEnvelope(m).DestinationHost)
	}

	res = s.ueOffer(t, u, e, "UPDATE", sdpBody(ueAddr.String(), "4000"), sdpBody("192.0.2.9", "5000"))
	wantStatus(t, res, 200)

	if m := pcrf.next(); tgpp.ParseEnvelope(m).SessionID != e.session || tgpp.ParseEnvelope(m).DestinationHost != "" {
		t.Fatalf("next update %s to %q, want the session's, by realm", tgpp.ParseEnvelope(m).SessionID, tgpp.ParseEnvelope(m).DestinationHost)
	}

	s.scscf.RecvNone(quiet)
	pcrf.none()
}
