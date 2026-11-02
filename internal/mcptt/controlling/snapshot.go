package controlling

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/ellanetworks/ims/sip"
)

type Call struct {
	Group                  sip.URI
	Session                sip.URI
	ID                     string
	Originator             sip.URI
	Created, Started, Ends time.Time
	Talker                 sip.URI
	TalkingSince           time.Time
	Participants           []Participant
}

type Participant struct {
	ID                    sip.URI
	Joined                bool
	Required, ReceiveOnly bool
}

func (c *Controlling) Calls(ctx context.Context) ([]Call, error) {
	c.mu.Lock()

	sessions := make(map[string]*session, len(c.sessions))
	maps.Copy(sessions, c.sessions)

	c.mu.Unlock()

	type copied struct {
		id   string
		call *Call
	}

	copies := make(chan copied, len(sessions))

	for id, s := range sessions {
		s.grp.st.Post(func() { copies <- copied{id, s.snapshot()} })
	}

	out := make([]Call, 0, len(sessions))

	for answered := range len(sessions) {
		select {
		case cp := <-copies:
			delete(sessions, cp.id)

			if cp.call != nil {
				out = append(out, *cp.call)
			}
		case <-ctx.Done():
			if answered == 0 {
				return nil, ctx.Err()
			}

			c.cfg.Logger.Warn("calls listed without those of busy groups",
				slog.Any("call_ids", slices.Sorted(maps.Keys(sessions))), slog.Any("error", ctx.Err()))

			return out, nil
		}
	}

	return out, nil
}

func (c *Controlling) Call(ctx context.Context, id string) (Call, bool, error) {
	c.mu.Lock()
	s := c.sessions[id]
	c.mu.Unlock()

	if s == nil {
		return Call{}, false, nil
	}

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
