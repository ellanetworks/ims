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

	r.send(out)

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

	at := st.now.Add(r.cfg.ReauthExpires)

	for _, reg := range st.regs {
		changed := false

		for i, b := range reg.Bindings {
			if b.ExpiresAt.After(at) {
				reg.Bindings[i].ExpiresAt = at
				changed = true
			}
		}

		if !changed {
			continue
		}

		if _, err := r.cfg.DB.SaveRegistration(ctx, reg); err != nil {
			return nil, err
		}
	}

	r.mu.Lock()
	r.reauth[impi]++
	r.mu.Unlock()

	r.log.Info("network-initiated re-authentication", slog.String("impi", impi),
		slog.Duration("expires", r.cfg.ReauthExpires))

	return r.notifyChange(ctx, impi, change{shortened: impi}), nil
}
