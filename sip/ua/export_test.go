package ua

func init() {
	checkLoop = true
}

func Reinviting(s *Session) bool {
	return get(s.st, func() bool { return s.reinvite != nil })
}

func Kept(s *Session) (rseq, acks int) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()

	return len(s.rseq), len(s.acks)
}
