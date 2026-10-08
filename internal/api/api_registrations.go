package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultPerPage = 25
	maxPerPage     = 100
)

var (
	ErrNotRegistered = errors.New("not registered")
	// ErrUnavailable is for while the IMS restarts to apply its settings.
	ErrUnavailable = errors.New("unavailable")
)

type Registrations interface {
	// ListRegistrations returns a page of the private identities registered with the IMS, in order, and their count.
	// A search matches a part of the private identity or of one of its public identities.
	ListRegistrations(ctx context.Context, search string, page, perPage int) ([]RegistrationStatus, int, error)
	Reauthenticate(ctx context.Context, impi string) error
}

// RegistrationStatus is a private identity registered with the IMS: its public identities, and the devices
// registered with it (TS 24.229 §5.4.1).
type RegistrationStatus struct {
	IMPI       string
	Identities []RegisteredIdentity
	Devices    []RegisteredDevice
}

type RegisteredIdentity struct {
	URI         string
	DisplayName string
	Barred      bool
}

// SignallingPath is the state of a device's IMS signalling path, which the P-CSCF learns from the policy function
// (TS 29.214 §4.4.5, TS 29.514 §4.2.6.7).
type SignallingPath string

const (
	SignallingPathUnmonitored SignallingPath = "unmonitored"
	SignallingPathMonitored   SignallingPath = "monitored"
	SignallingPathLost        SignallingPath = "lost"
)

// RegisteredDevice is a contact bound to the private identity: the device's instance ID (TS 23.003 §13.8), the
// media it registered for (RFC 3840), and its flow to the P-CSCF, if the P-CSCF knows it.
type RegisteredDevice struct {
	Contact        string
	Instance       string
	Media          []string
	RegisteredAt   time.Time
	ExpiresAt      time.Time
	Address        string
	Transport      string
	Protected      bool
	SignallingPath SignallingPath
}

type RegistrationIdentityResponse struct {
	URI         string `json:"uri"`
	DisplayName string `json:"display_name,omitempty"`
	Barred      bool   `json:"barred"`
}

type RegisteredDeviceResponse struct {
	Contact        string   `json:"contact"`
	Instance       string   `json:"instance,omitempty"`
	Media          []string `json:"media"`
	RegisteredAt   string   `json:"registered_at"`
	ExpiresAt      string   `json:"expires_at"`
	Address        string   `json:"address,omitempty"`
	Transport      string   `json:"transport,omitempty"`
	Protected      bool     `json:"protected"`
	SignallingPath string   `json:"signalling_path"`
}

type RegistrationResponse struct {
	IMPI       string                         `json:"impi"`
	Identities []RegistrationIdentityResponse `json:"identities"`
	Devices    []RegisteredDeviceResponse     `json:"devices"`
}

type ListRegistrationsResponse struct {
	Items      []RegistrationResponse `json:"items"`
	Page       int                    `json:"page"`
	PerPage    int                    `json:"per_page"`
	TotalCount int                    `json:"total_count"`
}

func ListRegistrations(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		regs, total, err := cfg.Registrations.ListRegistrations(r.Context(), r.URL.Query().Get("search"), page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list registrations", err, cfg.Logger)
			return
		}

		resp := ListRegistrationsResponse{
			Items:      make([]RegistrationResponse, 0, len(regs)),
			Page:       page,
			PerPage:    perPage,
			TotalCount: total,
		}

		for _, reg := range regs {
			resp.Items = append(resp.Items, registrationResponse(reg))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func registrationResponse(reg RegistrationStatus) RegistrationResponse {
	out := RegistrationResponse{
		IMPI:       reg.IMPI,
		Identities: make([]RegistrationIdentityResponse, 0, len(reg.Identities)),
		Devices:    make([]RegisteredDeviceResponse, 0, len(reg.Devices)),
	}

	for _, id := range reg.Identities {
		out.Identities = append(out.Identities, RegistrationIdentityResponse(id))
	}

	for _, d := range reg.Devices {
		media := d.Media
		if media == nil {
			media = []string{}
		}

		out.Devices = append(out.Devices, RegisteredDeviceResponse{
			Contact:        d.Contact,
			Instance:       d.Instance,
			Media:          media,
			RegisteredAt:   formatTime(d.RegisteredAt),
			ExpiresAt:      formatTime(d.ExpiresAt),
			Address:        d.Address,
			Transport:      d.Transport,
			Protected:      d.Protected,
			SignallingPath: string(d.SignallingPath),
		})
	}

	return out
}

func pagination(w http.ResponseWriter, r *http.Request, cfg Config) (int, int, bool) {
	q := r.URL.Query()

	page, ok := atoiDefault(q.Get("page"), 1)
	if !ok || page < 1 {
		writeError(w, http.StatusBadRequest, "page must be an integer >= 1", nil, cfg.Logger)
		return 0, 0, false
	}

	perPage, ok := atoiDefault(q.Get("per_page"), defaultPerPage)
	if !ok || perPage < 1 || perPage > maxPerPage {
		writeError(w, http.StatusBadRequest, "per_page must be an integer between 1 and 100", nil, cfg.Logger)
		return 0, 0, false
	}

	return page, perPage, true
}

func atoiDefault(s string, def int) (int, bool) {
	if s == "" {
		return def, true
	}

	n, err := strconv.Atoi(s)

	return n, err == nil
}

type Reauthentication struct {
	IMPI string `json:"impi"`
}

func PostReauthentication(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		impi := r.PathValue("impi")

		err := cfg.Registrations.Reauthenticate(r.Context(), impi)

		switch {
		case errors.Is(err, ErrUnavailable):
			writeError(w, http.StatusServiceUnavailable, "The IMS is restarting", err, cfg.Logger)
		case errors.Is(err, ErrNotRegistered):
			writeError(w, http.StatusNotFound, "no registration for "+impi, err, cfg.Logger)
		case err != nil:
			writeError(w, http.StatusInternalServerError, "re-authentication failed", err, cfg.Logger)
		default:
			writeResponse(w, Reauthentication{IMPI: impi}, http.StatusAccepted, cfg.Logger)
		}
	})
}
