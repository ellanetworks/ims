package controlling

import (
	"context"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// Call is a group call of the function as it is: its session (TS 24.379 §10.1.1.4.2 step 14e) and its participants.
type Call struct {
	Group sip.URI
	// Session is the MCPTT session identity (TS 24.379 §4.5), and ID its "gr", which tells the calls apart.
	Session sip.URI
	ID      string
	// Originator is the MCPTT ID of the member whose INVITE created the session.
	Originator sip.URI
	// Created is when the session was created; Started when its originator was answered, zero while it is set up;
	// Ends when TNG3 releases it (TS 24.379 §6.3.3.5), zero if never.
	Created, Started, Ends time.Time
	// Talker is the MCPTT ID of the participant with permission to send media, and TalkingSince when it got it; zero
	// if the floor is idle (TS 24.380 §6.3.4).
	Talker       sip.URI
	TalkingSince time.Time
	// Participants are the members invited to the session and those that joined it, the originator first.
	Participants []Participant
}

// Participant is a member of a call.
type Participant struct {
	ID sip.URI
	// Joined tells the member joined the session; an invited member's clients are being invited otherwise.
	Joined bool
	// Required and ReceiveOnly are of the member's group entry (TS 24.481 §7.2.2).
	Required, ReceiveOnly bool
}

// Calls are the group calls of the function, in no order. Each session's state is of the strand of its group: a call
// is copied there, so Calls waits for each group's strand, until ctx is done.
func (c *Controlling) Calls(ctx context.Context) ([]Call, error) {
	c.mu.Lock()

	sessions := make([]*session, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}

	c.mu.Unlock()

	// Buffered: a strand that answers after ctx is done does not block.
	copies := make(chan *Call, len(sessions))

	for _, s := range sessions {
		s.grp.st.Post(func() { copies <- s.snapshot() })
	}

	out := make([]Call, 0, len(sessions))

	for range sessions {
		select {
		case call := <-copies:
			if call != nil {
				out = append(out, *call)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return out, nil
}

// Call is the group call whose MCPTT session identity has the "gr" id, and whether there is one. It waits for the
// strand of the call's group only, until ctx is done: another group's busy strand does not delay it.
func (c *Controlling) Call(ctx context.Context, id string) (Call, bool, error) {
	c.mu.Lock()
	s := c.sessions[id]
	c.mu.Unlock()

	if s == nil {
		return Call{}, false, nil
	}

	// Buffered: a strand that answers after ctx is done does not block.
	copied := make(chan *Call, 1)

	s.grp.st.Post(func() { copied <- s.snapshot() })

	select {
	case call := <-copied:
		if call == nil {
			return Call{}, false, nil
		}

		return *call, true, nil
	case <-ctx.Done():
		return Call{}, false, ctx.Err()
	}
}

// snapshot is the call of s, nil once released. On the strand of its group.
func (s *session) snapshot() *Call {
	if s.released {
		return nil
	}

	call := &Call{
		Group: s.g.ID(), Session: s.id.Clone(), ID: s.gr, Originator: s.origin.id, Created: s.created, Started: s.started,
	}

	if s.g.TNG3 > 0 {
		call.Ends = s.created.Add(s.g.TNG3)
	}

	if fm, since := s.fs.Talker(); fm != nil {
		if m := s.floors[fm]; m != nil {
			call.Talker, call.TalkingSince = m.id, since
		}
	}

	for _, m := range s.members {
		if m.state != invited && m.state != joined {
			continue
		}

		p := Participant{ID: m.id, Joined: m.state == joined, Required: m.required}
		if m.entry != nil {
			p.ReceiveOnly = m.entry.ReceiveOnly
		}

		call.Participants = append(call.Participants, p)
	}

	return call
}
