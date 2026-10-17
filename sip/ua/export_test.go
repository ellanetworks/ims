package ua

import "context"

func init() {
	checkLoop = true
}

// Tables is the size of the tables of the agent, for tests to check that nothing leaks: the dialogs with usages, the
// INVITEs of peers awaiting their final response, the initial requests in progress, the outgoing INVITEs.
func Tables(a *Agent) (dialogs, invites, initials, outgoing int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, e := range a.dialogs {
		if !e.ended {
			dialogs++
		}
	}

	return dialogs, len(a.invites), len(a.initials), len(a.outgoing)
}

// Lingering is the number of dialogs kept without usages, for 64*T1 after their last one ended.
func Lingering(a *Agent) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	n := 0

	for _, e := range a.dialogs {
		if e.ended {
			n++
		}
	}

	return n
}

// Sync waits for the strands of the agent to run the jobs posted to them so far, and the handlers they made due.
func Sync(a *Agent) {
	a.waitUntil(context.Background(), func() bool { return a.workers == 0 })
}

// Reinviting reports whether a re-INVITE of the agent is in progress on s: sent, and not yet failed or answered.
func Reinviting(s *Session) bool {
	return get(s.st, func() bool { return s.reinvite != nil })
}

// Kept is the size of the tables of s kept for the retransmissions of responses to its INVITEs: the RSeq of their
// reliable 1xx, the ACKs of their 2xx.
func Kept(s *Session) (rseq, acks int) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()

	return len(s.rseq), len(s.acks)
}

// Subscribing is the number of initial SUBSCRIBEs of the agent whose NOTIFYs may still create subscriptions.
func Subscribing(a *Agent) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.subscribing)
}
