package ua

import (
	"context"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"go.opentelemetry.io/otel/trace"
)

type forkSet struct {
	members []*Request
	chosen  *Request
}

// RFC 3261 section 12.1.1
// RFC 6228
func (r *Request) Fork(h Handler) (*Request, error) {
	var f *Request

	err := r.st.do(func() error {
		s := r.s

		if s == nil || r.req.Method != "INVITE" || r != s.initial() || r.final || r.forks == nil {
			return &Error{Op: "Fork", Err: ErrState}
		}

		a := r.a
		id := dialog.ID{CallID: s.id.CallID, LocalTag: sip.NewTag(), RemoteTag: s.id.RemoteTag}

		fs := newSession(a, r.st, h, false, id, s.policy)
		if res := fs.timer.check(a, r.req); res != nil {
			return &Error{Op: "Fork", Err: ErrState}
		}

		f = newRequest(r.ctx, fs, r.tx, r.req)
		f.tag = id.LocalTag
		f.role = fs.offerRequest(f.key, sessionSDP(r.req.Envelope))
		fs.in = f
		fs.notePeer(r.req.Header)

		f.forks = r.forks
		r.forks.members = append(r.forks.members, f)

		if !a.addFork(fs) {
			return ErrClosed
		}

		return nil
	})

	return f, err
}

func (r *Request) toTag() string {
	if r.tag != "" {
		return r.tag
	}

	return r.tx.ToTag()
}

func (r *Request) forked() bool {
	return r.forks != nil && len(r.forks.members) > 1
}

func (r *Request) Chosen() *Session {
	return get(r.st, func() *Session {
		if r.forks == nil || r.forks.chosen == nil {
			return nil
		}

		return r.forks.chosen.s
	})
}

func (r *Request) live() bool {
	return (!r.final || r.held != nil) && r.s.state == Early
}

func (r *Request) others() []*Request {
	if !r.forked() {
		return nil
	}

	var out []*Request

	for _, m := range r.forks.members {
		if m != r && m.live() {
			out = append(out, m)
		}
	}

	return out
}

func (r *Request) leave() {
	r.final = true
	r.held = nil
	r.queued = nil

	if r.unacked != nil {
		r.unacked.timer.stop()
	}

	if r.forks != nil && r.forks.chosen == r {
		r.forks.chosen = nil
	}
}

// RFC 6228 section 6
func (r *Request) settle(res *sip.Response) {
	if r.forks == nil {
		return
	}

	for _, m := range r.forks.members {
		m.answerBy.stop()
	}

	r.forks.chosen = nil
	if res.IsSuccess() {
		r.forks.chosen = r
	}

	for _, m := range r.others() {
		m.leave()

		if !res.IsSuccess() {
			m.s.end(r.ctx, End{Cause: EndRejected, Response: res})
			continue
		}

		if m.s.d != nil && supports199(r.req) {
			if early, err := m.build(Reply{Code: 199}); err == nil {
				m.complete(early)
				_ = m.tx.Respond(early)
			}
		}

		m.s.end(r.ctx, End{Cause: EndNotChosen})
	}
}

// RFC 6228 section 6
func (r *Request) drop(ctx context.Context, c Change) {
	r.leave()

	if r.s.d != nil && supports199(r.req) {
		if early, err := r.build(Reply{Code: 199, Header: c.Header}); err == nil {
			r.complete(early)
			_ = r.tx.Respond(early)
		}
	}

	r.s.end(ctx, End{Cause: EndBye})
}

func supports199(req *sip.Request) bool {
	return req.Header.HasOptionTag("Supported", sip.OptionTag199) || req.Header.HasOptionTag("Require", sip.OptionTag199)
}

func (r *Request) group() []*Request {
	if !r.forked() {
		return []*Request{r}
	}

	var out []*Request

	for _, m := range r.forks.members {
		if m.live() {
			out = append(out, m)
		}
	}

	return out
}

func (a *Agent) addFork(s *Session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return false
	}

	var origin trace.SpanContext
	if s.out != nil {
		origin = s.out.origin
	}

	e := a.entry(s.id, s.st, origin)
	e.invite, e.invited = s, true

	return true
}
