package ua

func init() {
	checkLoop = true
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
