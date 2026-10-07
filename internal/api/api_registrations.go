package api

import (
	"context"
	"errors"
	"net/http"
)

var ErrNotRegistered = errors.New("not registered")

type Registrations interface {
	Reauthenticate(ctx context.Context, impi string) error
}

type Reauthentication struct {
	IMPI string `json:"impi"`
}

func PostReauthentication(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		impi := r.PathValue("impi")

		err := cfg.Registrations.Reauthenticate(r.Context(), impi)

		switch {
		case errors.Is(err, ErrNotRegistered):
			writeError(w, http.StatusNotFound, "no registration for "+impi, err, cfg.Logger)
		case err != nil:
			writeError(w, http.StatusInternalServerError, "re-authentication failed", err, cfg.Logger)
		default:
			writeResponse(w, Reauthentication{IMPI: impi}, http.StatusAccepted, cfg.Logger)
		}
	})
}
