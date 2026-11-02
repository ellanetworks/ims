package ua

import (
	"fmt"
	"testing"

	"github.com/ellanetworks/ims/sip/transaction"
)

// RFC 3261 section 8.1.3.1
func TestFailureEnd(t *testing.T) {
	for err, want := range map[error]EndCause{
		transaction.ErrTimeout:                              EndTimeout,
		fmt.Errorf("%w: refused", transaction.ErrTransport): EndTransport,
		transaction.ErrDropped:                              EndDropped,
	} {
		if got := failureEnd(err).Cause; got != want {
			t.Errorf("failureEnd(%v) = %s, want %s", err, got, want)
		}
	}
}
