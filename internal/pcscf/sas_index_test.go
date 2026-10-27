package pcscf

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
)

// The indexes of the sets by UE and by private identity hold the sets, and only them, through challenges,
// registrations, re-authentications, a subscriber's second UE and expiries.
func TestSetIndexes(t *testing.T) {
	a := newAssociations(context.Background(), IPsec{
		Kernel: ipsectest.NewKernel(), Grace: time.Millisecond, AwaitAuth: time.Millisecond, ServerPort: 5100,
		ClientPorts: [2]uint16{5101, 5102},
	}, slog.New(slog.DiscardHandler))
	t.Cleanup(a.close)

	check := func(when string) {
		t.Helper()

		a.mu.Lock()
		defer a.mu.Unlock()

		n := 0

		for ue, sets := range a.byUE {
			for s := range sets {
				if _, ok := a.sets[s]; !ok || s.set.Remote.Addr != ue {
					t.Fatalf("%s: byUE[%s] has %v", when, ue, s.set)
				}

				n++
			}
		}

		m := 0

		for impi, sets := range a.byIMPI {
			for s := range sets {
				if _, ok := a.sets[s]; !ok || s.impi != impi {
					t.Fatalf("%s: byIMPI[%s] has %v", when, impi, s.set)
				}

				m++
			}
		}

		if n != len(a.sets) || m != len(a.sets) {
			t.Fatalf("%s: %d sets, %d by UE, %d by private identity", when, len(a.sets), n, m)
		}
	}

	register := func(impi string, ue netip.Addr, port uint16, origin *saSet) *saSet {
		t.Helper()

		c := challenge{
			impi: impi, local: loopback, ue: ue, origin: origin,
			offer: ipsec.Offer{Endpoint: ipsec.Endpoint{PortC: port, PortS: port + 1, SPIC: uint32(port), SPIS: uint32(port) + 1}},
		}

		if _, err := a.challenged(c, ipsec.Keys{}); err != nil {
			t.Fatal(err)
		}

		a.mu.Lock()
		s := a.byFlow[flowKey{netip.AddrPortFrom(ue, port), netip.AddrPortFrom(loopback, 5100)}]
		a.mu.Unlock()

		a.registered(s, outcome{lifetime: time.Hour})

		return s
	}

	ue1, ue2 := netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")

	for i := range 3 {
		impi := fmt.Sprintf("00101000000000%d@%s", i, homeDomain)

		first := register(impi, ue1.Next().Next(), 40000+uint16(10*i), nil)

		check("registered")

		register(impi, first.set.Remote.Addr, 41000+uint16(10*i), first)
		check("re-authenticated")
	}

	// A subscriber's second UE, at another address: the first's sets are shortened, not removed.
	register(testIMPI, ue1, 42000, nil)
	register(testIMPI, ue2, 42010, nil)
	check("second UE")

	a.deregistered(regKey{impi: testIMPI, ue: ue2})
	time.Sleep(50 * time.Millisecond)
	check("expired")
}
