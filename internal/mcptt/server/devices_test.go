package server_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip/ua"
)

func (w *world) device() *user {
	t := w.t
	w.user("bob2", 5, func(_ string, c *client.Config) {
		c.MCPTTID = mustURI(t, "sip:bob@mcptt.example.com")
		c.IMPU = mustURI(t, "sip:bob@ims.example.com")
	})

	return w.users["bob2"]
}

func sent(lines []string, name, method string) int {
	return len(slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return l != "server>"+name+" "+method }))
}

// TS 24.379 clause 7.3.2
// TS 24.379 clause 9.2.2.2.11
// TS 24.379 clause 10.1.1.3.2
// RFC 3261 section 9.1
// RFC 3261 section 13.2.2.4
func TestMultipleDevices(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		w := newWorld(t, nil)
		bob2 := w.device()
		silent(bob2)

		ca, res := w.established(w.users["alice"], fire)
		if res.StatusCode != 200 {
			t.Fatalf("alice's response %v", res)
		}

		w.n.Quiesce()

		lines := w.trace.take()
		if sent(lines, "bob", "INVITE") != 1 || sent(lines, "bob2", "INVITE") != 1 || !slices.Contains(lines, "bob2>server 487 INVITE") {
			t.Fatalf("trace:\n%s", strings.Join(lines, "\n"))
		}

		w.users["bob"].expect(t, "incoming", "established")

		ca.Leave(context.Background())
		w.n.Quiesce()
	})

	t.Run("both answer", func(t *testing.T) {
		w := newWorld(t, nil)
		w.device()

		ca, res := w.established(w.users["alice"], fire)
		if res.StatusCode != 200 {
			t.Fatalf("alice's response %v", res)
		}

		w.n.Quiesce()

		lines, kept := w.trace.take(), 0

		for _, name := range []string{"bob", "bob2"} {
			answered := slices.Contains(lines, name+">server 200 INVITE")
			ended := sent(lines, name, "BYE") == 1

			switch {
			case sent(lines, name, "INVITE") != 1:
				t.Fatalf("%s not invited once:\n%s", name, strings.Join(lines, "\n"))
			case answered && !ended:
				kept++
			case !answered && !slices.Contains(lines, name+">server 487 INVITE"):
				t.Fatalf("%s neither answered nor cancelled:\n%s", name, strings.Join(lines, "\n"))
			}
		}

		if kept != 1 {
			t.Fatalf("%d of bob's clients in the call:\n%s", kept, strings.Join(lines, "\n"))
		}

		ca.Leave(context.Background())
		w.n.Quiesce()
	})

	t.Run("one refuses", func(t *testing.T) {
		w := newWorld(t, nil)
		bob := w.users["bob"]
		bob2 := w.device()
		bob.onInvite = func(r *ua.Request) ua.Handler {
			_ = r.Reject(486)
			return ua.Handler{}
		}

		ca, res := w.established(w.users["alice"], fire)
		if res.StatusCode != 200 {
			t.Fatalf("alice's response %v", res)
		}

		bob2.expect(t, "incoming", "established")

		w.talk(ca, 0)
		w.n.Quiesce()
		bob2.expectAll(t, "floor:taken")

		ca.Leave(context.Background())
		w.n.Quiesce()
	})
}

// TS 24.379 clause 10.1.1.4.2
// RFC 3261 section 9.1
func TestJoinSupersedesInvitation(t *testing.T) {
	w := newWorld(t, nil)
	silent(w.users["bob"])

	ca, res := w.established(w.users["alice"], fire)
	if res.StatusCode != 200 {
		t.Fatalf("alice's response %v", res)
	}

	w.trace.take()

	bob2 := w.device()

	c2, res := w.established(bob2, fire)
	if !warned(res, identity.WarnSessionExists) {
		t.Errorf("bob2's 200 warnings %v", res.Header.Values("Warning"))
	}

	w.n.Quiesce()

	if lines := w.trace.take(); !slices.Contains(lines, "bob>server 487 INVITE") {
		t.Fatalf("trace:\n%s", strings.Join(lines, "\n"))
	}

	c2.Leave(context.Background())
	ca.Leave(context.Background())
	w.n.Quiesce()
}
