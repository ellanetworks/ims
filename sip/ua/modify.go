package ua

import (
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

// Modification is a re-INVITE or an UPDATE of the application.
type Modification struct {
	// Method is INVITE or UPDATE.
	Method string

	// Build makes the change of the request, its session description included, each time the agent builds it: when
	// it is sent, after it waited for a transaction in progress or for the delay of a 491 (RFC 3261 §14.1), and a
	// change that cannot go yet, its offer conflicting with one pending, is built again once it can. An error ends the
	// modification, done receiving it. It runs with the strand held, as Handler.Decorate: it must not call the agent.
	Build func() (Change, error)
}

// Reinvite sends a re-INVITE with c: with an offer when c has a session description, or asking the peer for one
// (RFC 3261 §14.1). The re-INVITEs and UPDATEs of the agent and its session refreshes go in order, one re-INVITE and
// one UPDATE at a time, each once the session allows it (Modify); an offer of c that waited while another exchange
// completed goes stale, done receiving ErrStale. A 491 is retried when Policy.RetryGlare. The changes of an exchange
// within the re-INVITE that fails (a reliable 1xx and its PRACK, an UPDATE) stay: the application may send another
// offer to settle the session (RFC 6141 §3.4). CancelReinvite cancels it.
func (s *Session) Reinvite(c Change, done func(res *sip.Response, err error)) error {
	return s.modify("Reinvite", &modification{method: "INVITE", build: fixed(c), fixed: true, done: done})
}

// Update sends an UPDATE with c (RFC 3311): with an offer when c has a session description; without one, it refreshes
// the session timer and the target. It goes in order with the other modifications, as Reinvite.
func (s *Session) Update(c Change, done func(res *sip.Response, err error)) error {
	return s.modify("Update", &modification{method: "UPDATE", build: fixed(c), fixed: true, done: done})
}

// Modify sends a re-INVITE or an UPDATE whose change m.Build makes as it is sent: it never goes stale. It waits, in
// order with the other modifications of the agent, for what would make it conflict: the ACK of a 2xx of the agent, an
// INVITE transaction of the peer for a re-INVITE (RFC 3261 §14.1), an offer pending for one with an offer
// (RFC 3264 §4), the PRACK of the reliable 1xx answering an initial INVITE before a UPDATE of the callee with an offer
// (RFC 3311 §5.1), and the re-INVITE or the UPDATE of the agent in progress, of which there is one at a time
// (RFC 3311 §5.2); an UPDATE goes during a re-INVITE of the agent, for its preconditions (RFC 3312).
func (s *Session) Modify(m Modification, done func(res *sip.Response, err error)) error {
	if m.Method != "INVITE" && m.Method != "UPDATE" || m.Build == nil {
		return &Error{Op: "Modify", Err: errorString("a Modification is an INVITE or an UPDATE, with Build")}
	}

	return s.modify("Modify", &modification{method: m.Method, build: m.Build, done: done})
}

type errorString string

func (e errorString) Error() string {
	return "sip/ua: " + string(e)
}

func fixed(c Change) func() (Change, error) {
	return func() (Change, error) { return c, nil }
}

// modification is a re-INVITE or an UPDATE of the agent: of the application, or a session refresh.
type modification struct {
	method string
	build  func() (Change, error)
	done   func(*sip.Response, error)

	// fixed: the change was made once, at exchanges, and goes stale should another exchange complete before it goes.
	fixed     bool
	exchanges uint64

	refresh bool

	// glared: a 491 retried (RFC 3261 §14.1); minSE the Min-SE of the last 422 retried (RFC 4028 §7.3); wait the
	// delay of a 491 retry.
	glared bool
	minSE  uint32
	wait   *timer

	// cancel completes the CANCEL of a re-INVITE the application cancelled before it went.
	cancel *Change
}

func (s *Session) modify(op string, m *modification) error {
	return s.st.do(func() error {
		// A re-INVITE goes on a confirmed session, or waits for the ACK that confirms it.
		if !s.usable() || m.method == "INVITE" && s.state == Early && !s.awaitingAck() {
			return s.errorf(op, ErrState)
		}

		m.exchanges = s.exchanges
		s.queue = append(s.queue, m)
		s.pump()

		return nil
	})
}

// pump sends the modifications at the head of the queue that the session allows now. It runs whenever something
// they may wait for completes.
func (s *Session) pump() {
	if s.pumping {
		s.repump = true
		return
	}

	s.pumping = true

	for {
		s.repump = false
		s.pumpOnce()

		if !s.repump {
			break
		}
	}

	s.pumping = false
}

func (s *Session) pumpOnce() {
	for len(s.queue) > 0 && s.state != Terminated {
		m := s.queue[0]

		if !s.ready(m) {
			return
		}

		c, err := m.build()
		if err != nil {
			s.queue = s.queue[1:]
			s.emitDone(m.done, nil, err)

			continue
		}

		offer := sessionSDPOf(c.Bodies) != nil

		if m.fixed && offer && s.exchanges != m.exchanges {
			s.queue = s.queue[1:]
			s.emitDone(m.done, nil, s.errorf("modification", ErrStale))

			continue
		}

		if !s.readyOffer(m.method, offer) {
			return
		}

		s.queue = s.queue[1:]

		ci, err := s.newClientInvite(m, c)
		if err != nil {
			s.emitDone(m.done, nil, err)
			continue
		}

		_ = ci.send()
	}
}

// ready reports whether nothing in progress holds a modification back but its offer.
func (s *Session) ready(m *modification) bool {
	switch {
	case m.wait != nil, s.awaitingAck(), len(s.answering) > 0, !s.usable():
		// The ACK of a 2xx of either side goes first.
		return false
	case m.method == "INVITE":
		// RFC 3261 §14.1: one INVITE transaction at a time, in either direction; the UPDATE of the agent first.
		return s.state == Confirmed && s.reinvite == nil && s.update == nil && s.in == nil
	}

	return s.update == nil
}

// readyOffer reports whether the session allows the request now, given whether it carries an offer (RFC 3264 §4,
// RFC 3311 §5.1).
func (s *Session) readyOffer(method string, offer bool) bool {
	if !offer && method == "UPDATE" {
		return true
	}

	if _, _, pending := s.sdp.Pending(); pending {
		return false
	}

	if method == "UPDATE" {
		if _, _, done := s.sdp.Session(); !done {
			return false
		}

		// RFC 3311 §5.1: the callee offers in an UPDATE once the reliable 1xx with its answer is PRACKed.
		if r := s.initial(); r != nil && r.unacked != nil && r.unacked.sdp {
			return false
		}
	}

	return true
}

// newClientInvite builds a re-INVITE or an UPDATE of the session with c, records its offer, and notes it in
// progress.
func (s *Session) newClientInvite(m *modification, c Change) (*clientInvite, error) {
	req, err := s.d.NewRequest(m.method)
	if err != nil {
		return nil, err
	}

	c.apply(req)

	if !req.Header.Has("Contact") && s.contact != "" {
		req.Header.Add("Contact", s.contact)
	}

	mergeSupported(&req.Header, s.a.supported)

	if !req.Header.Has("Allow") {
		req.Header.Add("Allow", s.a.allowValue())
	}

	s.timer.requestHeaders(s.a, req)

	ci := &clientInvite{s: s, m: m, req: req, key: keyOf(local, req)}
	s.offerRequest(ci.key, sessionSDPOf(c.Bodies))
	s.started(ci)

	if m.cancel != nil {
		ci.cancel(*m.cancel)
	}

	return ci, nil
}

// started records a re-INVITE or an UPDATE of the agent as in progress, finished once it completes.
func (s *Session) started(c *clientInvite) {
	if c.key.Method == "INVITE" {
		s.reinvite = c
	} else {
		s.update = c
	}
}

// finished drops a re-INVITE or an UPDATE of the agent from those in progress, and sends what waited for it.
func (s *Session) finished(c *clientInvite) {
	switch c {
	case s.reinvite:
		s.reinvite = nil
	case s.update:
		s.update = nil
	default:
		return
	}

	s.modified()
	s.pump()
}

// requeue puts a modification back at the head of the queue, for a retry.
func (s *Session) requeue(m *modification) {
	s.queue = slices.Insert(s.queue, 0, m)
}

// modifying reports an INVITE or an UPDATE in progress or waiting on the session, of either side: it may refresh the
// session.
func (s *Session) modifying() bool {
	return s.reinvite != nil || s.update != nil || len(s.queue) > 0 || s.in != nil || s.inUpdate != nil
}

// modified ends the session whose timer expired while it was being modified, once no modification refreshed it.
func (s *Session) modified() {
	if s.timer.expired && !s.modifying() && s.state != Terminated {
		s.byeEnd(End{Cause: EndExpired}, Change{}, nil)
	}
}

// dropModifications completes the modifications of the agent in progress or waiting with err, as the session ends.
func (s *Session) dropModifications(err error) {
	for _, c := range []*clientInvite{s.reinvite, s.update} {
		if c != nil && !c.final {
			c.final = true
			s.emitDone(c.m.done, nil, err)
		}
	}

	for _, m := range s.queue {
		m.wait.stop()
		s.emitDone(m.done, nil, err)
	}

	s.queue = nil
}

// CancelReinvite cancels the re-INVITE of Reinvite or Modify, the CANCEL carrying c, its Reason header fields
// (Reasons) (RFC 3261 §9.1): one in progress, done receiving the 487, or the 2xx that came first; one waiting to be
// sent, done receiving ErrCancelled. The session refreshes of the agent are not cancelled.
func (s *Session) CancelReinvite(c Change) error {
	return s.st.do(func() error {
		found := false

		if ci := s.reinvite; ci != nil && !ci.m.refresh {
			found = true

			ci.cancel(c)
		}

		kept := s.queue[:0]

		for _, m := range s.queue {
			if m.method != "INVITE" || m.refresh {
				kept = append(kept, m)
				continue
			}

			found = true

			m.wait.stop()
			s.emitDone(m.done, nil, s.errorf("CancelReinvite", ErrCancelled))
		}

		s.queue = kept

		if !found {
			return s.errorf("CancelReinvite", ErrState)
		}

		s.pump()

		return nil
	})
}

// modificationFailed handles a final response other than 2xx to a re-INVITE or an UPDATE of the agent: a challenge
// is answered (RFC 3261 §22.2), a 422 retried with its Min-SE while it rises (RFC 4028 §7.3), a 491 retried after
// the delay of RFC 3261 §14.1 when Policy.RetryGlare; a failure that destroys the usage or the dialog ends them
// (RFC 5057); a failed session refresh is tried again before the session expires (RFC 4028 §10).
func (s *Session) modificationFailed(c *clientInvite, res *sip.Response) {
	m := c.m
	minSE, hasMinSE, _ := res.Header.MinSE()

	switch {
	case s.state == Terminated:
	case c.cancelled != nil:
	case res.StatusCode == 401 || res.StatusCode == 407:
		if next, ok := s.challenged(c.req, res, c.tries, false); ok {
			ci := &clientInvite{s: s, m: m, req: next, key: keyOf(local, next), tries: c.tries + 1}
			s.offerRequest(ci.key, sessionSDP(next.Envelope))
			s.finishedQuietly(c)
			s.started(ci)

			_ = ci.send()

			return
		}
	case res.StatusCode == 491 && s.policy.RetryGlare && !m.glared:
		// RFC 3261 §14.1: once, not to go on forever against a peer whose offer stays pending.
		m.glared = true

		m.wait = s.st.after(glareDelay(s.uac), func() {
			m.wait = nil

			s.pump()
		})
		s.requeue(m)
		s.finished(c)

		return
	case res.StatusCode == 422 && hasMinSE && minSE > m.minSE:
		s.timer.noteMinSE(minSE)
		m.minSE = minSE

		s.requeue(m)
		s.finished(c)

		return
	case failureOf(c.key.Method, res, nil) != affectsTransaction:
		s.fail(c.key.Method, res, nil)
	case m.refresh:
		s.timer.retry(s)
	}

	s.finished(c)
	s.emitDone(m.done, res, nil)
}

// finishedQuietly drops a request in progress that another replaces at once, as after a challenge.
func (s *Session) finishedQuietly(c *clientInvite) {
	switch c {
	case s.reinvite:
		s.reinvite = nil
	case s.update:
		s.update = nil
	}
}

// refreshSession refreshes the session (RFC 4028 §7.4): with an UPDATE without an offer when the peer allows UPDATE,
// with a re-INVITE offering the session unchanged otherwise. A modification in progress or waiting refreshes the
// session too: the refresh is tried again a little later.
func (s *Session) refreshSession() {
	if s.state != Confirmed {
		return
	}

	// A refresh skipped is tried again later: turning SkipRefreshes off resumes them.
	if s.policy.SkipRefreshes {
		s.timer.retry(s)
		return
	}

	if s.modifying() || s.awaitingAck() {
		s.timer.retry(s)
		return
	}

	method := "INVITE"
	if s.peerAllows("UPDATE") {
		method = "UPDATE"
	}

	m := &modification{method: method, refresh: true, exchanges: s.exchanges}
	m.build = func() (Change, error) {
		var c Change

		if mine, ok := s.sdp.Described(local); ok && m.method == "INVITE" {
			c.Bodies = []sip.BodyPart{SDP(mine)}
		}

		return c, nil
	}

	s.queue = append(s.queue, m)
	s.pump()
}

// mergeSupported adds to Supported the option tags it lacks, in a field of their own (RFC 3261 §20.37).
func mergeSupported(h *sip.Header, tags []string) {
	var missing []string

	for _, tag := range tags {
		if !h.HasOptionTag("Supported", tag) {
			missing = append(missing, tag)
		}
	}

	if len(missing) > 0 {
		h.Add("Supported", strings.Join(missing, ", "))
	}
}
