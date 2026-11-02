package ue

import (
	"errors"
	"math/rand/v2"
	"net/netip"
	"slices"
	"time"
)

// TS 24.229 clause 5.1.9
var ErrPCSCFUnavailable = errors.New("ue: P-CSCF marked unavailable")

// TS 24.229 clause 5.1.9
// RFC 5626 section 4.5
type pcscfMark struct {
	expires  time.Time
	failures int
}

const (
	// RFC 5626 section 4.5
	// TS 24.229 clause 5.1.9
	markBase  = 30 * time.Second
	markMax   = 1800 * time.Second
	markExtra = 5 * time.Minute
)

// TS 24.229 clause 5.1.9
// RFC 5626 section 4.5
func markDuration(failures int) time.Duration {
	w := markMax
	if failures < 6 {
		w = min(markMax, markBase<<failures)
	}

	return w/2 + time.Duration(rand.Int64N(int64(w/2)+1)) + markExtra
}

// TS 24.229 clause 5.1.2A.1.6
func (u *UE) markLocked(p netip.AddrPort) {
	m := u.marks[p]
	m.failures++
	m.expires = u.layer.Clock().Now().Add(markDuration(m.failures))
	u.marks[p] = m
}

// TS 24.229 clause 5.1.9
func (u *UE) markedLocked(p netip.AddrPort) bool {
	m, ok := u.marks[p]
	return ok && u.layer.Clock().Now().Before(m.expires)
}

func (u *UE) unavailableLocked() []netip.AddrPort {
	var out []netip.AddrPort

	for p := range u.marks {
		if u.markedLocked(p) {
			out = append(out, p)
		}
	}

	slices.SortFunc(out, func(a, b netip.AddrPort) int { return a.Compare(b) })

	return out
}

// TS 24.229 clause 5.1.9
func (u *UE) ClearPCSCFMarks() {
	u.mu.Lock()
	defer u.mu.Unlock()

	clear(u.marks)
}
