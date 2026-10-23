package client_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

const police = "sip:police@mcptt.example.com"

// configuration is alice, a member of fire and police, affiliated to fire on registration.
func configuration() settings.Settings {
	group := func(uri string) settings.Group {
		return settings.Group{
			URI: uri, InviteMembers: true, TNG3: 10 * time.Minute,
			List: settings.List{Entries: []settings.Entry{{URI: "sip:alice@mcptt.example.com"}}},
		}
	}

	return settings.Settings{
		System: settings.System{ParticipatingPSI: psi, ControllingPSI: "sip:mcptt-controlling@mcptt.example.com"},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    domain,
			OnNetwork: settings.OnNetwork{FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)}},
		},
		Users: []settings.User{{
			MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:alice@ims.example.com"},
			ImplicitAffiliations: []string{fire},
		}},
		Groups: []settings.Group{group(fire), group(police)},
	}
}

type fixed struct{ s *settings.Snapshot }

func (f fixed) Current() *settings.Snapshot { return f.s }

// direct is the server in direct mode [PM 14] on the net, and a client of alice sending to it.
func direct(t *testing.T) (*env, *server.Server, *user) {
	t.Helper()

	snap, err := settings.New(configuration())
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)

	s, err := server.New(server.Config{
		UA: ua.Config{
			Transaction: e.n.Config(transaction.Config{}),
			Prepare:     func(*sip.Request) error { return fmt.Errorf("the server sends no request") },
		},
		Source: fixed{snap},
		Mode:   registrar.Direct,
	})
	if err != nil {
		t.Fatal(err)
	}

	addr := e.n.Serve(s.Agent().Layer())

	return e, s, e.user("alice", 1, addr, nil)
}

// result records the final response of a request of the client.
type result struct {
	res *sip.Response
	err error
}

func (r *result) done(res *sip.Response, err error) { r.res, r.err = res, err }

func (r *result) code(t *testing.T) int {
	t.Helper()

	if r.err != nil {
		t.Fatal(r.err)
	}

	if r.res == nil {
		t.Fatal("no response")
	}

	return r.res.StatusCode
}

// TS 24.379 §7.2.1, §7.3.2 [PM 14]: the client registers with the server, its access token and client ID in the
// mcptt-info; refreshes halfway through the duration granted; unregisters.
func TestRegister(t *testing.T) {
	e, s, a := direct(t)
	id := mustURI(t, a.id)

	var r result
	if err := a.Register(600*time.Second, r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	if r.code(t) != 200 {
		t.Fatalf("REGISTER: %d", r.code(t))
	}

	bs := s.Registrar().Bindings(id)
	if len(bs) != 1 || bs[0].ClientID != "urn:uuid:00000000-0000-1000-8000-000000000001" || len(bs[0].Contacts) != 1 {
		t.Fatalf("bindings %+v", bs)
	}

	if fs, _ := bs[0].Contacts[0].Features(); !fs.Has("g.3gpp.mcptt") || len(fs.Tokens("g.3gpp.icsi-ref")) != 1 {
		t.Errorf("contact features %v", fs)
	}

	// Refreshed at 300 s, the binding outlives the first 600 s.
	e.n.Advance(301 * time.Second)

	es := a.expect(t, "registration")
	if es[0].Response == nil || es[0].Response.StatusCode != 200 {
		t.Fatalf("refresh %+v", es[0])
	}

	e.n.Advance(400 * time.Second)

	if len(s.Registrar().Bindings(id)) != 1 {
		t.Fatal("refreshed binding expired")
	}

	a.events.take()

	r = result{}
	if err := a.Unregister(r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	if r.code(t) != 200 || len(s.Registrar().Bindings(id)) != 0 {
		t.Fatalf("unregister: %d %+v", r.code(t), s.Registrar().Bindings(id))
	}

	// No refresh once unregistered.
	e.n.Advance(time.Hour)
	a.expect(t)
}

// TS 24.379 §9.2.1.2, §9.2.2.2.3: the client affiliates to groups, changes them, and de-affiliates from all with
// Expires 0.
func TestAffiliate(t *testing.T) {
	e, s, a := direct(t)
	id := mustURI(t, a.id)
	clientID := "urn:uuid:00000000-0000-1000-8000-000000000001"

	affiliated := func(group string) bool {
		now := s.Agent().Layer().Clock().Now()
		return s.Participating().Affiliated(now, id, clientID, mustURI(t, group))
	}

	var r result
	if err := a.Register(600*time.Second, r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	if r.code(t) != 200 || !affiliated(fire) {
		t.Fatalf("REGISTER: %d, implicitly affiliated %v", r.code(t), affiliated(fire))
	}

	for _, tc := range []struct {
		groups       []string
		fire, police bool
	}{
		{[]string{fire, police}, true, true},
		{[]string{police}, false, true},
		{nil, false, false},
		{[]string{fire}, true, false},
	} {
		var groups []sip.URI
		for _, g := range tc.groups {
			groups = append(groups, mustURI(t, g))
		}

		r = result{}
		if err := a.Affiliate(groups, r.done); err != nil {
			t.Fatal(err)
		}

		e.n.Quiesce()

		if r.code(t) != 200 || affiliated(fire) != tc.fire || affiliated(police) != tc.police {
			t.Errorf("%v: %d, fire %v police %v", tc.groups, r.code(t), affiliated(fire), affiliated(police))
		}
	}
}
