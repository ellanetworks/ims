package transaction_test

import (
	"errors"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

// RFC 3261 §17.1.4, §18.4: the loss of the connection a request went out on, before a response, is a transport error
// at once, rather than the timeout of Timer B or F.
func TestClientConnectionLost(t *testing.T) {
	for _, method := range []string{"INVITE", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			tx, rs, _ := h.request(method, sip.TCP)

			h.peer.CloseConn(h.local)

			err := rs.NextError()
			if !errors.Is(err, transaction.ErrTransport) || !errors.Is(err, transport.ErrConnectionLost) {
				t.Fatalf("error %v, want a lost connection", err)
			}

			wantTerminated(t, tx, transaction.ErrTransport)
		})
	}
}

// RFC 3261 Figure 6: a non-INVITE client transaction in Proceeding fails on a transport error too.
func TestClientConnectionLostInProceeding(t *testing.T) {
	h := newHarness(t)
	tx, rs, sent := h.request("OPTIONS", sip.TCP)

	h.reply(sent, 100)
	wantCode(t, rs.Next(), 100)

	h.peer.CloseConn(h.local)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTransport) {
		t.Fatalf("error %v, want a transport error", err)
	}

	wantTerminated(t, tx, transaction.ErrTransport)
}

// RFC 3261 Figure 5, RFC 6026: only Calling takes a transport error; an INVITE answered with a provisional response
// waits for its final one, which can come on another connection (RFC 3261 §18.2.2). An answered non-INVITE has
// nothing to wait for.
func TestClientConnectionLostAfterResponse(t *testing.T) {
	for _, c := range []struct {
		method string
		code   int
		state  transaction.State
	}{
		{"INVITE", 180, transaction.Proceeding},
		{"OPTIONS", 200, transaction.Terminated},
	} {
		t.Run(c.method, func(t *testing.T) {
			h := newHarness(t)
			tx, rs, sent := h.request(c.method, sip.TCP)

			h.reply(sent, c.code)
			wantCode(t, rs.Next(), c.code)

			h.peer.CloseConn(h.local)

			rs.None(quiet)
			wantState(t, tx, c.state)
		})
	}
}

// A request after the loss goes out on a new connection of the flow, which the old one's loss does not fail.
func TestClientRequestAfterConnectionLost(t *testing.T) {
	h := newHarness(t)
	_, rs, _ := h.request("OPTIONS", sip.TCP)

	h.peer.CloseConn(h.local)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTransport) {
		t.Fatalf("error %v, want a transport error", err)
	}

	tx, rs, sent := h.request("OPTIONS", sip.TCP)

	rs.None(quiet)
	h.reply(sent, 200)
	wantCode(t, rs.Next(), 200)
	wantTerminated(t, tx, nil)
}
