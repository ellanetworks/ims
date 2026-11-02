package ua

import (
	"context"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

type Modification struct {
	Method string

	Build func() (Change, error)
}

// RFC 3261 section 14.1
// RFC 6141 section 3.4
func (s *Session) Reinvite(ctx context.Context, c Change, done func(res *sip.Response, err error)) error {
	return s.modify("Reinvite", &modification{ctx: ctx, method: "INVITE", build: fixed(c), fixed: true, done: done})
}

// RFC 3311
func (s *Session) Update(ctx context.Context, c Change, done func(res *sip.Response, err error)) error {
	return s.modify("Update", &modification{ctx: ctx, method: "UPDATE", build: fixed(c), fixed: true, done: done})
}

// RFC 3261 section 14.1
// RFC 3264 section 4
// RFC 3311 section 5.1
// RFC 3311 section 5.2
// RFC 3312
func (s *Session) Modify(ctx context.Context, m Modification, done func(res *sip.Response, err error)) error {
	if m.Method != "INVITE" && m.Method != "UPDATE" || m.Build == nil {
		return &Error{Op: "Modify", Err: errorString("a Modification is an INVITE or an UPDATE, with Build")}
	}

	return s.modify("Modify", &modification{ctx: ctx, method: m.Method, build: m.Build, done: done})
}

type errorString string

func (e errorString) Error() string {
	return "sip/ua: " + string(e)
}

func fixed(c Change) func() (Change, error) {
	return func() (Change, error) { return c, nil }
}

type modification struct {
	ctx    context.Context
	method string
	build  func() (Change, error)
	done   func(*sip.Response, error)

	fixed     bool
	exchanges uint64

	refresh bool

	glared bool
	minSE  uint32
	wait   *timer
}

func (s *Session) modify(op string, m *modification) error {
	return s.st.do(func() error {
		if !s.usable() || m.method == "INVITE" && s.state == Early && !s.awaitingAck() {
			return s.errorf(op, ErrState)
		}

		m.exchanges = s.exchanges
		s.queue = append(s.queue, m)
		s.pump()

		return nil
	})
}

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

		_ = ci.send(m.ctx)
	}
}

func (s *Session) ready(m *modification) bool {
	switch {
	case m.wait != nil, s.awaitingAck(), len(s.answering) > 0, !s.usable():
		return false
	case m.method == "INVITE":
		return s.state == Confirmed && s.reinvite == nil && s.update == nil && s.in == nil
	}

	return s.update == nil
}

// RFC 3264 section 4
// RFC 3311 section 5.1
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

		if r := s.initial(); r != nil && r.unacked != nil && r.unacked.sdp {
			return false
		}
	}

	return true
}

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

	return ci, nil
}

func (s *Session) started(c *clientInvite) {
	if c.key.Method == "INVITE" {
		s.reinvite = c
	} else {
		s.update = c
	}
}

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

func (s *Session) requeue(m *modification) {
	s.queue = slices.Insert(s.queue, 0, m)
}

func (s *Session) modifying() bool {
	return s.reinvite != nil || s.update != nil || len(s.queue) > 0 || s.in != nil || s.inUpdate != nil
}

func (s *Session) modified() {
	if s.timer.expired && !s.modifying() && s.state != Terminated {
		s.byeEnd(s.timer.expiry, End{Cause: EndExpired}, Change{}, nil)
	}
}

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

// RFC 3261 section 9.1
func (s *Session) CancelReinvite(ctx context.Context, c Change) error {
	return s.st.do(func() error {
		found := false

		if ci := s.reinvite; ci != nil && !ci.m.refresh {
			found = true

			ci.cancel(ctx, c)
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

// RFC 3261 section 22.2
// RFC 4028 section 7.3
// RFC 3261 section 14.1
// RFC 5057
// RFC 4028 section 10
func (s *Session) modificationFailed(ctx context.Context, c *clientInvite, res *sip.Response) {
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

			_ = ci.send(ctx)

			return
		}
	case res.StatusCode == 491 && s.policy.RetryGlare && !m.glared:
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
		s.fail(ctx, c.key.Method, res, nil)
	case m.refresh:
		s.timer.retry(ctx, s)
	}

	s.finished(c)
	s.emitDone(m.done, res, nil)
}

func (s *Session) finishedQuietly(c *clientInvite) {
	switch c {
	case s.reinvite:
		s.reinvite = nil
	case s.update:
		s.update = nil
	}
}

// RFC 4028 section 7.4
func (s *Session) refreshSession(ctx context.Context) {
	if s.state != Confirmed {
		return
	}

	if s.policy.SkipRefreshes {
		s.timer.retry(ctx, s)
		return
	}

	if s.modifying() || s.awaitingAck() {
		s.timer.retry(ctx, s)
		return
	}

	method := "INVITE"
	if s.peerAllows("UPDATE") {
		method = "UPDATE"
	}

	m := &modification{ctx: ctx, method: method, refresh: true, exchanges: s.exchanges}
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

// RFC 3261 section 20.37
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
