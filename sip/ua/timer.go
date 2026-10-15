package ua

import (
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// RFC 4028 §4: the smallest session interval.
const minSessionExpires = 90

// sessionTimer is the session timer of a dialog (RFC 4028): its session interval, and whether the agent refreshes.
// It is guarded by the lock of its session.
type sessionTimer struct {
	interval  uint32
	refresher bool

	// minSE is the largest Min-SE of a 422 or of a session refresh request on the dialog (RFC 4028 §7.4).
	minSE uint32

	gen            int
	refresh, abort transaction.Timer
}

func seconds(d time.Duration) uint32 {
	return uint32(d / time.Second)
}

func (t *sessionTimer) noteMinSE(v uint32) {
	t.minSE = max(t.minSE, v)
}

// check refuses a session refresh request asking for less than the minimum of the agent with a 422 carrying it
// (RFC 4028 §9), and notes its Min-SE.
func (t *sessionTimer) check(a *Agent, req *sip.Request) *sip.Response {
	if v, ok, err := req.Header.MinSE(); ok && err == nil {
		t.noteMinSE(v)
	}

	se, ok, err := req.Header.SessionExpires()
	if !ok || err != nil {
		return nil
	}

	least := seconds(a.cfg.SessionTimer.MinSE)
	if se.Seconds >= least {
		return nil
	}

	res := sip.NewResponse(req, 422, "")
	res.Header.Add("Min-SE", strconv.FormatUint(uint64(least), 10))

	if a.cfg.Respond != nil {
		a.cfg.Respond(req, res)
	}

	return res
}

// least is the smallest session interval allowed on the dialog.
func (t *sessionTimer) least() uint32 {
	return max(t.minSE, minSessionExpires)
}

// requestHeaders adds to a re-INVITE or an UPDATE of the agent the session timer of the dialog, keeping the
// refresher (RFC 4028 §7.4).
func (t *sessionTimer) requestHeaders(a *Agent, req *sip.Request) {
	if t.minSE > 0 {
		req.Header.Set("Min-SE", strconv.FormatUint(uint64(t.minSE), 10))
	}

	interval := t.interval
	if interval == 0 {
		interval = seconds(a.cfg.SessionTimer.Interval)
	}

	if interval == 0 || req.Header.Has("Session-Expires") {
		return
	}

	se := sip.SessionExpires{Seconds: max(interval, t.least())}

	if t.interval > 0 {
		se.Refresher = sip.RefresherUAS
		if t.refresher {
			se.Refresher = sip.RefresherUAC
		}
	}

	req.Header.Set("Session-Expires", se.String())
}

// responseHeaders sets the session timer of the 2xx of the agent to a session refresh request, and of the dialog
// (RFC 4028 §9).
func (t *sessionTimer) responseHeaders(a *Agent, req *sip.Request, res *sip.Response) {
	supported := req.Header.HasOptionTag("Supported", sip.OptionTagTimer) || req.Header.HasOptionTag("Require", sip.OptionTagTimer)
	least := max(minSessionExpires, t.minSE)

	var se sip.SessionExpires

	switch asked, ok, err := req.Header.SessionExpires(); {
	case ok && err == nil:
		se = asked

		// The UAS may reduce the interval, not below the Min-SE.
		if mine := seconds(a.cfg.SessionTimer.Interval); mine > 0 && mine < se.Seconds {
			se.Seconds = max(mine, least)
		}
	case supported && a.cfg.SessionTimer.Interval > 0:
		se.Seconds = max(seconds(a.cfg.SessionTimer.Interval), least)
	default:
		t.interval = 0
		return
	}

	switch {
	case !supported:
		se.Refresher = sip.RefresherUAS
	case se.Refresher == "":
		se.Refresher = a.cfg.SessionTimer.Refresher
		if se.Refresher == "" {
			se.Refresher = sip.RefresherUAS
		}
	}

	res.Header.Set("Session-Expires", se.String())

	if supported && !res.Header.HasOptionTag("Require", sip.OptionTagTimer) {
		res.Header.Add("Require", sip.OptionTagTimer)
	}

	if !res.Header.HasOptionTag("Supported", sip.OptionTagTimer) {
		res.Header.Add("Supported", sip.OptionTagTimer)
	}

	t.interval, t.refresher = se.Seconds, se.Refresher == sip.RefresherUAS
}

// fromResponse sets the session timer of the dialog from the 2xx to a session refresh request of the agent
// (RFC 4028 §7.2).
func (t *sessionTimer) fromResponse(a *Agent, req *sip.Request, res *sip.Response) {
	if se, ok, err := res.Header.SessionExpires(); ok && err == nil {
		t.interval, t.refresher = se.Seconds, se.Refresher != sip.RefresherUAS
		return
	}

	// The UAS does not support session timers: the UAC that asked for one refreshes.
	if se, ok, err := req.Header.SessionExpires(); ok && err == nil {
		t.interval, t.refresher = se.Seconds, true
		return
	}

	t.interval = 0
}

// startLocked arms the timers of the session after a 2xx to a session refresh request: the refresh at half the
// interval when the agent refreshes, and the BYE before the end of the interval otherwise, or when refreshes fail
// (RFC 4028 §7.2, §10).
func (t *sessionTimer) startLocked(s *Session) {
	t.stop()

	if t.interval == 0 {
		return
	}

	t.gen++
	gen := t.gen
	interval := time.Duration(t.interval) * time.Second

	if t.refresher {
		t.refresh = s.a.clock.AfterFunc(interval/2, func() { s.refreshSession(gen) })
		t.abort = s.a.clock.AfterFunc(interval, func() { s.expire(gen) })

		return
	}

	t.abort = s.a.clock.AfterFunc(interval-min(32*time.Second, interval/3), func() { s.expire(gen) })
}

func (t *sessionTimer) stop() {
	for _, tm := range []transaction.Timer{t.refresh, t.abort} {
		if tm != nil {
			tm.Stop()
		}
	}

	t.refresh, t.abort = nil, nil
}

// refreshSession refreshes the session: with an UPDATE without an offer when the peer allows UPDATE, with a re-INVITE
// offering the session unchanged otherwise (RFC 4028 §7.4).
func (s *Session) refreshSession(gen int) {
	s.mu.Lock()

	if s.timer.gen != gen || s.state != Confirmed {
		s.unlock()
		return
	}

	method := "INVITE"
	var c Change

	if s.peerAllowsLocked("UPDATE") {
		method = "UPDATE"
	} else if sdp, ok := s.sdp.Described(local); ok {
		c.Bodies = []sip.BodyPart{SDP(sdp)}
	}

	if code, _ := s.conflictLocked(method, len(c.Bodies) > 0); code != 0 || method == "INVITE" && s.in != nil {
		// A transaction in progress refreshes the session when it succeeds.
		s.unlock()
		return
	}

	ci, err := s.newClientInviteLocked(method, c, nil)
	s.unlock()

	if err == nil {
		_ = ci.send()
	}
}

// expire ends a session whose timer expired without a refresh (RFC 4028 §10).
func (s *Session) expire(gen int) {
	s.mu.Lock()

	if s.timer.gen == gen {
		s.byeLocked(End{Cause: EndExpired})
	}

	s.unlock()
}
