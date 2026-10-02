package ipsec

import (
	"math/rand/v2"
	"slices"
	"sync"
)

// MinSPI is the lowest SPI the P-CSCF allocates. RFC 4303 reserves 1-255, and
// some phones reject SPIs below 4096.
const MinSPI = 1 << 16

// SPIs allocates the SPIs of the P-CSCF's inbound SAs. They are unique among
// the SAs in use and differ from the UE's (TS 33.203 §7.1, §7.2).
type SPIs struct {
	mu   sync.Mutex
	used map[uint32]bool
}

func NewSPIs() *SPIs {
	return &SPIs{used: make(map[uint32]bool)}
}

// Allocate returns two distinct free SPIs, none of them in avoid.
func (a *SPIs) Allocate(avoid ...uint32) (uint32, uint32, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var out [2]uint32

	for i := range out {
		for try := 0; ; try++ {
			if try == 64 {
				for _, s := range out[:i] {
					delete(a.used, s)
				}

				return 0, 0, ErrSPIsExhausted
			}

			spi := MinSPI + rand.Uint32N(1<<32-MinSPI)
			if a.used[spi] || slices.Contains(avoid, spi) {
				continue
			}

			a.used[spi] = true
			out[i] = spi

			break
		}
	}

	return out[0], out[1], nil
}

// Reserve marks SPIs in use, as found at startup.
func (a *SPIs) Reserve(spis ...uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, s := range spis {
		a.used[s] = true
	}
}

func (a *SPIs) Release(spis ...uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, s := range spis {
		delete(a.used, s)
	}
}
