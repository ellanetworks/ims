package transaction

import (
	"fmt"
	"syscall"
	"testing"

	"github.com/ellanetworks/ims/sip/transport"
)

// A failure to send that is local, not the next hop's, is no transport error (RFC 3261 §17.1.4, §18.4).
func TestNetworkFailure(t *testing.T) {
	for err, want := range map[error]bool{
		fmt.Errorf("send: %w", syscall.ECONNREFUSED):              true,
		fmt.Errorf("send: %w", ErrRefused):                        true,
		fmt.Errorf("send: %w", transport.ErrNoListener):           false,
		fmt.Errorf("send: %w", transport.ErrUnsupportedTransport): false,
		ErrClosed: false,
	} {
		if got := networkFailure(err); got != want {
			t.Errorf("networkFailure(%v) = %t, want %t", err, got, want)
		}
	}
}
