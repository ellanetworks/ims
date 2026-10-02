package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

var ErrNotRegistered = errors.New("not registered")

type Registrations interface {
	Reauthenticate(ctx context.Context, impi string) error
}

type Reauthentication struct {
	IMPI string `json:"impi"`
}

type Error struct {
	Error string `json:"error"`
}

func PostReauthentication(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		impi := r.PathValue("impi")

		err := cfg.Registrations.Reauthenticate(r.Context(), impi)

		switch {
		case errors.Is(err, ErrNotRegistered):
			writeJSON(w, Error{Error: "no registration for " + impi}, http.StatusNotFound, cfg.Logger)
		case err != nil:
			cfg.Logger.Warn("network-initiated re-authentication failed", slog.String("impi", impi), slog.Any("error", err))
			writeJSON(w, Error{Error: "re-authentication failed"}, http.StatusInternalServerError, cfg.Logger)
		default:
			writeResponse(w, Reauthentication{IMPI: impi}, http.StatusAccepted, cfg.Logger)
		}
	})
}
