package scscf

import (
	"slices"
	"testing"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

const testICID = "AB12CD34"

// recorded makes the S-CSCF report to a recorder, which has the attempt of the call testICID names, as the
// originating P-CSCF would have reported it.
func (sh *sessionHarness) recorded(t *testing.T) *callrecords.Recorder {
	t.Helper()

	rec := callrecords.New(callrecords.Config{Store: sh.cfg.DB})
	t.Cleanup(rec.Close)

	sh.cfg.Records = rec
	sh.restart()

	rec.Attempt(callrecords.Attempt{ICID: testICID, CallID: "c1", RequestURI: remoteTel, IMPI: testIMPI, Asserted: []string{testMSISDN}})

	return rec
}

// charged is a request in the call testICID names.
func charged(req *sip.Request) *sip.Request {
	req.Header.Set("P-Charging-Vector", "icid-value="+testICID+";orig-ioi="+homeDomain)
	return req
}

func (sh *sessionHarness) callRecord(t *testing.T, rec *callrecords.Recorder) db.CallRecord {
	t.Helper()

	rec.Close()

	r, _, err := sh.cfg.DB.ListCallRecords(t.Context(), db.CallRecordFilter{}, 1, 10)
	if err != nil || len(r) != 1 {
		t.Fatalf("ListCallRecords = %+v, %v; want one record", r, err)
	}

	return r[0]
}

func TestCallRecordRouted(t *testing.T) {
	sh := newSessionHarness(t)
	sh.numbering = Numbering{CountryCode: "1", NationalPrefix: "1"}
	rec := sh.recorded(t)

	dialled := "tel:15559990000;phone-context=" + homeDomain
	sh.orig.Send(sip.UDP, sh.scscf, charged(sh.originating("INVITE", dialled, "<"+testMSISDN+">")))

	got, _ := sh.icscf.RecvRequest()

	r := sh.callRecord(t, rec)
	if r.CalledParty != remoteTel || r.RequestedParty != remoteTel {
		t.Errorf("called party %s, want the normalised %s", r.CalledParty, remoteTel)
	}

	want := asserted(t, got.Header)
	for i, a := range want {
		addr, _ := sip.ParseAddress(a)
		want[i] = addr.URI.String()
	}

	if !slices.Equal(r.CallingParty, want) {
		t.Errorf("calling party %v, want the S-CSCF's asserted identities %v", r.CallingParty, want)
	}
}

// TestCallRecordRejected checks that a call the S-CSCF refuses is recorded as ended by the network.
func TestCallRecordRejected(t *testing.T) {
	tests := []struct {
		name    string
		req     func(sh *sessionHarness) *sip.Request
		code    int
		outcome db.CallOutcome
	}{
		{"local number", func(sh *sessionHarness) *sip.Request {
			return sh.originating("INVITE", "tel:999;phone-context="+homeDomain, "<"+testMSISDN+">")
		}, 404, db.OutcomeFailed},
		{"barred identity", func(sh *sessionHarness) *sip.Request {
			return sh.originating("INVITE", remoteTel, "<"+testIMPU+">")
		}, 403, db.OutcomeFailed},
		{"callee not registered", func(sh *sessionHarness) *sip.Request {
			return sh.terminating("INVITE", "tel:+15551239876")
		}, 480, db.OutcomeUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := newSessionHarness(t)
			rec := sh.recorded(t)

			req := charged(tt.req(sh))

			from := sh.orig
			if req.Header.Get("Route") == "<sip:"+sh.scscf.String()+";lr>" {
				from = sh.icscf
			}

			from.Send(sip.UDP, sh.scscf, req)

			res := final(t, from)
			if res.StatusCode != tt.code {
				t.Fatalf("got %s, want %d", res.StartLine(), tt.code)
			}

			// The originating P-CSCF relays it to the caller.
			to, _ := res.Header.To()
			rec.Ended(testICID, callrecords.End{Code: tt.code, Tag: to.Tag(), By: proxy.Callee, Cause: proxy.EndFailed})

			r := sh.callRecord(t, rec)
			if r.SIPStatus != tt.code || r.EndedBy != db.PartyNetwork || r.Outcome != tt.outcome {
				t.Errorf("record %+v, want a %d from the network, %s", r, tt.code, tt.outcome)
			}
		})
	}
}

// TestCallRecordReleasedOnDeregistration checks that a call the S-CSCF releases is recorded as ended by the
// network, though the BYE reaches the caller as if from the callee.
func TestCallRecordReleasedOnDeregistration(t *testing.T) {
	sh := newSessionHarness(t)
	rec := sh.recorded(t)

	sh.orig.Send(sip.UDP, sh.scscf, charged(sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")))

	invite, f := sh.icscf.RecvRequest()
	reply(t, sh.icscf, invite, f, 200, "callee")

	ok := final(t, sh.orig)

	rec.Answered(testICID, 200)

	ack := sh.inDialog("ACK", invite, ok, ok.Header.Values("Record-Route")[0])
	ack.Header.Set("CSeq", "1 ACK")
	sh.orig.Send(sip.UDP, sh.scscf, ack)

	if req, _ := sh.icscf.RecvRequest(); req.Method != "ACK" {
		t.Fatalf("callee got %s, want the ACK", req.StartLine())
	}

	sh.ue.register(registerOptions{expires: "0"})

	bye(t, sh.icscf)
	bye(t, sh.orig)

	// The originating P-CSCF sees a BYE from the callee's side.
	rec.Ended(testICID, callrecords.End{Code: 200, By: proxy.Callee, Cause: proxy.EndBye})

	if r := sh.callRecord(t, rec); r.Outcome != db.OutcomeAnswered || r.EndedBy != db.PartyNetwork {
		t.Fatalf("record %+v, want an answered call ended by the network", r)
	}
}
