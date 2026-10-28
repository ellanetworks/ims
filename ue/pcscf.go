package ue

import (
	"errors"
	"math/rand/v2"
	"net/netip"
	"slices"
	"time"
)

// ErrPCSCFUnavailable is the error of an initial registration through a P-CSCF marked unavailable, which the UE does
// not use (TS 24.229 §5.1.9): the owner selects another one (SetPCSCF), or discovers them anew (ClearPCSCFMarks).
var ErrPCSCFUnavailable = errors.New("ue: P-CSCF marked unavailable")

// pcscfMark marks a P-CSCF address unavailable until expires (TS 24.229 §5.1.9). failures counts its consecutive
// marks since it last served a registration, which lengthen the next (RFC 5626 §4.5).
type pcscfMark struct {
	expires  time.Time
	failures int
}

const (
	// markBase is the base-time of RFC 5626 §4.5 with every flow failed, as the one flow of a UE; markMax its
	// max-time; markExtra the 5 minutes TS 24.229 §5.1.9 adds to its retry delay.
	markBase  = 30 * time.Second
	markMax   = 1800 * time.Second
	markExtra = 5 * time.Minute
)

// markDuration is how long the mark of a P-CSCF lasts, its failures-th in a row (TS 24.229 §5.1.9): the retry delay
// of RFC 5626 §4.5, uniform between half and all of min(max-time, base-time·2^failures), plus 5 minutes.
func markDuration(failures int) time.Duration {
	w := markMax
	if failures < 6 {
		w = min(markMax, markBase<<failures)
	}

	return w/2 + time.Duration(rand.Int64N(int64(w/2)+1)) + markExtra
}

// markLocked marks a P-CSCF address unavailable (TS 24.229 §5.1.2A.1.6 B).
func (u *UE) markLocked(p netip.AddrPort) {
	m := u.marks[p]
	m.failures++
	m.expires = u.layer.Clock().Now().Add(markDuration(m.failures))
	u.marks[p] = m
}

// markedLocked reports whether a P-CSCF address is marked unavailable now: a mark clears once it expires (TS 24.229
// §5.1.9).
func (u *UE) markedLocked(p netip.AddrPort) bool {
	m, ok := u.marks[p]
	return ok && u.layer.Clock().Now().Before(m.expires)
}

// unavailableLocked are the P-CSCF addresses marked unavailable now, in order.
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

// ClearPCSCFMarks clears the unavailability marks of every P-CSCF address, as a new P-CSCF discovery (TS 24.229
// §9.2.1) does (§5.1.9).
func (u *UE) ClearPCSCFMarks() {
	u.mu.Lock()
	defer u.mu.Unlock()

	clear(u.marks)
}
