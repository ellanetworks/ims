package ua

import (
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
)

// forkSet are the requests of one initial INVITE of the peer, answered in one early dialog or several: the first,
// and the forks of Request.Fork, each the initial INVITE of a session of its own. One final response answers the
// INVITE, for them all; chosen is the member whose 2xx answered it.
type forkSet struct {
	members []*Request
	chosen  *Request
}

// Fork answers the initial INVITE r in another early dialog: a session of its own, with handler h, on the strand
// of r, whose To tag differs (RFC 3261 §12.1.1), as a B2BUA mirroring the early dialogs of its other leg does. The
// request it returns sends that dialog's provisional responses and its final response; that response answers the
// INVITE for all its dialogs: a 2xx confirms its own and ends the others, each with a 199 when the INVITE supports
// it (RFC 6228), and another one ends them all. A CANCEL ends them all; a BYE in one of them ends that one, the
// INVITE answered with a 487 once none is left.
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

		f = newRequest(fs, r.tx, r.req)
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

// toTag is the To tag of the responses to the request: that of its dialog.
func (r *Request) toTag() string {
	if r.tag != "" {
		return r.tag
	}

	return r.tx.ToTag()
}

// forked reports whether the initial INVITE of r has several members.
func (r *Request) forked() bool {
	return r.forks != nil && len(r.forks.members) > 1
}

// Chosen is the session in which a 2xx answered the initial INVITE r, held for a PRACK or sent: of r or of one of its
// forks; nil before.
func (r *Request) Chosen() *Session {
	return get(r.st, func() *Session {
		if r.forks == nil || r.forks.chosen == nil {
			return nil
		}

		return r.forks.chosen.s
	})
}

// live reports whether the request is a member of its INVITE whose early dialog goes on: no final response sent,
// or a 2xx held for a PRACK.
func (r *Request) live() bool {
	return (!r.final || r.held != nil) && r.s.state == Early
}

// others are the members of the INVITE of r, r aside, still live.
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

// leave takes a member out of its INVITE, which its other members answer: its dialog ended alone.
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

// settle ends the other members of the INVITE of r as r sends res, its final response: before a 2xx, each with a 199
// when the INVITE supports it (RFC 6228 §6); with a failure, all with it.
func (r *Request) settle(res *sip.Response) {
	if r.forks == nil {
		return
	}

	// The deadline of the INVITE, armed on its first member, ends with its answer.
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
			m.s.end(End{Cause: EndRejected, Response: res})
			continue
		}

		if m.s.d != nil && supports199(r.req) {
			if early, err := m.build(Reply{Code: 199}); err == nil {
				m.complete(early)
				_ = m.tx.Respond(early)
			}
		}

		m.s.end(End{Cause: EndNotChosen})
	}
}

// drop ends the early dialog of r alone, its other members left to answer the INVITE: with a 199 carrying c when the
// INVITE supports it (RFC 6228 §6).
func (r *Request) drop(c Change) {
	r.leave()

	if r.s.d != nil && supports199(r.req) {
		if early, err := r.build(Reply{Code: 199, Header: c.Header}); err == nil {
			r.complete(early)
			_ = r.tx.Respond(early)
		}
	}

	r.s.end(End{Cause: EndBye})
}

func supports199(req *sip.Request) bool {
	return req.Header.HasOptionTag("Supported", sip.OptionTag199) || req.Header.HasOptionTag("Require", sip.OptionTag199)
}

// group are the members of the INVITE of r, live, for a CANCEL or an error of its transaction.
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

// addFork adds the session of a fork of an INVITE of the peer to the tables.
func (a *Agent) addFork(s *Session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return false
	}

	e := a.entry(s.id, s.st)
	e.invite, e.invited = s, true

	return true
}
