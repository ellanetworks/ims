package ipsec

import (
	"math/rand/v2"
	"slices"
	"sync"
)

const (
	MinSPI = 1 << 16
	MaxSPI = 1<<28 - 1
)

type SPIs struct {
	mu   sync.Mutex
	used map[uint32]bool
}

func NewSPIs() *SPIs {
	return &SPIs{used: make(map[uint32]bool)}
}

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

				return 0, 0, errSPIsExhausted
			}

			spi := MinSPI + rand.Uint32N(MaxSPI-MinSPI+1)
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
