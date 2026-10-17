package ua

func init() {
	checkLoop = true
}

// Tables is the size of the tables of the agent, for tests to check that nothing leaks.
func Tables(a *Agent) (dialogs, invites, initials, outgoing int) {
	_ = a.loop.call(func() error {
		dialogs, invites, initials, outgoing = len(a.dialogs), len(a.invites), len(a.initials), len(a.outgoing)
		return nil
	})

	return dialogs, invites, initials, outgoing
}

// Sync waits for the jobs posted to the loop of the agent so far, and the handlers they made due.
func Sync(a *Agent) {
	_ = a.loop.call(func() error { return nil })
}

// Reinviting reports whether a re-INVITE of the agent is in progress on s: sent, and not yet failed or answered.
func Reinviting(s *Session) bool {
	return get(s.a.loop, func() bool { return s.invite != nil })
}

// Kept is the size of the tables of s kept for the retransmissions of responses to its INVITEs: the RSeq of their
// reliable 1xx, the ACKs of their 2xx.
func Kept(s *Session) (rseq, acks int) {
	_ = s.a.loop.call(func() error {
		rseq, acks = len(s.rseq), len(s.acks)
		return nil
	})

	return rseq, acks
}

// Subscribing is the number of initial SUBSCRIBEs of the agent whose NOTIFYs may still create subscriptions.
func Subscribing(a *Agent) int {
	return get(a.loop, func() int { return len(a.subscribing) })
}
