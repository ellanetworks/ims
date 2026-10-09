package callrecords

import (
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip/proxy"
)

// Outcome is how a call ended, from the final status of its INVITE, the party that ended it and whether a 180
// reached the caller (RFC 3261 §21).
func Outcome(status int, by db.CallParty, alerted bool) db.CallOutcome {
	switch {
	case status >= 200 && status < 300:
		return db.OutcomeAnswered
	case status == 487 && by == db.PartyCaller:
		return db.OutcomeCancelled
	case status == 486 || status == 600:
		return db.OutcomeBusy
	case status == 603:
		return db.OutcomeRejected
	case status == 480:
		// The callee is not reachable: not registered, not taking calls, or no longer among the contacts rung.
		return db.OutcomeUnavailable
	case status == 408 && alerted:
		// The callee rang until a proxy gave up on it.
		return db.OutcomeNoAnswer
	}

	return db.OutcomeFailed
}

// Retried reports whether the final status of an INVITE is one a UE answers with a new INVITE, a new attempt of
// the same call: a challenge (RFC 3261 §21.4.2, §21.4.8), an extension the request lacks or must not use
// (§21.4.15, §21.4.16), an interval too brief (§21.4.17), a session interval too small (RFC 4028 §6) or a security
// agreement required (RFC 3329 §2.3.1).
func Retried(status int) bool {
	switch status {
	case 401, 407, 420, 421, 422, 423, 494:
		return true
	}

	return false
}

// EndedBy is the party that ended a dialog, from how the originating P-CSCF's dialog ended. A release by the IMS
// that reaches the dialog as a BYE or a final response from downstream is not seen here: the releasing node
// reports it with Released.
func EndedBy(cause proxy.EndCause, by proxy.Side) db.CallParty {
	switch cause {
	case proxy.EndBye, proxy.EndFailed:
		switch by {
		case proxy.Caller:
			return db.PartyCaller
		case proxy.Callee:
			return db.PartyCallee
		}
	}

	return db.PartyNetwork
}

// Duration is how long an answered call lasted, and false for a call that was not answered or has not ended.
func Duration(r db.CallRecord) (time.Duration, bool) {
	if r.Outcome != db.OutcomeAnswered || r.DeliveryEndAt.IsZero() {
		return 0, false
	}

	return r.DeliveryEndAt.Sub(r.DeliveryStartAt), true
}
