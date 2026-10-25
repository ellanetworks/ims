package registrar

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// reconfigured records the Reconfigure calls of Revoke.
type reconfigured struct {
	mu    sync.Mutex
	calls []reconfiguration
}

func (r *reconfigured) ImplicitConfigured(time.Time, *settings.User, string) {}

func (r *reconfigured) Reconfigure(_ time.Time, before, user *settings.User, client string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, reconfiguration{before: before, user: user, client: client})
}

func (r *reconfigured) LogOff(time.Time, sip.URI, string) {}

// Revoke hands the change of a user's settings, before and after, to the clients its bindings keep authorised; not to
// a binding expired that its timer has not removed yet (RFC 3261 §10.3), whose client would be affiliated for
// MaxExpires although unreachable.
func TestRevokeReconfigures(t *testing.T) {
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

	t.Cleanup(func() { n.Await(func() { _ = a.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1) })

	snapshot := func(implicit ...string) *settings.Snapshot {
		t.Helper()

		s, err := settings.New(settings.Settings{
			System: settings.System{
				ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com",
				ControllingPSI:   "sip:mcptt-controlling@mcptt.example.com",
			},
			ServiceConfiguration: settings.ServiceConfiguration{Domain: "mcptt.example.com"},
			Users: []settings.User{{
				MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:a@ims.example.com"},
				ImplicitAffiliations: implicit,
			}},
			Groups: []settings.Group{{
				URI: "sip:fire@mcptt.example.com", InviteMembers: true, TNG3: 10 * time.Minute,
				List: settings.List{Entries: []settings.Entry{{URI: "sip:alice@mcptt.example.com"}}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	rec := &reconfigured{}
	st := a.NewStrand()
	g := New(Config{Mode: ThroughIMS, StrandOf: func(string) *ua.Strand { return st }, Affiliations: rec, Clock: a.Layer().Clock()})
	now := g.cfg.Clock.Now()
	alice, impu := uri(t, "sip:alice@mcptt.example.com"), uri(t, "sip:a@ims.example.com")

	for client, expires := range map[string]time.Time{"live": now.Add(time.Hour), "expired": now.Add(-10 * time.Second)} {
		var ch change

		g.bind(&binding{
			key: bindingKey{settings.Key(impu), client}, user: settings.Key(alice),
			Binding: Binding{MCPTTID: alice, ClientID: client, IMPU: impu, IMPUs: []sip.URI{impu}, Expires: expires},
		}, &ch)
	}

	before, after := snapshot(), snapshot("sip:fire@mcptt.example.com")
	g.Revoke(before, after)
	n.Quiesce()

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if len(rec.calls) != 1 {
		t.Fatalf("reconfigured %d clients, want 1: %+v", len(rec.calls), rec.calls)
	}

	if c := rec.calls[0]; c.client != "live" || c.before != before.User(alice) || c.user != after.User(alice) {
		t.Errorf("reconfigured %+v", c)
	}
}
