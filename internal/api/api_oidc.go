package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/oidc"
)

type OIDCSettings struct {
	Enabled                bool            `json:"enabled"`
	ExternalURL            string          `json:"external_url"`
	RedirectURL            string          `json:"redirect_url"`
	Issuer                 string          `json:"issuer"`
	ClientID               string          `json:"client_id"`
	ClientSecretSet        bool            `json:"client_secret_set"`
	DisplayName            string          `json:"display_name"`
	Scopes                 []string        `json:"scopes"`
	SessionLifetimeSeconds int64           `json:"session_lifetime_seconds"`
	TrustUnverifiedEmail   bool            `json:"trust_unverified_email"`
	CACertificate          string          `json:"ca_certificate"`
	GroupsClaim            string          `json:"groups_claim"`
	GroupRoles             []OIDCGroupRole `json:"group_roles"`
}

type OIDCGroupRole struct {
	Group  string  `json:"group"`
	RoleID db.Role `json:"role_id"`
}

type UpdateOIDCParams struct {
	Enabled                bool            `json:"enabled"`
	ExternalURL            string          `json:"external_url"`
	Issuer                 string          `json:"issuer"`
	ClientID               string          `json:"client_id"`
	ClientSecret           string          `json:"client_secret"`
	DisplayName            string          `json:"display_name"`
	Scopes                 []string        `json:"scopes"`
	SessionLifetimeSeconds int64           `json:"session_lifetime_seconds"`
	TrustUnverifiedEmail   bool            `json:"trust_unverified_email"`
	CACertificate          string          `json:"ca_certificate"`
	GroupsClaim            string          `json:"groups_claim"`
	GroupRoles             []OIDCGroupRole `json:"group_roles"`
}

type DiscoverOIDCParams struct {
	Issuer        string `json:"issuer"`
	CACertificate string `json:"ca_certificate"`
}

type OIDCDiscovery struct {
	Issuer                  string   `json:"issuer"`
	AuthorizationEndpoint   string   `json:"authorization_endpoint"`
	TokenEndpoint           string   `json:"token_endpoint"`
	UserInfoEndpoint        string   `json:"userinfo_endpoint"`
	JWKSURI                 string   `json:"jwks_uri"`
	SigningAlgorithms       []string `json:"signing_algorithms"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Keys                    int      `json:"keys"`
	PKCE                    bool     `json:"pkce"`
	IssuerParameter         bool     `json:"issuer_parameter"`
}

type OIDC interface {
	Get() db.OIDC
	Update(ctx context.Context, o db.OIDC) error
	Client(ctx context.Context, now func() time.Time) (*oidc.Client, error)
}

func GetOIDC(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(r.Context(), w, oidcResponse(cfg.OIDC.Get()), http.StatusOK, cfg.Logger)
	})
}

func UpdateOIDC(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params UpdateOIDCParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		lifetime := time.Duration(params.SessionLifetimeSeconds) * time.Second
		if params.SessionLifetimeSeconds < int64(oidc.MinSessionLifetime/time.Second) ||
			params.SessionLifetimeSeconds > int64(oidc.MaxSessionLifetime/time.Second) {
			writeError(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("session_lifetime_seconds must be between %d and %d",
				int64(oidc.MinSessionLifetime/time.Second), int64(oidc.MaxSessionLifetime/time.Second)), nil, cfg.Logger)

			return
		}

		current := cfg.OIDC.Get()

		o := db.OIDC{
			Enabled:              params.Enabled,
			ExternalURL:          params.ExternalURL,
			Issuer:               params.Issuer,
			ClientID:             params.ClientID,
			ClientSecret:         params.ClientSecret,
			DisplayName:          params.DisplayName,
			Scopes:               params.Scopes,
			SessionLifetime:      lifetime,
			TrustUnverifiedEmail: params.TrustUnverifiedEmail,
			CACertificate:        params.CACertificate,
			GroupsClaim:          params.GroupsClaim,
			GroupRoles:           []db.GroupRole{},
		}

		if o.ClientSecret == "" && current.ClientSecret != "" {
			if o.Issuer != current.Issuer || o.ClientID != current.ClientID {
				writeError(r.Context(), w, http.StatusBadRequest, "client_secret is required when the issuer or the client ID changes",
					nil, cfg.Logger)

				return
			}

			o.ClientSecret = current.ClientSecret
		}

		for _, g := range params.GroupRoles {
			o.GroupRoles = append(o.GroupRoles, db.GroupRole{Group: g.Group, Role: g.RoleID})
		}

		err := cfg.OIDC.Update(r.Context(), o)
		if errors.Is(err, oidc.ErrInvalid) {
			writeError(r.Context(), w, http.StatusBadRequest, err.Error(), nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to update the OIDC settings", err, cfg.Logger)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		updated := cfg.OIDC.Get()
		a.audit(r, "update_oidc", p.Email, oidcChanges(current, updated))
		writeResponse(r.Context(), w, oidcResponse(updated), http.StatusOK, cfg.Logger)
	})
}

func DiscoverOIDC(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params DiscoverOIDCParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		d, err := oidc.Discover(r.Context(), params.Issuer, params.CACertificate)
		if errors.Is(err, oidc.ErrInvalid) {
			writeError(r.Context(), w, http.StatusBadRequest, err.Error(), nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(r.Context(), w, http.StatusBadGateway, clip(err.Error()), nil, cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, OIDCDiscovery{
			Issuer:                  d.Issuer,
			AuthorizationEndpoint:   d.AuthorizationEndpoint,
			TokenEndpoint:           d.TokenEndpoint,
			UserInfoEndpoint:        d.UserInfoEndpoint,
			JWKSURI:                 d.JWKSURI,
			SigningAlgorithms:       d.SigningAlgorithms,
			TokenEndpointAuthMethod: d.TokenEndpointAuthMethod,
			Keys:                    d.Keys,
			PKCE:                    d.PKCE,
			IssuerParameter:         d.IssuerParameter,
		}, http.StatusOK, cfg.Logger)
	})
}

func oidcResponse(o db.OIDC) OIDCSettings {
	resp := OIDCSettings{
		Enabled:                o.Enabled,
		ExternalURL:            o.ExternalURL,
		Issuer:                 o.Issuer,
		ClientID:               o.ClientID,
		ClientSecretSet:        o.ClientSecret != "",
		DisplayName:            o.DisplayName,
		Scopes:                 o.Scopes,
		SessionLifetimeSeconds: int64(o.SessionLifetime / time.Second),
		TrustUnverifiedEmail:   o.TrustUnverifiedEmail,
		CACertificate:          o.CACertificate,
		GroupsClaim:            o.GroupsClaim,
		GroupRoles:             []OIDCGroupRole{},
	}

	if o.ExternalURL != "" {
		resp.RedirectURL = oidc.RedirectURL(o)
	}

	for _, g := range o.GroupRoles {
		resp.GroupRoles = append(resp.GroupRoles, OIDCGroupRole{Group: g.Group, RoleID: g.Role})
	}

	return resp
}

func oidcChanges(before, after db.OIDC) string {
	var changed []string

	for name, differs := range map[string]bool{
		"enabled":                before.Enabled != after.Enabled,
		"external_url":           before.ExternalURL != after.ExternalURL,
		"issuer":                 before.Issuer != after.Issuer,
		"client_id":              before.ClientID != after.ClientID,
		"client_secret":          before.ClientSecret != after.ClientSecret,
		"display_name":           before.DisplayName != after.DisplayName,
		"scopes":                 strings.Join(before.Scopes, " ") != strings.Join(after.Scopes, " "),
		"session_lifetime":       before.SessionLifetime != after.SessionLifetime,
		"trust_unverified_email": before.TrustUnverifiedEmail != after.TrustUnverifiedEmail,
		"ca_certificate":         before.CACertificate != after.CACertificate,
		"groups_claim":           before.GroupsClaim != after.GroupsClaim,
		"group_roles":            fmt.Sprint(before.GroupRoles) != fmt.Sprint(after.GroupRoles),
	} {
		if differs {
			changed = append(changed, name)
		}
	}

	if len(changed) == 0 {
		return fmt.Sprintf("OIDC settings saved unchanged, enabled %t", after.Enabled)
	}

	slices.Sort(changed)

	return fmt.Sprintf("OIDC settings changed (%s), enabled %t", strings.Join(changed, ", "), after.Enabled)
}
