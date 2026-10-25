package as

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

// noDNS answers no query.
type noDNS struct{}

var errNoDNS = errors.New("no DNS")

func (noDNS) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, errNoDNS
}

func (noDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, errNoDNS
}

// The host of a built-in AS resolves to it, whatever the case, port and user of the URI, until removed; the others
// through DNS, and an IP address as itself.
func TestResolve(t *testing.T) {
	r := &Resolver{DNS: &sip.Resolver{DNS: noDNS{}}}
	in := []sip.Server{{Transport: sip.UDP, Addr: netip.MustParseAddrPort("10.0.0.5:5090")}}

	r.Set("mcptt.ims.mnc001.mcc001.3gppnetwork.org", in)

	for _, s := range []string{
		"sip:mcptt.ims.mnc001.mcc001.3gppnetwork.org",
		"sip:mcptt-controlling@MCPTT.ims.mnc001.mcc001.3gppnetwork.org:5060;gr=x",
		"sip:mcptt.ims.mnc001.mcc001.3gppnetwork.org.",
	} {
		u, err := sip.ParseURI(s)
		if err != nil {
			t.Fatal(err)
		}

		if got, err := r.Resolve(t.Context(), u); err != nil || len(got) != 1 || got[0] != in[0] {
			t.Errorf("%s: %v, %v", s, got, err)
		}
	}

	if !r.Has("MCPTT.ims.mnc001.mcc001.3gppnetwork.org.") || r.Has("mcptt.example.com") {
		t.Error("Has")
	}

	other := sip.URI{Scheme: "sip", Host: "mcptt.example.com"}
	if _, err := r.Resolve(t.Context(), other); err == nil {
		t.Error("another host resolved without DNS")
	}

	if got, err := r.Resolve(t.Context(), sip.URI{Scheme: "sip", Host: "10.0.0.40", Port: 5060}); err != nil ||
		got[0].Addr != netip.MustParseAddrPort("10.0.0.40:5060") {
		t.Errorf("IP address: %v, %v", got, err)
	}

	r.Set("mcptt.ims.mnc001.mcc001.3gppnetwork.org", nil)

	if _, err := r.Resolve(t.Context(), sip.URI{Scheme: "sip", Host: "mcptt.ims.mnc001.mcc001.3gppnetwork.org"}); err == nil {
		t.Error("a removed host resolved without DNS")
	}
}
