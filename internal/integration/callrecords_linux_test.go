//go:build linux && (amd64 || arm64)

package integration

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/testue"
)

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

	res, err := apitest.Client(s.srv.APIAddr().String()).Do(req)
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

func TestCallRecordOfACallToAnUnknownNumber(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{})

	ac := invite(t, a, "sip:+15559876543@"+domain+";user=phone", testue.CallOptions{})
	failed(t, s.ctx(), ac, 404)

	if r := s.callRecord(ac); r.SIPStatus != 404 || r.Outcome != "failed" || r.EndedBy != "network" {
		t.Fatalf("record %+v, want a 404 from the network", r)
	}
}

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
