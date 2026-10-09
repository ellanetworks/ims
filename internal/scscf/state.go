package scscf

import (
	"context"
	"slices"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

type state struct {
	impi string
	regs []db.Registration
	now  time.Time
}

func (r *Registrar) load(ctx context.Context, impi string) (*state, error) {
	regs, err := r.cfg.DB.ListRegistrationsByIMPI(ctx, impi)
	if err != nil {
		return nil, err
	}

	return &state{impi: impi, regs: regs, now: r.clock.Now()}, nil
}

// hss is the HSS that serves the private identity, as the last one that answered for any of its registrations.
func (s *state) hss() db.HSS {
	for _, reg := range s.regs {
		if reg.HSS.Host != "" {
			return reg.HSS
		}
	}

	return db.HSS{}
}

func (s *state) set(key string) *db.Registration {
	for i := range s.regs {
		if holds(s.regs[i].Identities, key) {
			return &s.regs[i]
		}
	}

	return nil
}

func (s *state) registered(reg *db.Registration) bool {
	return reg != nil && len(s.live(reg.Bindings)) > 0
}

func (s *state) any() bool {
	return slices.ContainsFunc(s.regs, func(reg db.Registration) bool { return s.registered(&reg) })
}

func (s *state) registeredAny(ids []db.PublicIdentity) bool {
	return slices.ContainsFunc(s.regs, func(reg db.Registration) bool {
		return s.registered(&reg) && slices.ContainsFunc(ids, func(id db.PublicIdentity) bool { return holds(reg.Identities, id.Key) })
	})
}

func (s *state) contact(c contactRequest) (db.Contact, bool) {
	for _, reg := range s.regs {
		bindings := s.live(reg.Bindings)
		if i := bindingIndex(bindings, c); i >= 0 {
			return bindings[i].Contact, true
		}
	}

	return db.Contact{}, false
}

func (s *state) overlapping(reg db.Registration) []db.Registration {
	var out []db.Registration

	for _, other := range s.regs {
		if other.ID == reg.ID || s.registered(&other) {
			continue
		}

		for _, id := range other.Identities {
			if holds(reg.Identities, id.Key) {
				out = append(out, other)
				break
			}
		}
	}

	return out
}

// bound reports whether the contact has a live binding, in any registration set.
func (s *state) bound(c db.Contact) bool {
	return slices.ContainsFunc(s.regs, func(reg db.Registration) bool {
		return slices.ContainsFunc(s.live(reg.Bindings), func(b db.Binding) bool { return SameContact(b.Contact, c) })
	})
}

// SameContact reports whether two bindings bind the same contact: the same flow, or the same contact
// address (RFC 5626 §6, RFC 3261 §10.3).
func SameContact(a, b db.Contact) bool {
	if a.Flow() || b.Flow() {
		return a.Instance == b.Instance && a.RegID == b.RegID
	}

	ua, errA := sip.ParseURI(a.URI)
	ub, errB := sip.ParseURI(b.URI)

	return errA == nil && errB == nil && ua.Equivalent(ub)
}

func (s *state) live(bindings []db.Binding) []db.Binding {
	return liveAt(bindings, s.now)
}

func liveAt(bindings []db.Binding, now time.Time) []db.Binding {
	var out []db.Binding

	for _, b := range bindings {
		if b.ExpiresAt.After(now) {
			out = append(out, b)
		}
	}

	return out
}

func only(bindings []db.Binding, contacts []contactRequest) []db.Binding {
	var out []db.Binding

	for _, b := range bindings {
		if slices.ContainsFunc(contacts, func(c contactRequest) bool { return c.binds(b.Contact) }) {
			out = append(out, b)
		}
	}

	return out
}

func bindingIndex(bindings []db.Binding, c contactRequest) int {
	return slices.IndexFunc(bindings, func(b db.Binding) bool { return c.binds(b.Contact) })
}
