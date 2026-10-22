package ua

import (
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

// The Call-ID of an initial request is a word of its own, of a tel URI as of a SIP URI (RFC 3261 §8.1.1.4, §25.1).
func TestCallIDOfInitial(t *testing.T) {
	for _, target := range []string{"tel:+15550000003", "sip:bob@example.com"} {
		uri, err := sip.ParseURI(target)
		if err != nil {
			t.Fatal(err)
		}

		req := sip.NewRequest("INVITE", uri)
		req.Header.Add("From", "<sip:alice@example.com>")
		req.Header.Add("To", "<"+target+">")

		if err := (&Agent{}).completeInitial(req); err != nil {
			t.Fatal(err)
		}

		id := req.Header.CallID()
		if id == "" || strings.Contains(id, "@") {
			t.Errorf("%s: Call-ID %q", target, id)
		}
	}
}
