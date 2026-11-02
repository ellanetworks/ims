package ipsmgw

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter/s6c"
)

const correlationLifetime = 5 * time.Minute

// TS 23.204 clause 6.4
type mtContext struct {
	msisdn  string
	imsi    string
	routing s6c.Routing
	single  bool
	expires time.Time
}

type correlations struct {
	mu  sync.Mutex
	ids map[string]*mtContext
}

// TS 23.040 clause 3.2.7a
func (s *correlations) add(prefix string, c mtContext, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ids == nil {
		s.ids = map[string]*mtContext{}
	}

	for id, e := range s.ids {
		if !e.expires.After(now) {
			delete(s.ids, id)
		}
	}

	for range 8 {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
		if err != nil {
			return "", err
		}

		id := prefix + fmt.Sprintf("%09d", n.Int64())

		if _, taken := s.ids[id]; !taken {
			c.expires = now.Add(correlationLifetime)
			s.ids[id] = &c

			return id, nil
		}
	}

	return "", fmt.Errorf("ipsmgw: no free MT Correlation ID")
}

func (s *correlations) get(id string, now time.Time) (mtContext, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.ids[id]
	if !ok || !c.expires.After(now) {
		delete(s.ids, id)

		return mtContext{}, false
	}

	c.expires = now.Add(correlationLifetime)

	return *c, true
}
