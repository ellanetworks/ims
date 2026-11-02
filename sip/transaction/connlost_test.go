package transaction_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip"
)

// RFC 3261 section 18.2.2
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
