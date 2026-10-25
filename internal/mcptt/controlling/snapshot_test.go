package controlling

import (
	"context"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// A group whose strand stays busy past the deadline of the listing hides only its own call: the calls of the others
// are listed. If none answered, the listing fails; the call of the busy group alone fails to be read too.
func TestCallsBusyGroup(t *testing.T) {
	n := siptest.NewNet(t)
	addr := n.Addr()

	a, err := ua.New(ua.Config{
		Transaction: n.Config(transaction.Config{}),
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: addr.Addr().String(), Port: addr.Port()}},
		Prepare:     func(*sip.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, err := settings.New(settings.Settings{
		System: settings.System{
			ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com",
			ControllingPSI:   "sip:mcptt-controlling@mcptt.example.com",
		},
		ServiceConfiguration: settings.ServiceConfiguration{Domain: "mcptt.example.com"},
		Users:                []settings.User{{MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:a@ims.example.com"}}},
		Groups: []settings.Group{
			{URI: "sip:fire@mcptt.example.com", InviteMembers: true, TNG3: 10 * time.Minute},
			{URI: "sip:police@mcptt.example.com", InviteMembers: true, TNG3: 10 * time.Minute},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	c := New(Config{Agent: a, Source: fixedSource{snap}})
	alice := sip.URI{Scheme: "sip", User: "alice", Host: "mcptt.example.com"}

	for _, gr := range []string{"fire", "police"} {
		id := sip.URI{Scheme: "sip", User: gr, Host: "mcptt.example.com"}
		c.sessions[gr] = &session{
			c: c, grp: c.group(id), snap: snap, g: snap.Group(id), id: id, gr: gr,
			origin: &member{id: alice}, fs: &floor.Server{},
		}
	}

	// Fire's strand busy until released.
	release := make(chan struct{})

	t.Cleanup(func() { close(release) })

	c.sessions["fire"].grp.st.Post(func() { <-release })

	list := func() ([]Call, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		return c.Calls(ctx)
	}

	calls, err := list()
	if err != nil || len(calls) != 1 || calls[0].ID != "police" {
		t.Fatalf("calls %+v, %v; want police's alone", calls, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, _, err := c.Call(ctx, "fire"); err == nil {
		t.Error("the call of a busy group read")
	}

	delete(c.sessions, "police")

	if calls, err := list(); err == nil {
		t.Errorf("calls %+v, none answered, without error", calls)
	}
}

type fixedSource struct{ s *settings.Snapshot }

func (f fixedSource) Current() *settings.Snapshot { return f.s }
