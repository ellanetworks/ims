package scscf

import (
	"context"
	"errors"
	"log/slog"
)

var ErrNotRegistered = errors.New("scscf: not registered")

func (r *Registrar) Reauthenticate(ctx context.Context, impi string) error {
	ctx, done, ok := r.begin(ctx)
	if !ok {
		return errors.New("registrar closed")
	}

	defer done()

	out, err := r.reauthenticate(ctx, impi)

	r.send(ctx, out)

	return err
}

func (r *Registrar) reauthenticate(ctx context.Context, impi string) ([]*outgoing, error) {
	if err := r.lock(ctx, impi); err != nil {
		return nil, err
	}

	defer r.unlock(impi)

	st, err := r.load(ctx, impi)
	if err != nil {
		return nil, err
	}

	if !st.any() {
		return nil, ErrNotRegistered
	}

	if err := r.shorten(ctx, st); err != nil {
		return nil, err
	}

	return r.notifyChange(ctx, impi, change{shortened: impi}), nil
}

// TS 24.229 clause 5.4.1.6
func (r *Registrar) shorten(ctx context.Context, st *state) error {
	at := st.now.Add(r.cfg.ReauthExpires)

	for i, reg := range st.regs {
		changed := false

		for j, b := range reg.Bindings {
			if b.ExpiresAt.After(at) {
				reg.Bindings[j].ExpiresAt = at
				changed = true
			}
		}

		if !changed {
			continue
		}

		saved, err := r.cfg.DB.SaveRegistration(ctx, reg)
		if err != nil {
			return err
		}

		st.regs[i] = saved
	}

	r.mu.Lock()
	r.reauth[st.impi]++
	r.mu.Unlock()

	r.log.InfoContext(ctx, "network-initiated re-authentication", slog.String("impi", st.impi),
		slog.Duration("expires", r.cfg.ReauthExpires))

	return nil
}
