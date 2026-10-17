package ua

import (
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// RFC 5057 Table 2, §5.2, notes 3, 8, 12.
func TestFailureOf(t *testing.T) {
	for _, c := range []struct {
		method string
		code   int
		err    error
		want   effect
	}{
		{"NOTIFY", 481, nil, destroysUsage},
		{"CANCEL", 481, nil, affectsTransaction},
		{"OPTIONS", 481, nil, affectsTransaction},
		{"INFO", 405, nil, affectsTransaction},
		{"NOTIFY", 405, nil, destroysUsage},
		{"INVITE", 501, nil, destroysUsage},
		{"SUBSCRIBE", 489, nil, destroysUsage},
		{"INFO", 489, nil, affectsTransaction},
		{"UPDATE", 404, nil, destroysDialog},
		{"MESSAGE", 502, nil, destroysDialog},
		{"BYE", 408, nil, destroysUsage},
		{"MESSAGE", 408, nil, affectsTransaction},
		{"NOTIFY", 0, transaction.ErrTimeout, destroysUsage},
		{"OPTIONS", 0, transaction.ErrTimeout, affectsTransaction},
		{"INVITE", 486, nil, affectsTransaction},
		{"INVITE", 603, nil, affectsTransaction},
	} {
		var res *sip.Response
		if c.code != 0 {
			res = &sip.Response{StatusCode: c.code}
		}

		if got := failureOf(c.method, res, c.err); got != c.want {
			t.Errorf("%s %d %v: %d, want %d", c.method, c.code, c.err, got, c.want)
		}
	}
}
