package ua

import (
	"context"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// RFC 4028 section 4
const minSessionExpires = 90

const refreshRetry = 5 * time.Second

// RFC 4028
type sessionTimer struct {
	interval  uint32
	refresher bool

	minSE uint32

	expired bool

	refresh, abort *timer

	expiry context.Context
}

func seconds(d time.Duration) uint32 {
	return uint32(d / time.Second)
}

func (t *sessionTimer) noteMinSE(v uint32) {
	t.minSE = max(t.minSE, v)
}

// RFC 4028 section 9
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

func (t *sessionTimer) least() uint32 {
	return max(t.minSE, minSessionExpires)
}

// RFC 4028 section 7.4
func (t *sessionTimer) requestHeaders(a *Agent, req *sip.Request) {
	if t.minSE > 0 && !req.Header.Has("Min-SE") {
		req.Header.Set("Min-SE", strconv.FormatUint(uint64(t.minSE), 10))
	}

	interval := t.interval
	if interval == 0 {
		interval = seconds(a.cfg.SessionTimer.Interval)
	}

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

// RFC 4028 section 9
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

	mergeSupported(&rp.Header, []string{sip.OptionTagTimer})

	t.interval, t.refresher = se.Seconds, se.Refresher == sip.RefresherUAS
}

// RFC 4028 section 7.2
// RFC 4028 section 9
func (t *sessionTimer) fromResponse(req *sip.Request, res *sip.Response) {
	if se, ok, err := res.Header.SessionExpires(); ok && err == nil {
		least := minSessionExpires
		if v, ok, err := req.Header.MinSE(); ok && err == nil {
			least = max(least, int(v))
		}

		t.interval, t.refresher = max(se.Seconds, uint32(least)), se.Refresher != sip.RefresherUAS

		return
	}

	if se, ok, err := req.Header.SessionExpires(); ok && err == nil {
		t.interval, t.refresher = se.Seconds, true
		return
	}

	t.interval = 0
}

// RFC 4028 section 7.2
// RFC 4028 section 10
// RFC 4028 section 9
func (t *sessionTimer) start(ctx context.Context, s *Session) {
	t.stop()
	t.expired = false

	if t.interval == 0 || s.state != Confirmed {
		return
	}

	interval := time.Duration(t.interval) * time.Second

	if t.refresher {
		t.refresh = s.st.detached(ctx, interval/2, "session refresh", s.refreshSession)
		t.abort = s.st.detached(ctx, interval, "session expiry", s.expire)

		return
	}

	t.abort = s.st.detached(ctx, interval-min(32*time.Second, interval/3), "session expiry", s.expire)
}

// RFC 4028 section 10
func (t *sessionTimer) retry(ctx context.Context, s *Session) {
	if t.interval == 0 || !t.refresher {
		return
	}

	t.refresh.stop()
	t.refresh = s.st.detached(ctx, refreshRetry, "session refresh", s.refreshSession)
}

func (t *sessionTimer) stop() {
	t.refresh.stop()
	t.abort.stop()
	t.refresh, t.abort = nil, nil
}

// RFC 4028 section 10
func (s *Session) expire(ctx context.Context) {
	switch {
	case s.state == Terminated:
	case s.modifying():
		s.timer.expired = true
		s.timer.expiry = ctx
		s.timer.abort = s.st.later(ctx, s.policy.answerTimeout(), func(ctx context.Context) {
			if s.state != Terminated {
				s.byeEnd(ctx, End{Cause: EndExpired}, Change{}, nil)
			}
		})
	default:
		s.byeEnd(ctx, End{Cause: EndExpired}, Change{}, nil)
	}
}
