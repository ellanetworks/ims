package server_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/controlling"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
)

// calls are the group calls of w's server, its strands run.
func (w *world) calls() []controlling.Call {
	w.t.Helper()

	return onStrands(w, func(ctx context.Context) ([]controlling.Call, error) { return w.s.Calls().Calls(ctx) })
}

// callOf is the group call of the "gr" id of w's server, and whether there is one, its strands run.
func (w *world) callOf(id string) (controlling.Call, bool) {
	w.t.Helper()

	type found struct {
		call controlling.Call
		ok   bool
	}

	f := onStrands(w, func(ctx context.Context) (found, error) {
		c, ok, err := w.s.Calls().Call(ctx, id)
		return found{c, ok}, err
	})

	return f.call, f.ok
}

// onStrands is the result of read, which waits for strands of w's server, run meanwhile.
func onStrands[T any](w *world, read func(context.Context) (T, error)) T {
	w.t.Helper()

	type result struct {
		v   T
		err error
	}

	done := make(chan result, 1)

	go func() {
		v, err := read(context.Background())
		done <- result{v, err}
	}()

	for {
		w.n.Quiesce()

		select {
		case r := <-done:
			if r.err != nil {
				w.t.Fatal(r.err)
			}

			return r.v
		case <-time.After(time.Millisecond):
		}
	}
}

// The live state: the registered clients since their authorisation, kept by a refresh; their affiliations; the group
// call, its participants and its talker, and none once released.
func TestLiveState(t *testing.T) {
	w := newWorld(t, nil)
	alice, bob := w.users["alice"], w.users["bob"]

	clients := w.s.Registrar().Clients()
	if len(clients) != len(w.users) {
		t.Fatalf("%d clients, want %d", len(clients), len(w.users))
	}

	i := slices.IndexFunc(clients, func(c registrar.Binding) bool { return c.MCPTTID.String() == alice.id })
	if i < 0 || clients[i].Since.IsZero() || !clients[i].Since.Before(clients[i].Expires) {
		t.Fatalf("alice's client %+v", clients)
	}

	since, expires := clients[i].Since, clients[i].Expires

	affs := w.s.Participating().Snapshot(w.s.Now())
	j := slices.IndexFunc(affs, func(a affiliation.Affiliations) bool { return a.User.String() == alice.id })

	if j < 0 || !slices.ContainsFunc(affs[j].Groups, func(g affiliation.Group) bool {
		return g.ID.String() == fire && g.Status == affiliation.Affiliated
	}) {
		t.Errorf("alice's affiliations %+v", affs)
	}

	if calls := w.calls(); len(calls) != 0 {
		t.Errorf("calls before any %+v", calls)
	}

	ca, cb, cc := w.call(fire)
	w.n.Quiesce()
	w.talk(cb, time.Second)

	calls := w.calls()
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}

	c := calls[0]
	if c.Group.String() != fire || c.Originator.String() != alice.id || c.ID == "" || c.Started.IsZero() ||
		!c.Ends.Equal(c.Created.Add(10*time.Minute)) || c.Talker.String() != bob.id || c.TalkingSince.IsZero() {
		t.Errorf("call %+v", c)
	}

	joined := 0

	for _, p := range c.Participants {
		if p.Joined {
			joined++
		}
	}

	if joined != 3 || c.Participants[0].ID.String() != alice.id {
		t.Errorf("participants %+v", c.Participants)
	}

	if one, ok := w.callOf(c.ID); !ok || one.ID != c.ID || one.Talker.String() != bob.id {
		t.Errorf("call %s: %+v, %v", c.ID, one, ok)
	}

	if _, ok := w.callOf("gone"); ok {
		t.Error("a call of an unknown ID")
	}

	cb.ReleasePTT()
	w.n.Advance(time.Second)

	if calls := w.calls(); len(calls) != 1 || calls[0].Talker.Host != "" {
		t.Errorf("talker once the floor is released %+v", calls)
	}

	ca.Leave()
	cb.Leave()
	cc.Leave()
	w.n.Quiesce()

	if calls := w.calls(); len(calls) != 0 {
		t.Errorf("calls once released %+v", calls)
	}

	if _, ok := w.callOf(c.ID); ok {
		t.Errorf("call %s once released", c.ID)
	}

	// The refreshes of the registration keep when the client was authorised.
	w.n.Advance(time.Hour)

	clients = w.s.Registrar().Clients()
	if i := slices.IndexFunc(clients, func(c registrar.Binding) bool { return c.MCPTTID.String() == alice.id }); i < 0 ||
		!clients[i].Expires.After(expires) || !clients[i].Since.Equal(since) {
		t.Errorf("alice's client after refreshes %+v, since %v", clients, since)
	}
}
