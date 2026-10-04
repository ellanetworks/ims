package pcscf

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func sdpBody(addr string, port string, extra ...string) []byte {
	lines := []string{
		"v=0", "o=- 1 1 IN IP4 " + addr, "s=-", "c=IN IP4 " + addr, "t=0 0",
		"m=audio " + port + " RTP/AVP 116", "b=AS:41", "a=rtpmap:116 AMR-WB/16000",
	}

	return []byte(strings.Join(append(lines, extra...), "\r\n") + "\r\n")
}

func newRxIPsecScene(t *testing.T, opts ...func(*Config)) (*ipsecScene, *ue, *fakePCRF, string) {
	t.Helper()

	f := newFakePCRF(t)
	s, u := newIPsecRegScene(t, append([]func(*Config){f.config(0)}, opts...)...)
	token, _ := s.registerOverIPsec(u)

	if _, r := f.aar(); r.MediaComponents[0].Type == nil || *r.MediaComponents[0].Type != rx.MediaControl {
		t.Fatalf("AAR %+v, want the signalling one", r)
	}

	return s, u, f, token
}

func refuseAA(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
	if req.CommandCode != rx.CommandAA {
		return succeed(req)
	}

	return rx.NewAAErrorAnswer(req, pcrfIdentity, rx.AAError{ResultError: rx.ResultError{Result: tgpp.Result{
		Code: tgpp.ResultRequestedServiceNotAuthorized, Experimental: true, VendorID: tgpp.VendorID,
	}}})
}

func (s *ipsecScene) originate(t *testing.T, u *ue) (*sip.Request, sip.Flow) {
	t.Helper()

	_, got, f := s.originateWith(t, u, true)

	return got, f
}

func (s *ipsecScene) originateWith(t *testing.T, u *ue, offer bool) (*sip.Request, *sip.Request, sip.Flow) {
	t.Helper()

	invite := s.ueInvite(u, func(r *sip.Request) {
		r.Header.Add("P-Preferred-Identity", "<"+testTel+">")

		if offer {
			r.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
		}
	})
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, first(u.us.RecvResponse()), 100)

	got, f := s.scscf.RecvRequest()
	if got.Method != "INVITE" {
		t.Fatalf("S-CSCF got %s, want the INVITE", got.Method)
	}

	return invite, got, f
}

func TestCallMediaAuthorizedOnTheAnswer(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	got, f := s.originate(t, u)

	release := pcrf.holdAA()

	tag := sip.NewTag()

	progress := s.coreResponse(got, 183, tag)
	progress.Header.Add("Feature-Caps", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`)
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	session, aar := pcrf.aar()

	s.scscf.Send(f.Transport, f.Remote, s.coreResponse(got, 180, tag))
	u.us.RecvNone(quiet)

	release()

	wantStatus(t, first(u.us.RecvResponse()), 183)
	wantStatus(t, first(u.us.RecvResponse()), 180)

	if aar.RequestType == nil || *aar.RequestType != rx.RequestInitial || aar.FramedIPAddress != ueAddr ||
		!slices.Equal(aar.SpecificActions, []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer}) {
		t.Errorf("AAR %+v, want an initial one for the UE with the bearer events", aar)
	}

	if aar.AFApplicationIdentifier != icsiForTest {
		t.Errorf("AF-Application-Identifier %q, want the ICSI of the Feature-Caps", aar.AFApplicationIdentifier)
	}

	if want := []rx.SubscriptionID{{Type: rx.SubscriptionIDE164, Data: strings.TrimPrefix(testTel, "tel:+")}}; !slices.Equal(aar.SubscriptionIDs, want) {
		t.Errorf("Subscription-Id %+v, want %+v", aar.SubscriptionIDs, want)
	}

	if len(aar.MediaComponents) != 1 || len(aar.MediaComponents[0].SubComponents) != 2 ||
		!slices.Contains(aar.MediaComponents[0].SubComponents[0].FlowDescriptions, "permit in 17 from 127.0.0.2 to 192.0.2.9 5000") {
		t.Errorf("Media-Component-Description %+v, want the audio flows", aar.MediaComponents)
	}

	ok := s.coreResponse(got, 200, tag)
	s.scscf.Send(f.Transport, f.Remote, ok)
	wantStatus(t, first(u.us.RecvResponse()), 200)
	pcrf.none()

	s.scscf.Send(f.Transport, f.Remote, ok)
	wantStatus(t, first(u.us.RecvResponse()), 200)

	bye := siptest.NewRequest("BYE", "sip:callee@"+s.scscf.Addr().String(), sip.UDP, s.scscf.Addr())
	bye.Header.Set("From", "<"+callee+">;tag="+tag)
	bye.Header.Set("To", got.Header.Get("From"))
	bye.Header.Set("Call-ID", got.Header.CallID())
	bye.Header.Set("CSeq", "1 BYE")

	rr := uris(got.Header.RecordRoutes())
	bye.Header.Add("Route", "<"+rr[0]+">, <"+rr[1]+">")
	s.scscf.Send(sip.UDP, s.pcscf, bye)

	req, rf := u.us.RecvRequest()
	if req.Method != "BYE" {
		t.Fatalf("UE got %s, want the BYE", req.Method)
	}

	u.us.Send(sip.UDP, rf.Remote, sip.NewResponse(req, 200, ""))
	wantStatus(t, first(s.scscf.RecvResponse()), 200)

	pcrf.wantSTR(session, rx.TerminationLogout)
}

func TestCallMediaRefusedReleasesTheCall(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	got, f := s.originate(t, u)

	pcrf.answerWith(refuseAA)

	progress := s.coreResponse(got, 183, sip.NewTag())
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	pcrf.aar()

	if req, _ := s.scscf.RecvRequest(); req.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", req.Method)
	}

	wantStatus(t, first(u.us.RecvResponse()), 500)
	pcrf.none()
}

func TestCallMediaAuthorizedOnTheTerminatingAnswer(t *testing.T) {
	s, u, pcrf, token := newRxIPsecScene(t)

	path := "sip:" + token + "@" + s.pcscf.String() + ";lr"

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", path, func(r *sip.Request) {
		r.Header.Add("P-Asserted-Service", icsiForTest)
		r.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	}))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()

	tag := sip.NewTag()

	answer := sip.NewResponse(got, 183, "")
	_ = answer.Header.SetToTag(tag)
	answer.Header.Add("Contact", ueContact(u))

	for _, v := range got.Header.Values("Record-Route") {
		answer.Header.Add("Record-Route", v)
	}

	answer.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	u.us.Send(sip.UDP, f.Remote, answer)

	session, aar := pcrf.aar()

	wantStatus(t, first(s.scscf.RecvResponse()), 183)

	if aar.AFApplicationIdentifier != icsiForTest || aar.FramedIPAddress != ueAddr {
		t.Errorf("AAR %+v, want the P-Asserted-Service and the UE address", aar)
	}

	if want := subscriptionIDs([]string{testIMPU}); !slices.Equal(aar.SubscriptionIDs, want) {
		t.Errorf("Subscription-Id %+v, want the called party %+v", aar.SubscriptionIDs, want)
	}

	cd := aar.MediaComponents[0].CodecData
	if len(cd) != 2 || cd[0].Direction != rx.CodecDownlink || cd[0].Kind != rx.CodecOffer || cd[1].Direction != rx.CodecUplink {
		t.Errorf("Codec-Data %+v, want a downlink offer and an uplink answer", cd)
	}

	update := siptest.NewRequest("UPDATE", "sip:ue@"+u.us.Addr().String(), sip.UDP, s.scscf.Addr())
	update.Header.Set("From", got.Header.Get("From"))
	update.Header.Set("To", "<"+testIMPU+">;tag="+tag)
	update.Header.Set("Call-ID", got.Header.CallID())
	update.Header.Set("CSeq", "2 UPDATE")
	update.Header.Set("Contact", "<sip:caller@"+s.scscf.Addr().String()+">")

	rr := uris(got.Header.RecordRoutes())
	update.Header.Add("Route", "<"+rr[1]+">, <"+rr[0]+">")
	update.SetBody("application/sdp", sdpBody("192.0.2.9", "5000", "a=sendonly"))
	s.scscf.Send(sip.UDP, s.pcscf, update)

	upd, uf := u.us.RecvRequest()
	if upd.Method != "UPDATE" {
		t.Fatalf("UE got %s, want the UPDATE", upd.Method)
	}

	ok := sip.NewResponse(upd, 200, "")
	ok.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000", "a=recvonly"))
	u.us.Send(sip.UDP, uf.Remote, ok)

	id, mod := pcrf.aar()

	wantStatus(t, first(s.scscf.RecvResponse()), 200)

	if id != session || mod.RequestType == nil || *mod.RequestType != rx.RequestUpdate || len(mod.SpecificActions) != 0 {
		t.Errorf("AAR %s %+v, want an update of %s without Specific-Action", id, mod, session)
	}

	if fs := mod.MediaComponents[0].FlowStatus; fs == nil || *fs != rx.FlowStatusEnabledDownlink {
		t.Errorf("Flow-Status %v, want ENABLED_DOWNLINK for a UE answering recvonly", fs)
	}
}

// TS 29.214 §4.4.4: no STR without a successful initial AA-Answer.
func TestCallMediaUnansweredReleasesTheCall(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	got, f := s.originate(t, u)

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode == rx.CommandAA {
			return nil, diameter.ErrNotConnected
		}

		return succeed(req)
	})

	progress := s.coreResponse(got, 183, sip.NewTag())
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	pcrf.aar()

	if req, _ := s.scscf.RecvRequest(); req.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", req.Method)
	}

	wantStatus(t, first(u.us.RecvResponse()), 500)
	pcrf.none()
}
