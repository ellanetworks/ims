package pcscf

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/siptest"
)

const mcpttICSI = "urn:urn-7:3gpp-service.ims.icsi.mcptt"

// mcpttAA answers the AA-Requests as a PCRF that supports MCPTT (TS 29.214 §5.4.1).
func mcpttAA(_ context.Context, req *diameter.Message) (*diameter.Message, error) {
	if req.CommandCode == rx.CommandAA {
		return rx.NewAAAnswer(req, pcrfIdentity, rx.AAAnswer{Features: rx.FeatureRel8 | rx.FeatureMCPTT})
	}

	return succeed(req)
}

func newMCPTTScene(t *testing.T) (*ipsecScene, *ue, *fakePCRF, string) {
	t.Helper()

	s, u, pcrf, token := newRxIPsecScene(t)
	pcrf.answerWith(mcpttAA)

	return s, u, pcrf, token
}

func wantPriority(t *testing.T, aar rx.AARequest, namespace string, level rx.ReservationPriority) {
	t.Helper()

	if aar.MCPTTIdentifier != namespace || aar.ReservationPriority == nil || *aar.ReservationPriority != level {
		t.Errorf("MCPTT-Identifier %q, Reservation-Priority %v; want %q, %s", aar.MCPTTIdentifier, aar.ReservationPriority,
			namespace, level)
	}
}

// TS 24.229 §4.4.6, §5.1: Resource-Priority crosses the P-CSCF both ways between the UE and the core.
func wantResourcePriority(t *testing.T, h sip.Header, want string) {
	t.Helper()

	if got := h.Get("Resource-Priority"); got != want {
		t.Errorf("Resource-Priority %q, want %q forwarded", got, want)
	}
}

func wantNoPriority(t *testing.T, aar rx.AARequest) {
	t.Helper()

	if aar.MCPTTIdentifier != "" || aar.ReservationPriority != nil {
		t.Errorf("MCPTT-Identifier %q, Reservation-Priority %v; want none", aar.MCPTTIdentifier, aar.ReservationPriority)
	}
}

// coreInDialog sends a request of the core in the originating call e, and returns it as the UE gets it.
func (s *ipsecScene) coreInDialog(t *testing.T, u *ue, e *callUp, method string, cseq int, edit func(*sip.Request)) (*sip.Request, sip.Flow) {
	t.Helper()

	req := siptest.NewRequest(method, "sip:callee@"+s.scscf.Addr().String(), sip.UDP, s.scscf.Addr())
	req.Header.Set("From", "<"+callee+">;tag="+e.tag)
	req.Header.Set("To", e.got.Header.Get("From"))
	req.Header.Set("Call-ID", e.got.Header.CallID())
	req.Header.Set("CSeq", strconv.Itoa(cseq)+" "+method)
	req.Header.Set("Contact", "<sip:callee@"+s.scscf.Addr().String()+">")

	rr := uris(e.got.Header.RecordRoutes())
	req.Header.Add("Route", "<"+rr[0]+">, <"+rr[1]+">")

	if edit != nil {
		edit(req)
	}

	s.scscf.Send(sip.UDP, s.pcscf, req)

	got, f := u.us.RecvRequest()
	if got.Method != method {
		t.Fatalf("UE got %s, want the %s", got.Method, method)
	}

	return got, f
}

// ueAnswers answers a request the UE got with a 200, with an SDP answer unless nil, and waits for the core to get
// it.
func (s *ipsecScene) ueAnswers(t *testing.T, u *ue, req *sip.Request, f sip.Flow, answer []byte) {
	t.Helper()

	ok := sip.NewResponse(req, 200, "")
	ok.Header.Add("Contact", ueContact(u))

	if answer != nil {
		ok.SetBody("application/sdp", answer)
	}

	u.us.Send(sip.UDP, f.Remote, ok)

	for {
		res, _ := s.scscf.RecvResponse()
		if res.StatusCode != 100 {
			wantStatus(t, res, 200)
			return
		}
	}
}

// TS 29.214 Annex A.13.2, TS 24.229 §3.1: the Resource-Priority of an INVITE from the trust domain is authorised;
// the initial AA-Request carries its MCPTT namespace and priority value, and the MCPTT ICSI.
func TestAnnexA13MCPTTPriorityOfTheTerminatingInvite(t *testing.T) {
	s, u, pcrf, token := newMCPTTScene(t)

	path := "sip:" + token + "@" + s.pcscf.String() + ";lr"

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", path, func(r *sip.Request) {
		r.Header.Add("P-Asserted-Service", mcpttICSI)
		r.Header.Add("Resource-Priority", "mcpttp.11")
		r.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	}))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()
	wantResourcePriority(t, got.Header, "mcpttp.11")

	answer := sip.NewResponse(got, 183, "")
	_ = answer.Header.SetToTag(sip.NewTag())
	answer.Header.Add("Contact", ueContact(u))
	answer.Header.Add("Resource-Priority", "mcpttp.15")

	for _, v := range got.Header.Values("Record-Route") {
		answer.Header.Add("Record-Route", v)
	}

	answer.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	u.us.Send(sip.UDP, f.Remote, answer)

	_, aar := pcrf.aar()

	progressed, _ := s.scscf.RecvResponse()
	wantStatus(t, progressed, 183)
	wantResourcePriority(t, progressed.Header, "mcpttp.15")

	if *aar.RequestType != rx.RequestInitial || aar.AFApplicationIdentifier != mcpttICSI {
		t.Errorf("AAR %s for %q, want an initial one for the MCPTT ICSI", aar.RequestType, aar.AFApplicationIdentifier)
	}

	wantPriority(t, aar, "mcpttp", 11)
	pcrf.none()
}

// TS 29.214 Annex A.1 NOTE 3, Annex A.13.2, TS 24.229 §3.1: an originating call takes the ICSI of the Feature-Caps
// and the authorised Resource-Priority of the responses from the trust domain, not the UE's own.
func TestAnnexA13MCPTTPriorityOfTheOriginatingResponse(t *testing.T) {
	s, u, pcrf, _ := newMCPTTScene(t)

	invite := s.ueInvite(u, func(r *sip.Request) {
		r.Header.Add("P-Preferred-Identity", "<"+testTel+">")
		r.Header.Add("Resource-Priority", "mcpttp.15")
		r.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	})
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, first(u.us.RecvResponse()), 100)

	got, f := s.scscf.RecvRequest()
	wantResourcePriority(t, got.Header, "mcpttp.15")

	tag := sip.NewTag()

	ringing := s.coreResponse(got, 180, tag)
	ringing.Header.Add("Feature-Caps", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt"`)
	s.scscf.Send(f.Transport, f.Remote, ringing)
	wantStatus(t, first(u.us.RecvResponse()), 180)
	pcrf.none()

	progress := s.coreResponse(got, 183, tag)
	progress.Header.Add("Resource-Priority", "mcpttp.11")
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	_, aar := pcrf.aar()

	progressed, _ := u.us.RecvResponse()
	wantStatus(t, progressed, 183)
	wantResourcePriority(t, progressed.Header, "mcpttp.11")

	if *aar.RequestType != rx.RequestInitial || aar.AFApplicationIdentifier != mcpttICSI {
		t.Errorf("AAR %s for %q, want an initial one for the MCPTT ICSI of the 180", aar.RequestType, aar.AFApplicationIdentifier)
	}

	wantPriority(t, aar, "mcpttp", 11)
}

// TS 24.229 §3.1, §5.2.9.1: the P-CSCF knows no priority privileges of its users, so a Resource-Priority from the
// UE is not authorised: it asks for no priority, at the call's set-up or later.
func TestAnnexA13MCPTTPriorityFromTheUEIsNotAuthorised(t *testing.T) {
	s, u, pcrf, _ := newMCPTTScene(t)

	invite := s.ueInvite(u, func(r *sip.Request) {
		r.Header.Add("P-Preferred-Identity", "<"+testTel+">")
		r.Header.Add("Resource-Priority", "mcpttp.15")
		r.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	})
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, first(u.us.RecvResponse()), 100)

	got, f := s.scscf.RecvRequest()
	tag := sip.NewTag()

	progress := s.coreResponse(got, 183, tag)
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	_, aar := pcrf.aar()

	wantStatus(t, first(u.us.RecvResponse()), 183)
	wantNoPriority(t, aar)

	s.scscf.Send(f.Transport, f.Remote, s.coreResponse(got, 200, tag))

	answered, _ := u.us.RecvResponse()
	wantStatus(t, answered, 200)

	d, err := dialog.NewUAC(invite, answered)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := d.NewAck(invite)
	if err != nil {
		t.Fatal(err)
	}

	s.ueSend(u, ack)

	if req, _ := s.scscf.RecvRequest(); req.Method != "ACK" {
		t.Fatalf("S-CSCF got %s, want the ACK", req.Method)
	}

	e := &callUp{ue: d}

	req, err := e.ue.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Add("Contact", ueContact(u))
	req.Header.Add("Resource-Priority", "mcpttp.15")
	req.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000", "a=sendonly"))
	s.ueSend(u, req)

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != "UPDATE" {
		t.Fatalf("S-CSCF got %s, want the UPDATE", fwd.Method)
	}

	wantResourcePriority(t, fwd.Header, "mcpttp.15")

	ok := sip.NewResponse(fwd, 200, "")
	ok.Header.Add("Contact", "<sip:callee@"+s.scscf.Addr().String()+">")
	ok.SetBody("application/sdp", sdpBody("192.0.2.9", "5000", "a=recvonly"))
	s.scscf.Send(ff.Transport, ff.Remote, ok)

	_, aar = pcrf.aar()

	wantStatus(t, first(u.us.RecvResponse()), 200)
	wantNoPriority(t, aar)

	req, err = e.ue.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Add("Contact", ueContact(u))
	req.Header.Add("Resource-Priority", "mcpttp.15")
	s.ueSend(u, req)

	fwd, ff = s.scscf.RecvRequest()
	s.scscf.Send(ff.Transport, ff.Remote, sip.NewResponse(fwd, 200, ""))
	wantStatus(t, first(u.us.RecvResponse()), 200)
	pcrf.none()
}

// TS 29.214 Annex A.13.1 NOTE 1, TS 24.229 §5.2.9.2, TS 24.379 §6.3.3.1.8: a normal MCPTT call made an emergency
// call by a re-INVITE, and normal again by an UPDATE without SDP; each change of the authorised Resource-Priority
// updates the session at once, with the media authorised so far.
func TestAnnexA13MCPTTPriorityUpgradeAndDowngrade(t *testing.T) {
	s, u, pcrf, _ := newMCPTTScene(t)

	invite, got, f := s.originateBody(t, u, sdpBody(ueAddr.String(), "4000"))
	e := &callUp{invite: invite, got: got, f: f, tag: sip.NewTag()}

	progress := s.coreResponse(got, 183, e.tag)
	progress.Header.Add("Resource-Priority", "mcpttp.11")
	progress.SetBody("application/sdp", sdpBody("192.0.2.9", "5000"))
	s.scscf.Send(f.Transport, f.Remote, progress)

	session, aar := pcrf.aar()

	wantStatus(t, first(u.us.RecvResponse()), 183)
	wantPriority(t, aar, "mcpttp", 11)

	final := s.coreResponse(got, 200, e.tag)
	final.Header.Add("Resource-Priority", "mcpttp.11")
	s.scscf.Send(f.Transport, f.Remote, final)
	wantStatus(t, first(u.us.RecvResponse()), 200)
	pcrf.none()

	reinvite, rf := s.coreInDialog(t, u, e, "INVITE", 2, func(r *sip.Request) {
		r.Header.Add("Resource-Priority", "mcpttp.15")
		r.SetBody("application/sdp", sdpBody("192.0.2.9", "5002"))
	})

	wantResourcePriority(t, reinvite.Header, "mcpttp.15")

	id, up := pcrf.aar()
	if id != session || *up.RequestType != rx.RequestUpdate || len(up.MediaComponents) != 1 ||
		!slices.Contains(up.MediaComponents[0].SubComponents[0].FlowDescriptions, "permit in 17 from 127.0.0.2 to 192.0.2.9 5000") {
		t.Errorf("AAR %s %s %+v, want an update of %s with the media authorised so far", id, up.RequestType, up.MediaComponents, session)
	}

	wantPriority(t, up, "mcpttp", rx.ReservationPriorityFifteen)

	s.ueAnswers(t, u, reinvite, rf, sdpBody(ueAddr.String(), "4000"))

	_, media := pcrf.aar()
	if !slices.Contains(media.MediaComponents[0].SubComponents[0].FlowDescriptions, "permit in 17 from 127.0.0.2 to 192.0.2.9 5002") {
		t.Errorf("Media-Component-Description %+v, want the new media", media.MediaComponents)
	}

	wantPriority(t, media, "mcpttp", rx.ReservationPriorityFifteen)

	update, uf := s.coreInDialog(t, u, e, "UPDATE", 3, func(r *sip.Request) {
		r.Header.Add("Resource-Priority", "mcpttp.11")
	})

	id, down := pcrf.aar()
	if id != session || *down.RequestType != rx.RequestUpdate || len(down.MediaComponents) != 1 ||
		!slices.Contains(down.MediaComponents[0].SubComponents[0].FlowDescriptions, "permit in 17 from 127.0.0.2 to 192.0.2.9 5002") {
		t.Errorf("AAR %s %s %+v, want an update of %s with the media of the re-INVITE", id, down.RequestType, down.MediaComponents, session)
	}

	wantPriority(t, down, "mcpttp", 11)

	s.ueAnswers(t, u, update, uf, nil)

	same, sf := s.coreInDialog(t, u, e, "UPDATE", 4, func(r *sip.Request) {
		r.Header.Add("Resource-Priority", "mcpttp.11")
	})
	s.ueAnswers(t, u, same, sf, nil)
	pcrf.none()
}

// TS 29.214 Annex A.13.1 NOTE 1, Annex A.13.2, TS 24.229 §5.2.9.1: the authorised Resource-Priority of a response
// to the UE's request, without SDP, in the mcpttq namespace, its lowest value DEFAULT.
func TestAnnexA13MCPTTQueueingPriorityWithoutSDP(t *testing.T) {
	s, u, pcrf, _ := newMCPTTScene(t)
	e := s.establish(t, u, pcrf, true, sdpBody(ueAddr.String(), "4000"), sdpBody("192.0.2.9", "5000"))

	req, err := e.ue.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Add("Contact", ueContact(u))
	s.ueSend(u, req)

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != "UPDATE" {
		t.Fatalf("S-CSCF got %s, want the UPDATE", fwd.Method)
	}

	ok := sip.NewResponse(fwd, 200, "")
	ok.Header.Add("Contact", "<sip:callee@"+s.scscf.Addr().String()+">")
	ok.Header.Add("Resource-Priority", "mcpttq.0")
	s.scscf.Send(ff.Transport, ff.Remote, ok)

	wantStatus(t, first(u.us.RecvResponse()), 200)

	id, aar := pcrf.aar()
	if id != e.session || *aar.RequestType != rx.RequestUpdate || len(aar.MediaComponents) != 1 {
		t.Errorf("AAR %s %s %+v, want an update of %s with the media", id, aar.RequestType, aar.MediaComponents, e.session)
	}

	wantPriority(t, aar, "mcpttq", rx.ReservationPriorityDefault)
}

// TS 29.214 Annex A.13.2: only the MCPTT namespaces and their priority values 0 to 15 count.
func TestAnnexA13MCPTTPriorityValues(t *testing.T) {
	for _, tc := range []struct {
		rp        []string
		namespace string
		level     uint8
	}{
		{rp: []string{"mcpttp.0"}, namespace: "mcpttp"},
		{rp: []string{"MCPTTQ.15"}, namespace: "mcpttq", level: 15},
		{rp: []string{"esnet.1, mcpttq.3"}, namespace: "mcpttq", level: 3},
		{rp: []string{"wps.2", "mcpttp.9"}, namespace: "mcpttp", level: 9},
		{rp: []string{"mcpttp.16"}},
		{rp: []string{"mcpttp.x"}},
		{rp: []string{"ets.0"}},
		{rp: []string{"mcpttp.1, mcpttp.2"}},
		{},
	} {
		var h sip.Header
		for _, v := range tc.rp {
			h.Add("Resource-Priority", v)
		}

		p := mcpttPriority(h)

		switch {
		case tc.namespace == "" && p != nil:
			t.Errorf("%q: %+v, want none", tc.rp, *p)
		case tc.namespace != "" && (p == nil || p.Namespace != tc.namespace || p.Level != tc.level):
			t.Errorf("%q: %+v, want %s.%d", tc.rp, p, tc.namespace, tc.level)
		}
	}
}

// TS 29.214 Annex A.13.1 NOTE 3, Annex A.1 NOTE 3, TS 24.229 §7.2A.8.2: the percent-encoded MCPTT ICSI of
// Feature-Caps.
func TestAnnexA13MCPTTICSI(t *testing.T) {
	h := sip.Header{{Name: "Feature-Caps", Value: `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt"`}}

	if got := icsiRef(h); got != mcpttICSI {
		t.Fatalf("icsiRef = %q, want %q", got, mcpttICSI)
	}
}
