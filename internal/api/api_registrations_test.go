package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type fakeRegistrations struct {
	impi string
	err  error

	regs          []RegistrationStatus
	total         int
	search        string
	page, perPage int
}

func (f *fakeRegistrations) ListRegistrations(_ context.Context, search string, page, perPage int) ([]RegistrationStatus, int, error) {
	f.search, f.page, f.perPage = search, page, perPage
	return f.regs, f.total, f.err
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

func TestListRegistrations(t *testing.T) {
	const impi = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"

	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	regs := &fakeRegistrations{
		total: 30,
		regs: []RegistrationStatus{{
			IMPI: impi,
			Identities: []RegisteredIdentity{
				{URI: "tel:+15551230001", DisplayName: "Alice", RegisteredWith: []string{"001010000000005@ims.mnc001.mcc001.3gppnetwork.org"}},
				{URI: "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org", Barred: true},
			},
			Contacts: []RegisteredContact{{
				Contact:        "sip:001010000000001@[2001:db8::1]:5064",
				Instance:       "urn:gsma:imei:35000000-000001-0",
				Q:              0.5,
				Media:          []string{"audio", "video"},
				RegisteredAt:   at,
				ExpiresAt:      at.Add(time.Hour),
				Address:        "[2001:db8::1]:5064",
				Transport:      "udp",
				Protected:      true,
				SignallingPath: SignallingPathMonitored,
			}, {
				Contact:        "sip:001010000000001@192.0.2.1:5060",
				Q:              1,
				RegisteredAt:   at,
				ExpiresAt:      at.Add(time.Hour),
				SignallingPath: SignallingPathUnmonitored,
			}},
		}},
	}
	h := NewHandler(Config{Registrations: regs, Logger: slog.New(slog.DiscardHandler)})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/registrations?search=%2B1555&page=2&per_page=10", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	if regs.search != "+1555" || regs.page != 2 || regs.perPage != 10 {
		t.Fatalf("listed %q page %d of %d, want \"+1555\" page 2 of 10", regs.search, regs.page, regs.perPage)
	}

	var got struct {
		Result ListRegistrationsResponse `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	want := ListRegistrationsResponse{
		Page:       2,
		PerPage:    10,
		TotalCount: 30,
		Items: []RegistrationResponse{{
			IMPI: impi,
			Identities: []RegistrationIdentityResponse{
				{URI: "tel:+15551230001", DisplayName: "Alice", RegisteredWith: []string{"001010000000005@ims.mnc001.mcc001.3gppnetwork.org"}},
				{URI: "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org", Barred: true, RegisteredWith: []string{}},
			},
			Contacts: []RegisteredContactResponse{{
				Contact:        "sip:001010000000001@[2001:db8::1]:5064",
				Instance:       "urn:gsma:imei:35000000-000001-0",
				Q:              0.5,
				Media:          []string{"audio", "video"},
				RegisteredAt:   "2026-10-08T12:00:00.000Z",
				ExpiresAt:      "2026-10-08T13:00:00.000Z",
				Address:        "[2001:db8::1]:5064",
				Transport:      "udp",
				Protected:      true,
				SignallingPath: "monitored",
			}, {
				Contact:        "sip:001010000000001@192.0.2.1:5060",
				Q:              1,
				Media:          []string{},
				RegisteredAt:   "2026-10-08T12:00:00.000Z",
				ExpiresAt:      "2026-10-08T13:00:00.000Z",
				SignallingPath: "unmonitored",
			}},
		}},
	}
	if !reflect.DeepEqual(got.Result, want) {
		t.Fatalf("got %+v, want %+v", got.Result, want)
	}
}

func TestListRegistrationsDefaults(t *testing.T) {
	regs := &fakeRegistrations{}
	h := NewHandler(Config{Registrations: regs, Logger: slog.New(slog.DiscardHandler)})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/registrations", nil))

	if rec.Code != http.StatusOK || regs.search != "" || regs.page != 1 || regs.perPage != 25 {
		t.Fatalf("status %d, listed %q page %d of %d", rec.Code, regs.search, regs.page, regs.perPage)
	}

	if body := rec.Body.String(); body != `{"result":{"items":[],"page":1,"per_page":25,"total_count":0}}` {
		t.Fatalf("body = %s", body)
	}
}

func TestListRegistrationsErrors(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query string
		err   error
		want  int
	}{
		{"page 0", "?page=0", nil, http.StatusBadRequest},
		{"page not a number", "?page=one", nil, http.StatusBadRequest},
		{"per_page too large", "?per_page=101", nil, http.StatusBadRequest},
		{"failure", "", errors.New("database closed"), http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(Config{Registrations: &fakeRegistrations{err: tt.err}, Logger: slog.New(slog.DiscardHandler)})

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/registrations"+tt.query, nil))

			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
