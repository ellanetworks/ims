package server_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip/ua"
)

// device logs bob on at a second client, bob2, of his IMPU (TS 24.379 §7.3.2 step 4a), implicitly affiliated as his
// first.
func (w *world) device() *user {
	t := w.t
	w.user("bob2", 5, func(_ string, c *client.Config) {
		c.MCPTTID = mustURI(t, "sip:bob@mcptt.example.com")
		c.IMPU = mustURI(t, "sip:bob@ims.example.com")
	})

	return w.users["bob2"]
}

// sent are the requests of method the server sent to name in the trace lines.
func sent(lines []string, name, method string) int {
	return len(slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return l != "server>"+name+" "+method }))
}

// TS 24.379 §7.3.2 step 4a, §9.2.2.2.11, §10.1.1.3.2 [PM]: bob logged on at two clients, both affiliated, is invited at
// both, one participant: the first to accept joins the call, the other's INVITE is cancelled (RFC 3261 §9.1), or, of
// a 2xx too, its dialog ended with a BYE (§13.2.2.4). A client refusing leaves bob to the other.
func TestMultipleDevices(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		w := newWorld(t, nil)
		bob2 := w.device()
		silent(bob2)

		ca, res := w.established(w.users["alice"], fire)
		if res.StatusCode != 200 {
			t.Fatalf("alice's response %v", res)
		}

		// The CANCEL takes the flow of the INVITE, without Prepare: its 487 tells it.
		lines := w.trace.take()
		if sent(lines, "bob", "INVITE") != 1 || sent(lines, "bob2", "INVITE") != 1 || !slices.Contains(lines, "bob2>server 487 INVITE") {
			t.Fatalf("trace:\n%s", strings.Join(lines, "\n"))
		}

		w.users["bob"].expect(t, "incoming", "established")

		ca.Leave()
		w.n.Quiesce()
	})

	t.Run("both answer", func(t *testing.T) {
		w := newWorld(t, nil)
		bob2 := w.device()

		ca, res := w.established(w.users["alice"], fire)
		if res.StatusCode != 200 {
			t.Fatalf("alice's response %v", res)
		}

		lines := w.trace.take()
		if sent(lines, "bob", "INVITE") != 1 || sent(lines, "bob2", "INVITE") != 1 || sent(lines, "bob", "BYE")+sent(lines, "bob2", "BYE") != 1 {
			t.Fatalf("trace:\n%s", strings.Join(lines, "\n"))
		}

		bob, kept := w.users["bob"], 0

		for _, u := range []*user{bob, bob2} {
			if !slices.Contains(kinds(u.take()), "ended") {
				kept++
			}
		}

		if kept != 1 {
			t.Fatalf("%d of bob's clients in the call", kept)
		}

		ca.Leave()
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

		// bob2 is the participant: the floor is taken for it.
		w.talk(ca, 0)
		w.n.Quiesce()
		bob2.expectAll(t, "floor:taken")

		ca.Leave()
		w.n.Quiesce()
	})
}

// TS 24.379 §10.1.1.4.2 step 15: bob invited, not answering yet, joins from another client of his: his invitation is
// superseded, cancelled (RFC 3261 §9.1), one leg of bob's in the call.
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

	if lines := w.trace.take(); !slices.Contains(lines, "bob>server 487 INVITE") {
		t.Fatalf("trace:\n%s", strings.Join(lines, "\n"))
	}

	c2.Leave()
	ca.Leave()
	w.n.Quiesce()
}
