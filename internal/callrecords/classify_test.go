package callrecords

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip/proxy"
)

func TestOutcome(t *testing.T) {
	tests := []struct {
		status  int
		by      db.CallParty
		alerted bool
		want    db.CallOutcome
	}{
		{200, db.PartyCaller, false, db.OutcomeAnswered},
		{202, db.PartyNetwork, false, db.OutcomeAnswered},
		{487, db.PartyCaller, true, db.OutcomeCancelled},
		{487, db.PartyCallee, true, db.OutcomeFailed},
		{487, db.PartyNetwork, false, db.OutcomeFailed},
		{486, db.PartyCallee, false, db.OutcomeBusy},
		{600, db.PartyCallee, true, db.OutcomeBusy},
		{603, db.PartyCallee, true, db.OutcomeRejected},
		{408, db.PartyCallee, true, db.OutcomeNoAnswer},
		{408, db.PartyNetwork, true, db.OutcomeNoAnswer},
		{480, db.PartyCallee, true, db.OutcomeUnavailable},
		{480, db.PartyNetwork, true, db.OutcomeUnavailable},
		{408, db.PartyCallee, false, db.OutcomeFailed},
		{480, db.PartyNetwork, false, db.OutcomeUnavailable},
		{403, db.PartyNetwork, false, db.OutcomeFailed},
		{404, db.PartyNetwork, false, db.OutcomeFailed},
		{380, db.PartyNetwork, false, db.OutcomeFailed},
		{500, db.PartyNetwork, false, db.OutcomeFailed},
		{503, db.PartyCallee, true, db.OutcomeFailed},
		{488, db.PartyCallee, false, db.OutcomeFailed},
	}

	for _, tt := range tests {
		if got := Outcome(tt.status, tt.by, tt.alerted); got != tt.want {
			t.Errorf("Outcome(%d, %s, alerted %t) = %s, want %s", tt.status, tt.by, tt.alerted, got, tt.want)
		}
	}
}

func TestEndedBy(t *testing.T) {
	tests := []struct {
		cause proxy.EndCause
		by    proxy.Side
		want  db.CallParty
	}{
		{proxy.EndBye, proxy.Caller, db.PartyCaller},
		{proxy.EndBye, proxy.Callee, db.PartyCallee},
		{proxy.EndFailed, proxy.Caller, db.PartyCaller},
		{proxy.EndFailed, proxy.Callee, db.PartyCallee},
		{proxy.EndFailed, 0, db.PartyNetwork},
		{proxy.EndReleased, 0, db.PartyNetwork},
		{proxy.EndLost, proxy.Callee, db.PartyNetwork},
		{proxy.EndLost, 0, db.PartyNetwork},
		{proxy.EndNoAck, 0, db.PartyNetwork},
		{proxy.EndExpired, 0, db.PartyNetwork},
		{proxy.EndDiscarded, 0, db.PartyNetwork},
	}

	for _, tt := range tests {
		if got := EndedBy(tt.cause, tt.by); got != tt.want {
			t.Errorf("EndedBy(%s, %s) = %s, want %s", tt.cause, tt.by, got, tt.want)
		}
	}
}

func TestDuration(t *testing.T) {
	start := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	answered := db.CallRecord{Outcome: db.OutcomeAnswered, DeliveryStartAt: start, DeliveryEndAt: start.Add(90 * time.Second)}
	if d, ok := Duration(answered); !ok || d != 90*time.Second {
		t.Fatalf("Duration of an ended call = %v, %t; want 1m30s", d, ok)
	}

	inCall := answered
	inCall.DeliveryEndAt = time.Time{}

	if _, ok := Duration(inCall); ok {
		t.Fatal("a call in progress has a duration")
	}

	if _, ok := Duration(db.CallRecord{Outcome: db.OutcomeBusy, DeliveryStartAt: start}); ok {
		t.Fatal("an unanswered call has a duration")
	}
}
