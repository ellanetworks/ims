package pcscf

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
)

// newRecordScene is a scene with a registered UE whose calls the P-CSCF records.
func newRecordScene(t *testing.T) (*ipsecScene, *ue, *callrecords.Recorder) {
	t.Helper()

	var rec *callrecords.Recorder

	s, u := newIPsecRegScene(t, func(c *Config) {
		rec = callrecords.New(callrecords.Config{Store: c.Registrations.(*db.DB)})
		c.Records = rec
	})
	t.Cleanup(rec.Close)

	s.registerOverIPsec(u)

	return s, u, rec
}

// callRecords stops the recorder, and returns the records it saved.
func (s *ipsecScene) callRecords(t *testing.T, rec *callrecords.Recorder) []db.CallRecord {
	t.Helper()

	rec.Close()

	l, _, err := s.store.ListCallRecords(t.Context(), db.CallRecordFilter{}, 1, 100)
	if err != nil {
		t.Fatalf("ListCallRecords: %v", err)
	}

	return l
}

// callRecord returns the only record the recorder saved.
func (s *ipsecScene) callRecord(t *testing.T, rec *callrecords.Recorder) db.CallRecord {
	t.Helper()

	l := s.callRecords(t, rec)
	if len(l) != 1 {
		t.Fatalf("records = %+v, want one", l)
	}

	return l[0]
}

func audioVideoSDP(addr string, port string) []byte {
	return sdpBody(addr, port, "m=video "+port+"2 RTP/AVP 99", "a=rtpmap:99 H264/90000")
}

// TestCallRecordOfAnAnsweredCall follows a call that rings, is answered with audio, adds video and is ended by
// the caller.
func TestCallRecordOfAnAnsweredCall(t *testing.T) {
	s, u, rec := newRecordScene(t)

	invite := s.ueInvite(u, func(r *sip.Request) {
		r.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000"))
	})
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, first(u.us.RecvResponse()), 100)

	got, f := s.scscf.RecvRequest()
	c := &moCall{s: s, u: u, invite: invite, core: got, flow: f}

	c.answer(t, 180, "callee", nil)
	res := c.answer(t, 200, "callee", func(r *sip.Response) {
		r.SetBody("application/sdp", sdpBody("10.0.0.2", "5000"))
	})

	cseq, _ := invite.Header.CSeq()
	seq := func(n uint32) string { return strconv.FormatUint(uint64(cseq.Seq+n), 10) }

	u.uc.Send(sip.UDP, s.ps, c.request("ACK", seq(0), res))
	wantMethod(t, s.scscf, "ACK")

	// A re-INVITE adds video.
	reinvite := c.request("INVITE", seq(1), res)
	reinvite.Header.Set("Contact", ueContact(u))
	reinvite.SetBody("application/sdp", audioVideoSDP(ueAddr.String(), "4000"))
	u.uc.Send(sip.UDP, s.ps, reinvite)

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != "INVITE" {
		t.Fatalf("S-CSCF got %s, want the re-INVITE", fwd.Method)
	}

	ok := sip.NewResponse(fwd, 200, "")
	ok.Header.Add("Contact", "<sip:callee@"+s.scscf.Addr().String()+">")
	ok.SetBody("application/sdp", audioVideoSDP("10.0.0.2", "5000"))
	s.scscf.Send(ff.Transport, ff.Remote, ok)
	wantStatus(t, lastResponse(t, u), 200)

	u.uc.Send(sip.UDP, s.ps, c.request("ACK", seq(1), res))
	wantMethod(t, s.scscf, "ACK")

	bye := c.request("BYE", seq(2), res)
	u.uc.Send(sip.UDP, s.ps, bye)

	fb, fbf := s.scscf.RecvRequest()
	if fb.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", fb.Method)
	}

	s.scscf.Send(fbf.Transport, fbf.Remote, sip.NewResponse(fb, 200, ""))
	wantStatus(t, lastResponse(t, u), 200)

	r := s.callRecord(t, rec)

	if r.ICID != chargingOf(t, got.Header).icid || r.SessionID != invite.Header.CallID() {
		t.Errorf("ICID, Call-ID = %s, %s; want those of the INVITE", r.ICID, r.SessionID)
	}

	asserted := uris(got.Header.Addresses("P-Asserted-Identity"))
	if r.CallerIMPI != testIMPI || r.RequestedParty != callee || !slices.Equal(r.CallingParty, asserted) {
		t.Errorf("caller = %s %v, requested %s; want %s %v, %s", r.CallerIMPI, r.CallingParty, r.RequestedParty,
			testIMPI, asserted, callee)
	}

	if r.SIPStatus != 200 || r.Outcome != db.OutcomeAnswered || r.EndedBy != db.PartyCaller || !r.Alerted {
		t.Errorf("status %d, outcome %s, ended by %s, alerted %t; want 200, answered by the caller after ringing",
			r.SIPStatus, r.Outcome, r.EndedBy, r.Alerted)
	}

	if !slices.Equal(r.Media, []string{"audio", "video"}) {
		t.Errorf("media = %v, want audio then video", r.Media)
	}

	if r.DeliveryStartAt.Before(r.RequestedAt) || !r.DeliveryEndAt.After(r.DeliveryStartAt) || r.Incomplete {
		t.Errorf("times %v, %v, %v; want them in order, and complete", r.RequestedAt, r.DeliveryStartAt, r.DeliveryEndAt)
	}
}

func TestCallRecordOfAFailedCall(t *testing.T) {
	tests := []struct {
		name    string
		ring    bool
		code    int
		outcome db.CallOutcome
	}{
		{"busy", false, 486, db.OutcomeBusy},
		{"declined", true, 603, db.OutcomeRejected},
		{"rang out", true, 408, db.OutcomeNoAnswer},
		{"unavailable after ringing", true, 480, db.OutcomeUnavailable},
		{"unavailable", false, 480, db.OutcomeUnavailable},
		// The caller gets a 500 for a 503 (RFC 3261 §16.7).
		{"server error", false, 503, db.OutcomeFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, u, rec := newRecordScene(t)
			c := s.originatingCall(t, u)

			if tt.ring {
				c.answer(t, 180, "callee", nil)
			}

			res := c.s.coreResponse(c.core, tt.code, "callee")
			s.scscf.Send(c.flow.Transport, c.flow.Remote, res)

			sent := tt.code
			if sent == 503 {
				sent = 500
			}

			wantStatus(t, lastResponse(t, u), sent)

			r := s.callRecord(t, rec)
			if r.SIPStatus != sent || r.Outcome != tt.outcome || r.EndedBy != db.PartyCallee || r.Alerted != tt.ring {
				t.Errorf("status %d, outcome %s, ended by %s, alerted %t; want %d, %s by the callee", r.SIPStatus,
					r.Outcome, r.EndedBy, r.Alerted, sent, tt.outcome)
			}

			if r.DeliveryStartAt.IsZero() || !r.DeliveryEndAt.IsZero() {
				t.Errorf("delivery %v to %v, want only the time of the failure", r.DeliveryStartAt, r.DeliveryEndAt)
			}
		})
	}
}

func TestCallRecordOfACancelledCall(t *testing.T) {
	s, u, rec := newRecordScene(t)
	c := s.originatingCall(t, u)
	c.answer(t, 180, "callee", nil)

	cancel, err := sip.NewCancel(c.invite)
	if err != nil {
		t.Fatal(err)
	}

	u.uc.Send(sip.UDP, s.ps, cancel)

	fc, cf := s.scscf.RecvRequest()
	if fc.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", fc.Method)
	}

	s.scscf.Send(cf.Transport, cf.Remote, sip.NewResponse(fc, 200, ""))
	s.scscf.Send(c.flow.Transport, c.flow.Remote, c.s.coreResponse(c.core, 487, "callee"))

	for codes := map[int]bool{}; !codes[487]; {
		res, _ := u.us.RecvResponse()
		codes[res.StatusCode] = true
	}

	r := s.callRecord(t, rec)
	if r.SIPStatus != 487 || r.Outcome != db.OutcomeCancelled || r.EndedBy != db.PartyCaller || !r.Alerted {
		t.Errorf("status %d, outcome %s, ended by %s; want 487, cancelled by the caller", r.SIPStatus, r.Outcome, r.EndedBy)
	}
}

// TestCallRecordOfARejectedCall checks that a call the P-CSCF refuses itself is recorded as ended by the network.
func TestCallRecordOfARejectedCall(t *testing.T) {
	s, u, rec := newRecordScene(t)

	invite := s.ueInvite(u, func(r *sip.Request) { r.Header.Set("Max-Forwards", "0") })
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, lastResponse(t, u), 483)

	r := s.callRecord(t, rec)
	if r.SIPStatus != 483 || r.Outcome != db.OutcomeFailed || r.EndedBy != db.PartyNetwork || r.CallerIMPI != testIMPI {
		t.Errorf("record %+v, want a 483 from the network to the registered UE", r)
	}
}

// TestCallRecordOfARetriedAttempt checks that an attempt the UE tries again with its Call-ID, here with a longer
// session interval, leaves only the record of the retry, and that one it does not try again is kept.
func TestCallRecordOfARetriedAttempt(t *testing.T) {
	for _, retried := range []bool{true, false} {
		t.Run(fmt.Sprintf("retried %t", retried), func(t *testing.T) {
			s, u, rec := newRecordScene(t)
			c := s.originatingCall(t, u)

			s.scscf.Send(c.flow.Transport, c.flow.Remote, c.s.coreResponse(c.core, 422, "callee"))

			tooSmall := lastResponse(t, u)
			wantStatus(t, tooSmall, 422)
			wantMethod(t, s.scscf, "ACK")

			ack, err := sip.NewAck(c.invite, tooSmall)
			if err != nil {
				t.Fatal(err)
			}

			u.uc.Send(sip.UDP, s.ps, ack)

			want := 422

			if retried {
				retry := s.ueInvite(u, func(r *sip.Request) {
					r.Header.Set("Call-ID", c.invite.Header.CallID())
					r.Header.Set("From", c.invite.Header.Get("From"))
					r.Header.Set("CSeq", "2 INVITE")
				})
				u.uc.Send(sip.UDP, s.ps, retry)
				wantStatus(t, first(u.us.RecvResponse()), 100)

				got, f := s.scscf.RecvRequest()
				s.scscf.Send(f.Transport, f.Remote, s.coreResponse(got, 486, "callee"))
				wantStatus(t, lastResponse(t, u), 486)

				want = 486
			}

			r := s.callRecord(t, rec)
			if r.SessionID != c.invite.Header.CallID() || r.SIPStatus != want {
				t.Fatalf("record %+v, want the %d of the last attempt", r, want)
			}
		})
	}
}

func TestNoCallRecordForAnUnregisteredUE(t *testing.T) {
	var rec *callrecords.Recorder

	s, _ := newIPsecRegScene(t, func(c *Config) {
		rec = callrecords.New(callrecords.Config{Store: c.Registrations.(*db.DB)})
		c.Records = rec
	})
	t.Cleanup(rec.Close)

	invite := siptest.NewRequest("INVITE", callee, sip.UDP, s.ue.Addr())
	invite.Header.Set("Contact", "<sip:ue@"+s.ue.Addr().String()+">")
	s.ue.Send(sip.UDP, s.pcscf, invite)

	// The P-CSCF drops requests from unregistered UEs.
	s.ue.RecvNone(quiet)

	if l := s.callRecords(t, rec); len(l) != 0 {
		t.Fatalf("records = %+v, want none", l)
	}
}

func TestAcceptedMedia(t *testing.T) {
	answer := strings.Join([]string{
		"v=0", "o=- 1 1 IN IP4 10.0.0.2", "s=-", "c=IN IP4 10.0.0.2", "t=0 0",
		"m=audio 5000 RTP/AVP 116", "m=video 0 RTP/AVP 99", "m=audio 5002 RTP/AVP 116", "m=text 5004 RTP/AVP 98",
	}, "\r\n") + "\r\n"

	if got := acceptedMedia([]byte(answer)); !slices.Equal(got, []string{"audio", "text"}) {
		t.Fatalf("acceptedMedia = %v, want audio and text, not the rejected video", got)
	}

	if got := acceptedMedia([]byte("not SDP")); got != nil {
		t.Fatalf("acceptedMedia of no SDP = %v", got)
	}
}

// lastResponse is the next final response to the UE.
func lastResponse(t *testing.T, u *ue) *sip.Response {
	t.Helper()

	for {
		res, _ := u.us.RecvResponse()
		if res.StatusCode >= 200 {
			return res
		}
	}
}

func wantMethod(t *testing.T, s *siptest.Socket, method string) {
	t.Helper()

	if req, _ := s.RecvRequest(); req.Method != method {
		t.Fatalf("got %s, want %s", req.Method, method)
	}
}

// recordTo makes the P-CSCF of a scene report to a recorder, which has the attempt of the call coreRequest's ICID
// names, as the originating P-CSCF would have reported it.
func recordTo(rec **callrecords.Recorder) func(*Config) {
	return func(c *Config) {
		*rec = callrecords.New(callrecords.Config{Store: c.Registrations.(*db.DB)})
		(*rec).Attempt(callrecords.Attempt{ICID: "AB12", CallID: "c1", RequestURI: testIMPU, IMPI: "caller@" + homeDomain})
		c.Records = *rec
	}
}

// TestCallRecordReached checks that the terminating P-CSCF reports the UE that answers.
func TestCallRecordReached(t *testing.T) {
	var rec *callrecords.Recorder

	s, u := newIPsecRegScene(t, recordTo(&rec))
	t.Cleanup(rec.Close)

	token, _ := s.registerOverIPsec(u)

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", "sip:"+token+"@"+s.pcscf.String()+";lr", nil))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()

	ok := sip.NewResponse(got, 200, "")
	_ = ok.Header.SetToTag(sip.NewTag())
	ok.Header.Add("Contact", ueContact(u))

	for _, v := range got.Header.Values("Record-Route") {
		ok.Header.Add("Record-Route", v)
	}

	u.us.Send(sip.UDP, f.Remote, ok)
	wantStatus(t, first(s.scscf.RecvResponse()), 200)

	if r := s.callRecord(t, rec); r.CalleeIMPI != testIMPI {
		t.Fatalf("callee IMPI = %q, want %s", r.CalleeIMPI, testIMPI)
	}
}

// TestCallRecordReleasedTowardTheUE checks that a call the terminating P-CSCF releases, when the UE's signalling
// bearer is lost, is recorded as ended by the network, though its 500 reaches the caller from downstream.
func TestCallRecordReleasedTowardTheUE(t *testing.T) {
	var rec *callrecords.Recorder

	s, u, pcrf, token := newRxIPsecScene(t, recordTo(&rec))
	t.Cleanup(rec.Close)

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", "sip:"+token+"@"+s.pcscf.String()+";lr", func(r *sip.Request) {
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

	terminate, _ := s.p.rxAbortSession(session, rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	t.Cleanup(terminate)

	wantStatus(t, first(s.scscf.RecvResponse()), 500)

	// The originating P-CSCF sees a 500 from downstream.
	rec.Ended("AB12", callrecords.End{Code: 500, By: proxy.Callee, Cause: proxy.EndFailed})

	if r := s.callRecord(t, rec); r.SIPStatus != 500 || r.EndedBy != db.PartyNetwork {
		t.Fatalf("record %+v, want a 500 from the network", r)
	}
}

// TestCallRecordRefusedTowardTheUE checks that a call the terminating P-CSCF refuses itself is recorded as ended
// by the network, though its response reaches the caller from downstream.
func TestCallRecordRefusedTowardTheUE(t *testing.T) {
	var rec *callrecords.Recorder

	s, u := newIPsecRegScene(t, recordTo(&rec))
	t.Cleanup(rec.Close)

	token, _ := s.registerOverIPsec(u)

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", "sip:"+token+"@"+s.pcscf.String()+";lr", func(r *sip.Request) {
		r.Header.Set("Max-Forwards", "0")
	}))
	res := lastCoreResponse(t, s)
	wantStatus(t, res, 483)

	// The originating P-CSCF sees the 483 from downstream.
	to, _ := res.Header.To()
	rec.Ended("AB12", callrecords.End{Code: 483, Tag: to.Tag(), By: proxy.Callee, Cause: proxy.EndFailed})

	if r := s.callRecord(t, rec); r.SIPStatus != 483 || r.EndedBy != db.PartyNetwork {
		t.Fatalf("record %+v, want a 483 from the network", r)
	}
}

// TestCallRecordReachedWithAQuotedICID checks that the terminating P-CSCF reports a call whose ICID is a quoted
// string by its value (RFC 7315 §5.6).
func TestCallRecordReachedWithAQuotedICID(t *testing.T) {
	var rec *callrecords.Recorder

	s, u := newIPsecRegScene(t, recordTo(&rec))
	t.Cleanup(rec.Close)

	token, _ := s.registerOverIPsec(u)

	s.scscf.Send(sip.UDP, s.pcscf, s.coreRequest(u, "INVITE", "sip:"+token+"@"+s.pcscf.String()+";lr", func(r *sip.Request) {
		r.Header.Set("P-Charging-Vector", `icid-value="AB12";orig-ioi=other.example`)
	}))
	wantStatus(t, first(s.scscf.RecvResponse()), 100)

	got, f := u.us.RecvRequest()

	ok := sip.NewResponse(got, 200, "")
	_ = ok.Header.SetToTag(sip.NewTag())
	ok.Header.Add("Contact", ueContact(u))

	for _, v := range got.Header.Values("Record-Route") {
		ok.Header.Add("Record-Route", v)
	}

	u.us.Send(sip.UDP, f.Remote, ok)
	wantStatus(t, first(s.scscf.RecvResponse()), 200)

	if r := s.callRecord(t, rec); r.CalleeIMPI != testIMPI {
		t.Fatalf("callee IMPI = %q, want %s", r.CalleeIMPI, testIMPI)
	}
}

// lastCoreResponse is the next final response to the S-CSCF.
func lastCoreResponse(t *testing.T, s *ipsecScene) *sip.Response {
	t.Helper()

	for {
		if res := first(s.scscf.RecvResponse()); res.StatusCode >= 200 {
			return res
		}
	}
}
