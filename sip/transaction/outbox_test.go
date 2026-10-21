package transaction_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

// RFC 3261 §12.2.2: the requests of a flow leave in the order the layer took them, so that a dialog's CSeq order
// survives; an ACK queued before a request leaves before it.
func TestRequestsLeaveInOrder(t *testing.T) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			h := newHarness(t)

			// Over UDP, a burst the kernel may drop under load, which the fake clock never retransmits: fewer
			// requests prove the same order.
			n := 300
			if tr == sip.UDP {
				n = 50
			}

			for i := range n {
				req := h.newRequest("INFO", tr)
				req.Header.Set("CSeq", sip.CSeq{Seq: uint32(i + 1), Method: "INFO"}.String())

				if _, err := h.l.Request(req, siptest.NewResponses(t)); err != nil {
					t.Fatal(err)
				}
			}

			ack := h.newRequest("ACK", tr)
			ack.Header.Set("CSeq", "1 ACK")

			if err := h.l.QueueAck(ack, nil); err != nil {
				t.Fatal(err)
			}

			bye := h.newRequest("BYE", tr)
			bye.Header.Set("CSeq", strconv.Itoa(n+1)+" BYE")

			if _, err := h.l.Request(bye, siptest.NewResponses(t)); err != nil {
				t.Fatal(err)
			}

			for i := range n {
				got, _ := h.peer.RecvRequest()
				if cseq, _ := got.Header.CSeq(); cseq.Seq != uint32(i+1) {
					t.Fatalf("request %d has CSeq %d", i+1, cseq.Seq)
				}
			}

			for _, want := range []string{"ACK", "BYE"} {
				if got, _ := h.peer.RecvRequest(); got.Method != want {
					t.Fatalf("got %s, want %s", got.Method, want)
				}
			}
		})
	}
}

// Shutdown sends the messages the layer took before closing the transport.
func TestShutdownSendsWhatItTook(t *testing.T) {
	h := newHarness(t)

	const n = 50

	for i := range n {
		req := h.newRequest("MESSAGE", sip.TCP)
		req.Header.Set("CSeq", sip.CSeq{Seq: uint32(i + 1), Method: "MESSAGE"}.String())

		if _, err := h.l.Request(req, siptest.NewResponses(t)); err != nil {
			t.Fatal(err)
		}
	}

	if err := h.l.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	for range n {
		h.peer.RecvRequest()
	}
}

// Shutdown returns the context's error when what the layer took is not sent in time, and closes the layer all the
// same.
func TestShutdownDeadline(t *testing.T) {
	h := newHarness(t)

	h.l.HoldFlow(sip.Flow{Transport: sip.TCP, Local: h.local, Remote: h.peer.Addr()})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if err := h.l.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown: %v, want %v", err, context.DeadlineExceeded)
	}

	req := h.newRequest("MESSAGE", sip.TCP)
	if _, err := h.l.Request(req, siptest.NewResponses(t)); !errors.Is(err, transaction.ErrClosed) {
		t.Errorf("Request after Shutdown: %v, want %v", err, transaction.ErrClosed)
	}
}
