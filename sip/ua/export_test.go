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
