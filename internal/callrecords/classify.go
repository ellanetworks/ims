package callrecords

import (
	"slices"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
)

// RFC 3261 section 21
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
		return db.OutcomeUnavailable
	case status == 408 && alerted:
		return db.OutcomeNoAnswer
	}

	return db.OutcomeFailed
}

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

func Duration(r db.CallRecord) (time.Duration, bool) {
	if r.Outcome != db.OutcomeAnswered || r.DeliveryEndAt.IsZero() {
		return 0, false
	}

	return r.DeliveryEndAt.Sub(r.DeliveryStartAt), true
}

// RFC 3264 section 6
func AcceptedMedia(answer []byte) []string {
	s, err := sdp.Parse(answer)
	if err != nil {
		return nil
	}

	var types []string

	for _, m := range s.Media {
		if m.Port() != 0 && !slices.Contains(types, m.Type()) {
			types = append(types, m.Type())
		}
	}

	return types
}
