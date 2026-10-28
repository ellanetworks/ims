package transaction_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

func all(sip.Flow) bool { return true }

// TS 24.229 §5.1.1.4.1: a dropped client transaction ends at once, its user told, and sends nothing more.
func TestDropClient(t *testing.T) {
	for _, method := range []string{"INVITE", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			tx, rs, _ := h.request(method, sip.UDP)

			h.l.Drop(all)

			if err := rs.NextError(); !errors.Is(err, transaction.ErrDropped) {
				t.Fatalf("error %v, want ErrDropped", err)
			}

			wantTerminated(t, tx, transaction.ErrDropped)

			h.advance(64 * t1)
			h.peer.RecvNone(quiet)
		})
	}
}

// A dropped server transaction ends at once, its user told, and refuses its responses.
func TestDropServer(t *testing.T) {
	h := newHarness(t)
	tx, _ := h.serve("INVITE", sip.UDP)

	h.l.Drop(all)

	if e := h.tu.NextError(); e.Tx != tx || !errors.Is(e.Err, transaction.ErrDropped) {
		t.Fatalf("HandleTransactionError(%v)", e.Err)
	}

	if err := tx.Respond(sip.NewResponse(tx.Request(), 486, "")); err == nil {
		t.Fatal("a dropped transaction responded")
	}

	h.peer.RecvNone(quiet)
}

// Drop leaves the transactions of the other flows alone.
func TestDropOtherFlow(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("OPTIONS", sip.UDP)

	h.l.Drop(func(f sip.Flow) bool { return f.Remote != h.peer.Addr() })

	h.reply(sent, 200)
	wantCode(t, rs.Next(), 200)
	wantState(t, tx, transaction.Completed)
}

// OnClientError tells the state a client transaction failed in: Timer F in Trying, or in Proceeding once a
// provisional response came (TS 24.229 §5.1.2A.1.6 acts on Trying only).
func TestOnClientError(t *testing.T) {
	type failure struct {
		method string
		in     transaction.State
		err    error
	}

	for _, c := range []struct {
		name        string
		provisional bool
		want        transaction.State
	}{
		{"trying", false, transaction.Trying},
		{"proceeding", true, transaction.Proceeding},
	} {
		t.Run(c.name, func(t *testing.T) {
			var (
				mu  sync.Mutex
				got []failure
			)

			h := newHarnessConfig(t, transaction.Config{OnClientError: func(req *sip.Request, in transaction.State, err error) {
				mu.Lock()
				defer mu.Unlock()

				got = append(got, failure{req.Method, in, err})
			}})
			_, rs, sent := h.request("OPTIONS", sip.UDP)

			if c.provisional {
				h.reply(sent, 100)
				wantCode(t, rs.Next(), 100)
			}

			h.advance(64 * t1)

			if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
				t.Fatalf("error %v", err)
			}

			mu.Lock()
			defer mu.Unlock()

			if len(got) != 1 || got[0].method != "OPTIONS" || got[0].in != c.want || !errors.Is(got[0].err, transaction.ErrTimeout) {
				t.Fatalf("OnClientError %+v, want OPTIONS in %s with a timeout", got, c.want)
			}
		})
	}
}
