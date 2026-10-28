package transaction_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip"
)

// RFC 3261 §18.2.2: the connection a request went out on lost, its response comes on a new one, which the server
// opens to the request's sent-by: the client transaction waits for it rather than failing.
func TestClientResponseAfterConnectionLost(t *testing.T) {
	for _, method := range []string{"INVITE", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			tx, rs, sent := h.request(method, sip.TCP)

			h.peer.CloseConn(h.local)
			rs.None(quiet)

			h.reply(sent, 486)
			wantCode(t, rs.Next(), 486)
			wantTerminated(t, tx, nil)
		})
	}
}
