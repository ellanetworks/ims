package transaction_test

import (
	"strconv"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

// RFC 3261 §12.2.2: the requests of a flow leave in the order the layer took them, so that a dialog's CSeq order
// survives; an ACK queued before a request leaves before it.
func TestRequestsLeaveInOrder(t *testing.T) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			h := newHarness(t)

			const n = 300

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
