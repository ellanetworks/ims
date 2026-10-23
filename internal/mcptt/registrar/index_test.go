package registrar

import (
	"maps"
	"testing"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
)

// indexed checks that the indexes of g are those of its bindings, a client bound at one IMPU at most.
func indexed(t *testing.T, g *Registrar) {
	t.Helper()

	impus, users, clients := map[string]map[bindingKey]*binding{}, map[string]map[bindingKey]*binding{}, map[string]*binding{}

	for k, b := range g.bindings {
		if b.key != k {
			t.Fatalf("binding %v at %v", b.key, k)
		}

		if clients[k.client] != nil {
			t.Fatalf("client %s bound twice", k.client)
		}

		add(impus, k.impu, b)
		add(users, b.user, b)
		clients[k.client] = b
	}

	eq := func(a, b map[bindingKey]*binding) bool { return maps.Equal(a, b) }

	if !maps.EqualFunc(g.impus, impus, eq) || !maps.EqualFunc(g.users, users, eq) || !maps.Equal(g.clients, clients) {
		t.Fatalf("indexes %v %v %v, of the bindings %v %v %v", g.impus, g.users, g.clients, impus, users, clients)
	}
}

// The bindings are found by IMPU, user and client as they change: a client bound again at another IMPU, or by
// another user, leaves its former binding (TS 24.379 §7.3.2 step 4), and the last binding of an IMPU or a user
// leaves no empty index.
func TestIndexes(t *testing.T) {
	g := New(Config{})
	alice, bob := uri(t, "sip:alice@mcptt.example.com"), uri(t, "sip:bob@mcptt.example.com")

	bind := func(impu, client, user string) *binding {
		b := &binding{
			key: bindingKey{settings.Key(uri(t, impu)), client}, user: settings.Key(uri(t, user)),
			Binding: Binding{MCPTTID: uri(t, user), ClientID: client, IMPU: uri(t, impu)},
		}

		var ch change

		g.bind(b, &ch)
		indexed(t, g)

		return b
	}

	a1 := bind("sip:a@ims.example.com", "c1", alice.String())
	bind("sip:a@ims.example.com", "c2", alice.String())
	bind("sip:b@ims.example.com", "c3", bob.String())

	if n := len(g.of(settings.Key(alice))); n != 2 || !g.Bound(alice, "c1") || g.Bound(bob, "c1") {
		t.Fatalf("alice: %d bindings, bound at c1 %v, bob at c1 %v", n, g.Bound(alice, "c1"), g.Bound(bob, "c1"))
	}

	// c1 at another IMPU, then of bob.
	bind("sip:a2@ims.example.com", "c1", alice.String())

	if g.bindings[a1.key] != nil || len(g.impus[a1.key.impu]) != 1 {
		t.Fatalf("c1 still at %s: %v", a1.key.impu, g.impus[a1.key.impu])
	}

	bind("sip:a2@ims.example.com", "c1", bob.String())

	if len(g.of(settings.Key(alice))) != 1 || len(g.of(settings.Key(bob))) != 2 {
		t.Fatalf("alice %v, bob %v", g.of(settings.Key(alice)), g.of(settings.Key(bob)))
	}

	var ch change

	for _, b := range g.bindings {
		g.unbind(b, &ch, causeDeregistered)
		indexed(t, g)
	}

	if len(g.impus) != 0 || len(g.users) != 0 || len(g.clients) != 0 {
		t.Fatalf("indexes left: %v %v %v", g.impus, g.users, g.clients)
	}
}
