package ua

import (
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// RFC 4028 §4: the smallest session interval.
const minSessionExpires = 90

// refreshRetry is the delay before trying again a session refresh that could not be sent, or failed.
const refreshRetry = 5 * time.Second

// sessionTimer is the session timer of a dialog (RFC 4028): its session interval, and whether the agent refreshes.
type sessionTimer struct {
	interval  uint32
	refresher bool

	// minSE is the largest Min-SE of a 422 or of a session refresh request on the dialog (RFC 4028 §7.4).
	minSE uint32

	// expired marks a session whose timer expired while a re-INVITE or an UPDATE was in progress: the session ends
	// once it completes, unless it refreshed the session.
	expired bool

	refresh, abort *timer
}

func seconds(d time.Duration) uint32 {
	return uint32(d / time.Second)
}

func (t *sessionTimer) noteMinSE(v uint32) {
	t.minSE = max(t.minSE, v)
}

// check refuses a session refresh request of a UAC supporting session timers that asks for less than the minimum of
// the agent, with a 422 carrying it (RFC 4028 §9), and notes its Min-SE.
func (t *sessionTimer) check(a *Agent, req *sip.Request) *sip.Response {
	if v, ok, err := req.Header.MinSE(); ok && err == nil {
		t.noteMinSE(v)
	}

	se, ok, err := req.Header.SessionExpires()
	if !ok || err != nil || !supportsTimer(req.Header) {
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

func supportsTimer(h sip.Header) bool {
	return h.HasOptionTag("Supported", sip.OptionTagTimer) || h.HasOptionTag("Require", sip.OptionTagTimer)
}

// least is the smallest session interval allowed on the dialog.
func (t *sessionTimer) least() uint32 {
	return max(t.minSE, minSessionExpires)
}

// requestHeaders adds to a re-INVITE or an UPDATE of the agent the session timer of the dialog, keeping the
// refresher (RFC 4028 §7.4).
func (t *sessionTimer) requestHeaders(a *Agent, req *sip.Request) {
	if t.minSE > 0 && !req.Header.Has("Min-SE") {
		req.Header.Set("Min-SE", strconv.FormatUint(uint64(t.minSE), 10))
	}

	interval := t.interval
	if interval == 0 {
		interval = seconds(a.cfg.SessionTimer.Interval)
	}

	// RFC 4028 §7.3: a Session-Expires of the application below the Min-SE of the dialog is raised to it.
	if se, ok, err := req.Header.SessionExpires(); ok && err == nil {
		if se.Seconds < t.least() {
			se.Seconds = t.least()
			req.Header.Set("Session-Expires", se.String())
		}

		return
	}

	if interval == 0 {
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

// responseHeaders sets the session timer of the 2xx of the agent to a session refresh request (RFC 4028 §9): the
// Session-Expires of the reply, or else the one of the request, or else the interval of the agent; and the session
// timer of the dialog.
func (t *sessionTimer) responseHeaders(a *Agent, req *sip.Request, rp *Reply) {
	supported := supportsTimer(req.Header)
	least := max(minSessionExpires, t.minSE)

	var se sip.SessionExpires

	switch {
	case rp.Header.Has("Session-Expires"):
		mine, err := sip.ParseSessionExpires(rp.Header.Get("Session-Expires"))
		if err != nil {
			t.interval = 0
			return
		}

		se = mine
	default:
		asked, ok, err := req.Header.SessionExpires()

		switch {
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
	}

	// RFC 4028 §9 Table 2: a UAC without session timers cannot refresh; otherwise the refresher of the request.
	asked, _, _ := req.Header.SessionExpires()

	switch {
	case !supported:
		se.Refresher = sip.RefresherUAS
	case asked.Refresher != "":
		se.Refresher = asked.Refresher
	case se.Refresher == "":
		se.Refresher = a.cfg.SessionTimer.Refresher
		if se.Refresher == "" {
			se.Refresher = sip.RefresherUAS
		}
	}

	rp.Header = rp.Header.Clone()
	rp.Header.Set("Session-Expires", se.String())

	if supported && !rp.Header.HasOptionTag("Require", sip.OptionTagTimer) {
		addOptionTag(&rp.Header, "Require", sip.OptionTagTimer)
	}

	if !rp.Header.HasOptionTag("Supported", sip.OptionTagTimer) && rp.Header.Has("Supported") {
		rp.Header.Add("Supported", sip.OptionTagTimer)
	}

	t.interval, t.refresher = se.Seconds, se.Refresher == sip.RefresherUAS
}

// fromResponse sets the session timer of the dialog from the 2xx to a session refresh request of the agent
// (RFC 4028 §7.2).
func (t *sessionTimer) fromResponse(req *sip.Request, res *sip.Response) {
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

// start arms the timers of the session after a 2xx to a session refresh request: the refresh at half the interval
// when the agent refreshes, and the BYE before the end of the interval otherwise, or when refreshes fail
// (RFC 4028 §7.2, §10).
func (t *sessionTimer) start(s *Session) {
	t.stop()
	t.expired = false

	if t.interval == 0 {
		return
	}

	interval := time.Duration(t.interval) * time.Second

	if t.refresher {
		t.refresh = s.st.after(interval/2, s.refreshSession)
		t.abort = s.st.after(interval, s.expire)

		return
	}

	t.abort = s.st.after(interval-min(32*time.Second, interval/3), s.expire)
}

// retry tries again, a little later, a session refresh that could not be sent or failed; the abort timer still ends
// the session at its expiry (RFC 4028 §10).
func (t *sessionTimer) retry(s *Session) {
	if t.interval == 0 || !t.refresher {
		return
	}

	t.refresh.stop()
	t.refresh = s.st.after(refreshRetry, s.refreshSession)
}

func (t *sessionTimer) stop() {
	t.refresh.stop()
	t.abort.stop()
	t.refresh, t.abort = nil, nil
}

// refreshSession refreshes the session: with an UPDATE without an offer when the peer allows UPDATE, with a re-INVITE
// offering the session unchanged otherwise (RFC 4028 §7.4). A transaction in progress delays it.
func (s *Session) refreshSession() {
	if s.state != Confirmed {
		return
	}

	method := "INVITE"

	var c Change

	if s.peerAllows("UPDATE") {
		method = "UPDATE"
	} else if mine, ok := s.sdp.Described(local); ok {
		c.Bodies = []sip.BodyPart{SDP(mine)}
	}

	if code, _ := s.conflict(method, len(c.Bodies) > 0, true); code != 0 {
		s.timer.retry(s)
		return
	}

	ci, err := s.newClientInvite(method, c, nil)
	if err != nil {
		s.timer.retry(s)
		return
	}

	ci.refresh = true

	if err := ci.send(); err != nil {
		s.timer.retry(s)
	}
}

// expire ends a session whose timer expired without a refresh (RFC 4028 §10). A re-INVITE or an UPDATE in progress
// defers it: its 2xx refreshes the session.
func (s *Session) expire() {
	switch {
	case s.state == Terminated:
	case s.modifying():
		s.timer.expired = true
	default:
		s.byeEnd(End{Cause: EndExpired})
	}
}
