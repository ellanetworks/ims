package settings

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/sip"
)

// FuzzSettings parses arbitrary settings, and checks of each accepted that its identities are found, its members are
// users, and the floor timers of its groups are ones floor accepts.
func FuzzSettings(f *testing.F) {
	// Valid settings, and invalid ones.
	b, err := json.Marshal(valid())
	if err != nil {
		f.Fatal(err)
	}

	if _, err := decode(b); err != nil {
		f.Fatalf("valid settings: %v", err)
	}

	f.Add(b)

	f.Add([]byte(`{"Users": [{"MCPTTUserID": "sip:a@x", "IMPUs": ["tel:+1"]}]}`))
	f.Add([]byte(`{"Groups": [{"URI": "sip:g@x", "List": {"Entries": [{"URI": "sip:a@x", "UserPriority": 3}]}}]}`))

	f.Fuzz(func(t *testing.T, in []byte) {
		s, err := decode(in)
		if err != nil {
			return
		}

		if s.User(s.ParticipatingPSI()) != nil || s.Group(s.ControllingPSI()) != nil {
			t.Fatal("a PSI is a user or a group")
		}

		for _, u := range s.cfg.Users {
			id := parse(t, u.MCPTTUserID)
			if s.User(id) == nil || s.Group(id) != nil {
				t.Fatalf("user %s", id)
			}

			for _, i := range u.IMPUs {
				if !s.User(id).HasIMPU(parse(t, i)) {
					t.Fatalf("user %s without IMPU %s", id, i)
				}
			}
		}

		for i := range s.cfg.Groups {
			g := &s.cfg.Groups[i]
			if s.Group(parse(t, g.URI)) != g {
				t.Fatalf("group %s", g.URI)
			}

			for _, e := range g.List.Entries {
				if id := parse(t, e.URI); s.User(id) == nil || g.Entry(id) == nil {
					t.Fatalf("member %s", e.URI)
				}
			}

			timers := s.ServerTimers(g)
			if _, err := floor.NewServer(floor.ServerConfig{Clock: nopClock{}, Out: nopOut{}, Timers: timers}); err != nil {
				t.Fatalf("group %s: %v", g.URI, err)
			}
		}
	})
}

// decode decodes settings of the JSON of their Go names, of known fields only, and validates them.
func decode(data []byte) (*Snapshot, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var s Settings
	if err := dec.Decode(&s); err != nil {
		return nil, err
	}

	return New(s)
}

func parse(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

type nopClock struct{}

func (nopClock) AfterFunc(time.Duration, func()) floor.Timer { return nopTimer{} }

func (nopClock) Now() time.Time { return time.Time{} }

type nopTimer struct{}

func (nopTimer) Stop() bool { return false }

type nopOut struct{}

func (nopOut) Send(*floor.Member, floorctl.Message) {}

func (nopOut) Forward(*floor.Member, bool) {}

func (nopOut) Inactive() {}

func (nopOut) Disconnect(*floor.Member) {}
