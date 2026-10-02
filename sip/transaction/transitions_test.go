package transaction_test

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

func TestClientTimeoutTCP(t *testing.T) {
	for _, method := range []string{"INVITE", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			tx, rs, _ := h.request(method, sip.TCP)

			h.advance(64*t1 - time.Millisecond)
			h.peer.RecvNone(quiet)
			h.advance(time.Millisecond)

			if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
				t.Fatalf("error %v", err)
			}

			wantTerminated(t, tx, transaction.ErrTimeout)
		})
	}
}

func TestClientTimerFInProceeding(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("OPTIONS", sip.UDP)

	h.reply(sent, 100)
	wantCode(t, rs.Next(), 100)
	wantState(t, tx, transaction.Proceeding)

	h.advance(64 * t1)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("error %v", err)
	}

	wantTerminated(t, tx, transaction.ErrTimeout)
}

func TestClientProceedingToAccepted(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("INVITE", sip.UDP)

	h.reply(sent, 180)
	wantCode(t, rs.Next(), 180)
	wantState(t, tx, transaction.Proceeding)

	h.reply(sent, 200)
	wantCode(t, rs.Next(), 200)
	wantState(t, tx, transaction.Accepted)

	h.reply(sent, 183)
	h.reply(sent, 486)
	h.reply(sent, 200)
	wantCode(t, rs.Next(), 200)
	rs.None(0)
	h.peer.RecvNone(quiet)
	wantState(t, tx, transaction.Accepted)
}

func TestClientInitialSendFailure(t *testing.T) {
	h := newHarness(t)

	req := h.newRequest("OPTIONS", sip.UDP)
	req.Flow.Remote = netip.MustParseAddrPort("[::1]:5060")

	rs := siptest.NewResponses(t)

	tx, err := h.l.Request(req, rs)
	if err != nil {
		t.Fatal(err)
	}

	if err := rs.NextError(); err == nil || errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("error %v, want the transport's", err)
	}

	wantTerminated(t, tx, tx.Err())

	if tx.Err() == nil {
		t.Fatal("Err() is nil after a failed send")
	}
}

func TestServerInviteTCPTimers(t *testing.T) {
	t.Run("H", func(t *testing.T) {
		h := newHarness(t)
		tx, _ := h.serve("INVITE", sip.TCP)
		h.respond(tx, 486)
		h.wantResponse(486)

		h.advance(64*t1 - time.Millisecond)
		h.peer.RecvNone(quiet)
		wantState(t, tx, transaction.Completed)
		h.advance(time.Millisecond)
		wantTerminated(t, tx, transaction.ErrTimeout)

		if e := h.tu.NextError(); !errors.Is(e.Err, transaction.ErrTimeout) {
			t.Fatalf("HandleTransactionError(%v)", e.Err)
		}
	})

	t.Run("L", func(t *testing.T) {
		h := newHarness(t)
		tx, _ := h.serve("INVITE", sip.TCP)
		h.respond(tx, 200)
		h.wantResponse(200)

		h.advance(64*t1 - time.Millisecond)
		wantState(t, tx, transaction.Accepted)
		h.advance(time.Millisecond)
		wantTerminated(t, tx, nil)
	})
}

func TestServerNonInviteTryingToCompleted(t *testing.T) {
	h := newHarness(t)
	tx, req := h.serve("OPTIONS", sip.UDP)

	h.respond(tx, 200)
	h.wantResponse(200)
	wantState(t, tx, transaction.Completed)

	h.peer.Send(sip.UDP, h.local, req)
	h.wantResponse(200)

	h.advance(64 * t1)
	wantTerminated(t, tx, nil)
}

func TestServerRetransmissionFailure(t *testing.T) {
	h := newHarnessConfig(t, transaction.Config{
		ResponseFlow: func(req *sip.Request, _ *sip.Response) (sip.Flow, bool, error) {
			return sip.Flow{Transport: sip.UDP, Local: req.Flow.Local, Remote: netip.MustParseAddrPort("[::1]:5060")}, true, nil
		},
	})

	h.peerRequest("INVITE", sip.UDP)
	tx := h.tu.NextRequest().Tx
	h.tu.NextError()

	h.respond(tx, 486)
	h.tu.NextError()
	wantState(t, tx, transaction.Completed)

	h.advance(t1)

	if e := h.tu.NextError(); e.Tx != tx || e.Err == nil {
		t.Fatalf("HandleTransactionError(%p, %v)", e.Tx, e.Err)
	}

	wantState(t, tx, transaction.Completed)

	if err := tx.Err(); err != nil {
		t.Fatalf("Err() = %v while Completed", err)
	}
}
