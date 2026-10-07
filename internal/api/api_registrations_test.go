package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeRegistrations struct {
	impi string
	err  error
}

func (f *fakeRegistrations) Reauthenticate(_ context.Context, impi string) error {
	f.impi = impi
	return f.err
}

func TestPostReauthentication(t *testing.T) {
	const impi = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"

	for _, tt := range []struct {
		name string
		err  error
		want int
	}{
		{"accepted", nil, http.StatusAccepted},
		{"not registered", ErrNotRegistered, http.StatusNotFound},
		{"restarting", ErrUnavailable, http.StatusServiceUnavailable},
		{"failure", errors.New("database closed"), http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			regs := &fakeRegistrations{err: tt.err}
			h := NewHandler(Config{Registrations: regs, Logger: slog.New(slog.DiscardHandler)})

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost,
				"/api/v1/registrations/"+impi+"/reauthenticate", nil))

			if rec.Code != tt.want || regs.impi != impi {
				t.Fatalf("status %d for %q, want %d for %q", rec.Code, regs.impi, tt.want, impi)
			}
		})
	}
}
