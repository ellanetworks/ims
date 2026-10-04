package pcscf

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/siptest"
)

func (s *ipsecScene) ueSend(u *ue, req *sip.Request) {
	req.Header.Prepend("Via", sip.NewVia(sip.UDP, u.us.Addr()).String())
	u.uc.Send(sip.UDP, s.ps, req)
}

type callUp struct {
	invite, got *sip.Request
	f           sip.Flow
	tag         string
	session     string
	ue          *dialog.Dialog
}

// An originating call answered by the core with SDP in a 183.
func (s *ipsecScene) establishEarly(t *testing.T, u *ue, pcrf *fakePCRF) *callUp {
	t.Helper()

	return s.establish(t, u, pcrf, false)
}

// An originating call answered by the core with SDP in a 183, then a 200, and ACKed.
func (s *ipsecScene) establishConfirmed(t *testing.T, u *ue, pcrf *fakePCRF) *callUp {
	t.Helper()

	return s.establish(t, u, pcrf, true)
}

func (s *ipsecScene) establish(t *testing.T, u *ue, pcrf *fakePCRF, confirm bool) *callUp {
	t.Helper()

	invite, got, f := s.originateWith(t, u, true)
	e := &callUp{invite: invite, got: got, f: f, tag: sip.NewTag()}

	progress := s.coreResponse(got, 183, e.tag)
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	e.session, _ = pcrf.aar()

	wantStatus(t, first(u.us.RecvResponse()), 183)

	if !confirm {
		return e
	}

	s.scscf.Send(f.Transport, f.Remote, s.coreResponse(got, 200, e.tag))

	ok, _ := u.us.RecvResponse()
	wantStatus(t, ok, 200)

	var err error
	if e.ue, err = dialog.NewUAC(invite, ok); err != nil {
		t.Fatal(err)
	}

	ack, err := e.ue.NewAck(invite)
	if err != nil {
		t.Fatal(err)
	}

	s.ueSend(u, ack)

	if req, _ := s.scscf.RecvRequest(); req.Method != "ACK" {
		t.Fatalf("S-CSCF got %s, want the ACK", req.Method)
	}

	return e
}

// TS 24.229 §5.2.8.1: no cause received from the bearer layer.
func wantReason503(t *testing.T, req *sip.Request) {
	t.Helper()

	if got := req.Header.Get("Reason"); !strings.EqualFold(strings.ReplaceAll(got, " ", ""), "SIP;cause=503") {
		t.Errorf("%s Reason = %q, want SIP;cause=503", req.Method, got)
	}
}

// TS 24.229 §5.2.8.1.2: signalling bearer lost, BYE toward the remote party only.
func TestCallAbortedWhileEstablished(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishConfirmed(t, u, pcrf)

	terminate, ok := s.p.AbortSession(e.session, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if !ok {
		t.Fatal("AbortSession reported an unknown session")
	}

	bye, bf := s.scscf.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", bye.Method)
	}

	wantReason503(t, bye)
	s.scscf.Send(bf.Transport, bf.Remote, sip.NewResponse(bye, 200, ""))
	u.us.RecvNone(quiet)

	terminate()
	pcrf.wantSTR(e.session, rx.TerminationAdministrative)
	pcrf.none()
}

// TS 24.229 §5.2.8.1.1: signalling bearer lost during setup, CANCEL from the originating P-CSCF.
func TestCallAbortedDuringSetup(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	e := s.establishEarly(t, u, pcrf)

	terminate, _ := s.p.AbortSession(e.session, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})

	cancel, _ := s.scscf.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", cancel.Method)
	}

	wantReason503(t, cancel)
	u.us.RecvNone(quiet)

	terminate()
	pcrf.wantSTR(e.session, rx.TerminationAdministrative)
	pcrf.none()
}

// TS 24.229 §5.2.8.1.1: the terminating P-CSCF answers 500.
func TestTerminatingCallAbortedDuringSetup(t *testing.T) {
	s, u, pcrf, token := newRxIPsecScene(t)

	path := "sip:" + token + "@" + s.pcscf.String() + ";lr"

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", path, func(r *sip.Request) {
		r.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	}))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()

	progress := sip.NewResponse(got, 183, "")
	_ = progress.Header.SetToTag(sip.NewTag())
	progress.Header.Add("Contact", ueContact(u))

	for _, v := range got.Header.Values("Record-Route") {
		progress.Header.Add("Record-Route", v)
	}

	progress.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	u.us.Send(sip.UDP, f.Remote, progress)

	session, _ := pcrf.aar()

	wantStatus(t, first(s.scscf.RecvResponse()), 183)

	terminate, _ := s.p.AbortSession(session, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})

	wantStatus(t, first(s.scscf.RecvResponse()), 500)

	terminate()
	pcrf.wantSTR(session, rx.TerminationAdministrative)
	pcrf.none()
}

const lossTimeout = 5 * time.Second

func mediaLossClock() (*siptest.Clock, func(*Config)) {
	clk := siptest.NewClock()

	return clk, func(c *Config) {
		c.Clock = fakeClock{clk}
		c.Rx.MediaLossTimeout = lossTimeout
	}
}

func lossRAR(t *testing.T, s *ipsecScene, session string, components ...uint32) {
	t.Helper()

	r := rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}
	for _, n := range components {
		r.Flows = append(r.Flows, rx.Flows{MediaComponentNumber: n})
	}

	if !s.p.ReAuth(session, r) {
		t.Fatal("ReAuth reported an unknown session")
	}
}

// TS 24.229 §5.2.8.1.2: media bearer lost, BYE toward the remote party once the operator timer expires.
func TestCallMediaLostWhileEstablished(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishConfirmed(t, u, pcrf)

	lossRAR(t, s, e.session, 1)

	s.scscf.RecvNone(quiet)
	clk.Advance(lossTimeout)

	bye, _ := s.scscf.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", bye.Method)
	}

	wantReason503(t, bye)
	u.us.RecvNone(quiet)
}

// TS 24.229 §5.2.8.1.1: media bearer lost during setup, 500 to the INVITE and CANCEL with cause 503.
func TestCallMediaLostDuringSetup(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishEarly(t, u, pcrf)

	s.p.ReAuth(e.session, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer}})

	s.scscf.RecvNone(quiet)
	clk.Advance(lossTimeout)

	cancel, _ := s.scscf.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", cancel.Method)
	}

	wantReason503(t, cancel)
	wantStatus(t, first(u.us.RecvResponse()), 500)
}

// TS 24.229 §5.2.8.1.2: a SIP message removing the lost media within the operator time keeps the call.
func TestCallMediaLostThenRemoved(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishConfirmed(t, u, pcrf)

	lossRAR(t, s, e.session, 1)

	update, err := e.ue.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	update.Header.Add("Contact", ueContact(u))
	update.SetBody("application/sdp", sdpBody(ueAddr.String(), "0"))
	s.ueSend(u, update)

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != "UPDATE" {
		t.Fatalf("S-CSCF got %s, want the UPDATE", fwd.Method)
	}

	ok := sip.NewResponse(fwd, 200, "")
	ok.SetBody("application/sdp", sdpBody("192.0.2.9", "0"))
	s.scscf.Send(ff.Transport, ff.Remote, ok)

	_, aar := pcrf.aar()
	if fs := aar.MediaComponents[0].FlowStatus; fs == nil || *fs != rx.FlowStatusRemoved {
		t.Fatalf("Flow-Status %v, want REMOVED", fs)
	}

	wantStatus(t, first(u.us.RecvResponse()), 200)

	clk.Advance(lossTimeout)
	s.scscf.RecvNone(quiet)
}

// TS 24.229 §5.2.8.1.2: each indication counts, a later one naming the active media included.
func TestCallMediaLostIndicationsMerge(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishConfirmed(t, u, pcrf)

	lossRAR(t, s, e.session, 2)
	lossRAR(t, s, e.session, 1)

	clk.Advance(lossTimeout)

	if bye, _ := s.scscf.RecvRequest(); bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", bye.Method)
	}
}

// TS 24.229 §5.2.8.1.2: only media still authorised counts.
func TestCallMediaLostOnInactiveMedia(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishConfirmed(t, u, pcrf)

	lossRAR(t, s, e.session, 2)

	clk.Advance(lossTimeout)
	s.scscf.RecvNone(quiet)
}

// The media-loss timer stops with the call.
func TestCallMediaLossTimerStopsWithTheCall(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishConfirmed(t, u, pcrf)

	lossRAR(t, s, e.session, 1)

	bye, err := e.ue.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	s.ueSend(u, bye)

	fwd, ff := s.scscf.RecvRequest()
	s.scscf.Send(ff.Transport, ff.Remote, sip.NewResponse(fwd, 200, ""))
	wantStatus(t, first(u.us.RecvResponse()), 200)
	pcrf.wantSTR(e.session, rx.TerminationLogout)

	clk.Advance(lossTimeout)
	s.scscf.RecvNone(quiet)

	if s.p.ReAuth(e.session, rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer}}) {
		eventually(t, "the ended session to be forgotten", func() bool {
			return !s.p.ReAuth(e.session, rx.ReAuthRequest{})
		})
	}
}

// coreAnswer sends a response to the INVITE got from the core, with an SDP answer on port unless empty.
func (s *ipsecScene) coreAnswer(got *sip.Request, f sip.Flow, code int, tag, port string) {
	res := s.coreResponse(got, code, tag)
	if port != "" {
		res.SetBody("application/sdp", sdpBody("192.0.2.9", port))
	}

	s.scscf.Send(f.Transport, f.Remote, res)
}

// TS 29.214 Annex A.3
func TestForkedCallMedia(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	_, got, f := s.originateWith(t, u, true)

	s.coreAnswer(got, f, 183, "one", "5000")

	session, first183 := pcrf.aar()

	wantStatus(t, first(u.us.RecvResponse()), 183)

	s.coreAnswer(got, f, 183, "two", "6000")

	id, second := pcrf.aar()

	wantStatus(t, first(u.us.RecvResponse()), 183)

	s.coreAnswer(got, f, 200, "one", "")

	id2, final := pcrf.aar()

	wantStatus(t, first(u.us.RecvResponse()), 200)

	if id != session || id2 != session {
		t.Fatalf("sessions %s %s %s, want one", session, id, id2)
	}

	if first183.SIPForkingIndication != rx.ForkingSingleDialogue || second.SIPForkingIndication != rx.ForkingSeveralDialogues ||
		final.SIPForkingIndication != rx.ForkingSingleDialogue {
		t.Errorf("forking indications %s %s %s, want single, several, single",
			first183.SIPForkingIndication, second.SIPForkingIndication, final.SIPForkingIndication)
	}

	if d := second.MediaComponents[0].SubComponents[0].FlowDescriptions; !strings.Contains(strings.Join(d, " "), "192.0.2.9 6000") {
		t.Errorf("second flows %q, want the second early dialog's", d)
	}

	if d := final.MediaComponents[0].SubComponents[0].FlowDescriptions; !strings.Contains(strings.Join(d, " "), "192.0.2.9 5000") {
		t.Errorf("final flows %q, want the answered early dialog's", d)
	}
}

// RFC 3262 §5: the answer to an offer in a reliable 183 comes in the PRACK.
func TestCallMediaAnsweredInThePrack(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	invite, got, f := s.originateWith(t, u, false)

	tag := sip.NewTag()

	progress := s.coreResponse(got, 183, tag)
	progress.Header.Add("Require", "100rel")
	progress.Header.Add("RSeq", "1")
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	early, _ := u.us.RecvResponse()
	wantStatus(t, early, 183)
	pcrf.none()

	release := pcrf.holdAA()

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

	_, aar := pcrf.aar()

	s.scscf.RecvNone(quiet)

	release()

	if req, _ := s.scscf.RecvRequest(); req.Method != "PRACK" {
		t.Fatalf("S-CSCF got %s, want the PRACK", req.Method)
	}

	if cd := aar.MediaComponents[0].CodecData; cd[0].Direction != rx.CodecDownlink || cd[1].Direction != rx.CodecUplink {
		t.Errorf("Codec-Data %+v, want the network's offer and the UE's answer", cd)
	}
}

// RFC 3264: the answer to an offer in the 2xx comes in the ACK.
func TestCallMediaAnsweredInTheAck(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)
	invite, got, f := s.originateWith(t, u, false)

	ok := s.coreResponse(got, 200, sip.NewTag())
	ok.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, ok)

	res, _ := u.us.RecvResponse()
	wantStatus(t, res, 200)
	pcrf.none()

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

	if _, aar := pcrf.aar(); len(aar.MediaComponents) != 1 {
		t.Fatalf("AAR %+v, want the audio", aar)
	}

	if req, _ := s.scscf.RecvRequest(); req.Method != "ACK" {
		t.Fatalf("S-CSCF got %s, want the ACK", req.Method)
	}
}

// TS 29.214 §4.4.1: the same service information is not sent again within the Retry-Interval.
func TestCallMediaRetryInterval(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)

	pcrf.answerWith(refuseAAWith(tgpp.ResultRequestedServiceTemporarilyNotAuthorized, time.Minute))

	for i := range 2 {
		got, f := s.originate(t, u)

		s.coreAnswer(got, f, 183, sip.NewTag(), "5000")

		if i == 0 {
			pcrf.aar()
		}

		if req, _ := s.scscf.RecvRequest(); req.Method != "CANCEL" {
			t.Fatalf("call %d: S-CSCF got %s, want the CANCEL", i, req.Method)
		}

		wantStatus(t, first(u.us.RecvResponse()), 500)
	}

	pcrf.none()
}

// TS 24.229 §5.2.7.2, §7.2A.5.2.7
func TestCallAccessNetworkChargingInfo(t *testing.T) {
	s, u, pcrf, _ := newRxIPsecScene(t)

	pcrf.answerWith(func(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
		if req.CommandCode != rx.CommandAA {
			return succeed(req)
		}

		return rx.NewAAAnswer(req, pcrfIdentity, rx.AAAnswer{
			AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{
				{Value: []byte{0xab, 0x01}, Flows: []rx.Flows{{MediaComponentNumber: 1}}},
			},
			AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.50"),
		})
	})

	e := s.establishEarly(t, u, pcrf)

	ok := s.coreResponse(e.got, 200, e.tag)
	s.scscf.Send(e.f.Transport, e.f.Remote, ok)

	res, _ := u.us.RecvResponse()

	ud, err := dialog.NewUAC(e.invite, res)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := ud.NewAck(e.invite)
	if err != nil {
		t.Fatal(err)
	}

	s.ueSend(u, ack)

	fwd, _ := s.scscf.RecvRequest()

	want := `pdngw=192.0.2.50;eps-info="eps-item=1;eps-sig=no;ecid=AB01;flow-id=({1,1},{1,2})"`
	if cv := fwd.Header.Get("P-Charging-Vector"); !strings.HasSuffix(cv, want) {
		t.Errorf("%s P-Charging-Vector = %q, want it to end with %s", fwd.Method, cv, want)
	}

	bye, err := ud.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	s.ueSend(u, bye)

	if fwd, _ := s.scscf.RecvRequest(); strings.Contains(fwd.Header.Get("P-Charging-Vector"), "pdngw") {
		t.Errorf("BYE P-Charging-Vector = %q, want the information only once", fwd.Header.Get("P-Charging-Vector"))
	}
}
