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

func (s *state) contact(u sip.URI) (db.Contact, bool) {
	for _, reg := range s.regs {
		bindings := s.live(reg.Bindings)
		if i := bindingIndex(bindings, u); i >= 0 {
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
		if slices.ContainsFunc(contacts, func(c contactRequest) bool { return bindingIndex([]db.Binding{b}, c.addr.URI) == 0 }) {
			out = append(out, b)
		}
	}

	return out
}

func bindingIndex(bindings []db.Binding, u sip.URI) int {
	return slices.IndexFunc(bindings, func(b db.Binding) bool {
		stored, err := sip.ParseURI(b.Contact.URI)
		return err == nil && stored.Equivalent(u)
	})
}
