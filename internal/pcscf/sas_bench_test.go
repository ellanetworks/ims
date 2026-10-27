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

// BenchmarkRegistrationAmong measures a UE's challenge and registration while n other UEs are registered: the
// P-CSCF's work on its security associations, which does not depend on n.
func BenchmarkRegistrationAmong(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			a := newAssociations(context.Background(), IPsec{
				Kernel: ipsectest.NewKernel(), Grace: time.Minute, ServerPort: 5100, ClientPorts: [2]uint16{5101, 5102},
			}, slog.New(slog.DiscardHandler))
			b.Cleanup(a.close)

			ueAt := func(i int) netip.Addr { return netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}) }

			register := func(i int) *saSet {
				c := challenge{
					impi:  fmt.Sprintf("00101%010d@%s", i, homeDomain),
					local: loopback,
					ue:    ueAt(i),
					offer: ipsec.Offer{Endpoint: ipsec.Endpoint{PortC: 40000, PortS: 40001, SPIC: 1000, SPIS: 1001}},
				}

				if _, err := a.challenged(c, ipsec.Keys{}); err != nil {
					b.Fatal(err)
				}

				a.mu.Lock()
				s := a.byFlow[flowKey{netip.AddrPortFrom(c.ue, 40000), netip.AddrPortFrom(loopback, 5100)}]
				a.mu.Unlock()

				a.registered(s, outcome{lifetime: time.Hour})

				return s
			}

			for i := range n {
				register(i)
			}

			b.ResetTimer()

			for i := 0; b.Loop(); i++ {
				s := register(n + i%1000)

				a.mu.Lock()
				a.remove(s)
				a.mu.Unlock()
			}
		})
	}
}
