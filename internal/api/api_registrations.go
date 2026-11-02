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
	ErrUnavailable   = errors.New("unavailable")
)

type Registrations interface {
	ListRegistrations(ctx context.Context, search string, page, perPage int) ([]RegistrationStatus, int, error)
	Reauthenticate(ctx context.Context, impi string) error
}

type RegistrationStatus struct {
	IMPI                     string
	ImplicitRegistrationSets []ImplicitRegistrationSet
}

// TS 23.228 clause 5.2.1a.0
type ImplicitRegistrationSet struct {
	HSS        HSS
	Identities []RegisteredIdentity
	Contacts   []RegisteredContact
}

type HSS struct {
	Host  string
	Realm string
}

type RegisteredIdentity struct {
	URI            string
	DisplayName    string
	Barred         bool
	RegisteredWith []string
}

// TS 29.214 clause 4.4.5
// TS 29.514 clause 4.2.6.7
type SignallingPath string

const (
	SignallingPathUnmonitored SignallingPath = "unmonitored"
	SignallingPathMonitored   SignallingPath = "monitored"
	SignallingPathLost        SignallingPath = "lost"
)

type RegisteredContact struct {
	Contact        string
	Instance       string
	RegID          int64
	Q              float64
	Media          []string
	RegisteredAt   time.Time
	ExpiresAt      time.Time
	Address        string
	Transport      string
	Protected      bool
	SignallingPath SignallingPath
}

type RegistrationIdentityResponse struct {
	URI            string   `json:"uri"`
	DisplayName    string   `json:"display_name,omitempty"`
	Barred         bool     `json:"barred"`
	RegisteredWith []string `json:"registered_with"`
}

type RegisteredContactResponse struct {
	Contact        string   `json:"contact"`
	Instance       string   `json:"instance,omitempty"`
	RegID          int64    `json:"reg_id,omitempty"`
	Q              float64  `json:"q"`
	Media          []string `json:"media"`
	RegisteredAt   string   `json:"registered_at"`
	ExpiresAt      string   `json:"expires_at"`
	Address        string   `json:"address,omitempty"`
	Transport      string   `json:"transport,omitempty"`
	Protected      bool     `json:"protected"`
	SignallingPath string   `json:"signalling_path"`
}

type HSSResponse struct {
	Host  string `json:"host"`
	Realm string `json:"realm"`
}

type ImplicitRegistrationSetResponse struct {
	HSS        *HSSResponse                   `json:"hss,omitempty"`
	Identities []RegistrationIdentityResponse `json:"identities"`
	Contacts   []RegisteredContactResponse    `json:"contacts"`
}

type RegistrationResponse struct {
	IMPI                     string                            `json:"impi"`
	ImplicitRegistrationSets []ImplicitRegistrationSetResponse `json:"implicit_registration_sets"`
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
			writeError(r.Context(), w, http.StatusInternalServerError, "failed to list registrations", err, cfg.Logger)
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

		writeResponse(r.Context(), w, resp, http.StatusOK, cfg.Logger)
	})
}

func registrationResponse(reg RegistrationStatus) RegistrationResponse {
	out := RegistrationResponse{
		IMPI:                     reg.IMPI,
		ImplicitRegistrationSets: make([]ImplicitRegistrationSetResponse, 0, len(reg.ImplicitRegistrationSets)),
	}

	for _, set := range reg.ImplicitRegistrationSets {
		out.ImplicitRegistrationSets = append(out.ImplicitRegistrationSets, implicitRegistrationSetResponse(set))
	}

	return out
}

func implicitRegistrationSetResponse(set ImplicitRegistrationSet) ImplicitRegistrationSetResponse {
	out := ImplicitRegistrationSetResponse{
		Identities: make([]RegistrationIdentityResponse, 0, len(set.Identities)),
		Contacts:   make([]RegisteredContactResponse, 0, len(set.Contacts)),
	}

	if set.HSS.Host != "" {
		out.HSS = &HSSResponse{Host: set.HSS.Host, Realm: set.HSS.Realm}
	}

	for _, id := range set.Identities {
		with := id.RegisteredWith
		if with == nil {
			with = []string{}
		}

		out.Identities = append(out.Identities, RegistrationIdentityResponse{
			URI: id.URI, DisplayName: id.DisplayName, Barred: id.Barred, RegisteredWith: with,
		})
	}

	for _, d := range set.Contacts {
		media := d.Media
		if media == nil {
			media = []string{}
		}

		out.Contacts = append(out.Contacts, RegisteredContactResponse{
			Contact:        d.Contact,
			Instance:       d.Instance,
			RegID:          d.RegID,
			Q:              d.Q,
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
		writeError(r.Context(), w, http.StatusBadRequest, "page must be an integer >= 1", nil, cfg.Logger)
		return 0, 0, false
	}

	perPage, ok := atoiDefault(q.Get("per_page"), defaultPerPage)
	if !ok || perPage < 1 || perPage > maxPerPage {
		writeError(r.Context(), w, http.StatusBadRequest, "per_page must be an integer between 1 and 100", nil, cfg.Logger)
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
			writeError(r.Context(), w, http.StatusServiceUnavailable, "The IMS is restarting", err, cfg.Logger)
		case errors.Is(err, ErrNotRegistered):
			writeError(r.Context(), w, http.StatusNotFound, "no registration for "+impi, err, cfg.Logger)
		case err != nil:
			writeError(r.Context(), w, http.StatusInternalServerError, "re-authentication failed", err, cfg.Logger)
		default:
			writeResponse(r.Context(), w, Reauthentication{IMPI: impi}, http.StatusAccepted, cfg.Logger)
		}
	})
}
