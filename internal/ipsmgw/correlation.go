package ipsmgw

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter/s6c"
)

// correlationLifetime is how long an MT Correlation ID lasts after its last use: the SMS-GMSC forwards the short
// message right after the routing information, and the next ones of the recipient within the same session.
const correlationLifetime = 5 * time.Minute

// mtContext is what an MT Correlation ID stands for (TS 23.204 §6.4 step 3c): the receiving user, and the serving
// nodes the HSS gave the IP-SM-GW for delivery over NAS.
type mtContext struct {
	msisdn  string
	imsi    string
	routing s6c.Routing
	// single is of the Single-Attempt-Delivery of the SMS-GMSC's routing request, which its report carries.
	single  bool
	expires time.Time
}

// correlations are the MT Correlation IDs the IP-SM-GW gave the SMS-GMSCs, by ID.
type correlations struct {
	mu  sync.Mutex
	ids map[string]*mtContext
}

// add stores c under a new MT Correlation ID of the HPLMN of prefix, the MCC and MNC of 6 digits (TS 23.040
// §3.2.7a), and returns it.
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
		// The Sender ID: nine decimal digits, at random (TS 23.040 §3.2.7a).
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

// get is the context of the MT Correlation ID id, if live, whose lifetime it extends.
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
