//go:build linux && (amd64 || arm64)

package integration

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/testue"
)

// callRecord waits for the call c made to end in the call records, and returns its record, which must be the
// only one of the call: the nodes on its path all add to one record.
func (s *scene) callRecord(c *testue.Call) api.CallRecordResponse {
	s.t.Helper()

	var found []api.CallRecordResponse

	eventually(s.t, "the call ended in the call records", func() bool {
		found = nil

		for _, r := range s.callRecords() {
			if r.SessionID == callID(c) {
				found = append(found, r)
			}
		}

		return len(found) == 1 && !found[0].InProgress
	})

	return found[0]
}

func (s *scene) callRecords() []api.CallRecordResponse {
	s.t.Helper()

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodGet,
		"http://"+s.srv.APIAddr().String()+"/api/v1/call-records?per_page=100", nil)
	if err != nil {
		s.t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	var body struct {
		Result api.ListCallRecordsResponse `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		s.t.Fatal(err)
	}

	return body.Result.Items
}

// TestCallRecordOfACall follows a call between two UEs through both P-CSCFs, the S-CSCF and the I-CSCF, to the
// one record of it.
func TestCallRecordOfACall(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac, _ := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

	if err := ac.Bye(ctx); err != nil {
		t.Fatalf("Bye: %v", err)
	}

	ended(t, ac, testue.LocalBye)

	r := s.callRecord(ac)

	caller, callee := subscriberAt(0), subscriberAt(1)

	if r.CallerIMPI != caller.impi || !slices.Contains(r.CallingParty, caller.tel) {
		t.Errorf("caller %s %v, want %s with %s", r.CallerIMPI, r.CallingParty, caller.impi, caller.tel)
	}

	if r.RequestedParty != phone(1) || r.CalleeIMPI != callee.impi {
		t.Errorf("callee %s %s, want %s %s", r.RequestedParty, r.CalleeIMPI, phone(1), callee.impi)
	}

	if r.SIPStatus != 200 || r.Outcome != "answered" || r.EndedBy != "caller" || !r.Alerted || r.Incomplete {
		t.Errorf("record %+v, want answered after ringing, ended by the caller", r)
	}

	if !slices.Contains(r.Media, "audio") || r.DurationMS == nil || r.DeliveryEndAt == "" {
		t.Errorf("media %v, duration %v; want an audio call that lasted", r.Media, r.DurationMS)
	}
}

func TestCallRecordOfABusyCall(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac := invite(t, a, phone(1), testue.CallOptions{})

	if err := incoming(t, b, ac).Reject(486); err != nil {
		t.Fatal(err)
	}

	failed(t, ctx, ac, 486)

	if r := s.callRecord(ac); r.SIPStatus != 486 || r.Outcome != "busy" || r.EndedBy != "callee" {
		t.Fatalf("record %+v, want busy, ended by the callee", r)
	}
}

// TestCallRecordOfACallToAnUnknownNumber checks the record of a call the IMS refuses itself.
func TestCallRecordOfACallToAnUnknownNumber(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{})

	ac := invite(t, a, "sip:+15559876543@"+domain+";user=phone", testue.CallOptions{})
	failed(t, s.ctx(), ac, 404)

	if r := s.callRecord(ac); r.SIPStatus != 404 || r.Outcome != "failed" || r.EndedBy != "network" {
		t.Fatalf("record %+v, want a 404 from the network", r)
	}
}

// TestCallRecordOfACallAnsweredByASecondDevice checks that the record names the device that answered.
func TestCallRecordOfACallAnsweredByASecondDevice(t *testing.T) {
	s := newScene(t)
	d := twoDevices(t, s, testue.Config{}, testue.Config{})

	ctx := s.ctx()
	ac := invite(t, d.caller, phone(1), testue.CallOptions{})
	pc, sc := d.ringing(t, ctx, ac)

	if err := sc.Answer(ctx); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v, want 200", res, err)
	}

	ended(t, pc, testue.Cancelled)

	if err := sc.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, ac, testue.RemoteBye)

	if r := s.callRecord(ac); r.CalleeIMPI != subscriberAt(4).impi || r.EndedBy != "callee" {
		t.Fatalf("record %+v, want answered by %s, ended by the callee", r, subscriberAt(4).impi)
	}
}

// TestCallRecordOfACallReleasedOnDeregistration checks that a call the IMS releases is recorded as ended by the
// network, though the caller gets the BYE from the callee's side.
func TestCallRecordOfACallReleasedOnDeregistration(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac, _ := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

	if err := b.Deregister(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, ac, testue.RemoteBye)

	if r := s.callRecord(ac); r.Outcome != "answered" || r.EndedBy != "network" {
		t.Fatalf("record %+v, want an answered call ended by the network", r)
	}
}
