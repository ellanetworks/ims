package mobile

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/sip/ua"
)

// event is an event of the session, in JSON to Platform.Event (the package doc lists them).
type event struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	Call    string `json:"call,omitempty"`

	Registered bool   `json:"registered,omitempty"`
	IMPU       string `json:"impu,omitempty"`
	Reason     string `json:"reason,omitempty"`
	RetryAfter int64  `json:"retryAfter,omitempty"`

	Kind   string `json:"kind,omitempty"`
	Group  string `json:"group,omitempty"`
	Caller string `json:"caller,omitempty"`
	Manual bool   `json:"manual,omitempty"`
	Cause  string `json:"cause,omitempty"`

	Notice string `json:"notice,omitempty"`
	Party  string `json:"party,omitempty"`
	// Duration is the seconds a granted talker may transmit (TS 24.380 §8.2.3.3).
	Duration *int `json:"duration,omitempty"`

	On   *bool `json:"on,omitempty"`
	Mode *int  `json:"mode,omitempty"`

	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// events is the queue of the session's events: one goroutine gives them to the platform, in order, so that neither
// the protocol's goroutines wait for the app (ua strands must not block) nor the app sees them concurrently.
type events struct {
	p Platform

	mu     sync.Mutex
	queue  []event
	wake   chan struct{}
	closed bool
	done   chan struct{}
}

func newEvents(p Platform) *events {
	e := &events{p: p, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go e.run()

	return e
}

func (e *events) emit(ev event) {
	ev.Version = schemaVersion

	e.mu.Lock()

	if e.closed {
		e.mu.Unlock()
		return
	}

	e.queue = append(e.queue, ev)
	e.mu.Unlock()

	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *events) run() {
	defer close(e.done)

	for {
		e.mu.Lock()
		queue, closed := e.queue, e.closed
		e.queue = nil
		e.mu.Unlock()

		for _, ev := range queue {
			if b, err := json.Marshal(ev); err == nil {
				e.p.Event(string(b))
			}
		}

		if closed {
			return
		}

		if len(queue) == 0 {
			<-e.wake
		}
	}
}

// close delivers the events queued, then stops; it does not wait for them, so that the app may call it from Event.
func (e *events) close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()

	select {
	case e.wake <- struct{}{}:
	default:
	}
}

var notices = map[floor.NoticeKind]string{
	floor.NoticeGranted: "granted", floor.NoticeDenied: "denied", floor.NoticeTaken: "taken", floor.NoticeIdle: "idle",
	floor.NoticeRevoked: "revoked", floor.NoticeRequestTimeout: "timeout", floor.NoticeMediaEnded: "media-ended",
}

// cause is how a call ended, for the app's DisconnectCause: "local" (the user left), "remote" (the server ended it),
// "rejected" (its INVITE refused, with its status), "missed" (an invitation cancelled before it was answered),
// "error" (a timeout, a dialog lost or expired).
func cause(e ua.End, incoming bool) string {
	switch e.Cause {
	case ua.EndBye:
		if e.Remote {
			return "remote"
		}

		return "local"
	case ua.EndRejected:
		return "rejected"
	case ua.EndCancelled:
		if incoming && e.Remote {
			return "missed"
		}

		return "local"
	}

	return "error"
}

// clientEvent turns an event of the client into the session's, the calls' state kept for State.
func (s *Session) clientEvent(e client.Event) {
	if e.Call == nil {
		return
	}

	c := s.callOf(e.Call)
	out := event{Call: c.id}

	switch e.Kind {
	case client.EventIncoming:
		out.Type, out.Kind = "call", "incoming"
		out.Group, out.Caller, out.Manual = e.Call.Group().String(), e.Call.Caller().String(), e.Call.Manual()

		s.update(c, func(st *callState) { st.State, st.Group, st.Caller = "incoming", out.Group, out.Caller })
	case client.EventEstablished:
		out.Type, out.Kind = "call", "established"

		s.update(c, func(st *callState) { st.State = "established" })
	case client.EventFailed:
		if s.retry(c, e.Response) {
			return
		}

		out.Type, out.Kind, out.Cause = "call", "ended", "rejected"

		if e.Response != nil {
			out.Status = e.Response.StatusCode
		} else {
			out.Cause = "error"
		}

		if e.Err != nil {
			out.Error = e.Err.Error()
		}

		defer s.forget(e.Call)
	case client.EventEnded:
		out.Type, out.Kind, out.Cause = "call", "ended", cause(e.End, e.Call.Incoming())

		if e.End.Response != nil {
			out.Status = e.End.Response.StatusCode
		}

		defer s.forget(e.Call)
	case client.EventFloor:
		out.Type, out.Notice, out.Party = "floor", notices[e.Floor.Kind], e.Floor.Party
		if d := int(e.Floor.Duration / time.Second); d > 0 {
			out.Duration = &d
		}

		s.update(c, func(st *callState) {
			st.Floor = out.Notice

			switch e.Floor.Kind {
			case floor.NoticeTaken:
				st.Talker = e.Floor.Party
			case floor.NoticeGranted:
				st.Talker = s.set.MCPTTID
			case floor.NoticeIdle, floor.NoticeRevoked:
				st.Talker = ""
			}
		})
	case client.EventTransmit:
		on, mode := e.Transmitting, e.Mode
		out.Type, out.On = "transmit", &on

		if on {
			out.Mode = &mode
		}

		s.update(c, func(st *callState) { st.Transmitting = on })
	case client.EventModeRequest:
		mode := e.Mode
		out.Type, out.Mode = "mode", &mode
	case client.EventHeard:
		out.Type, out.Party = "heard", e.Party
	default:
		return
	}

	s.events.emit(out)
}
