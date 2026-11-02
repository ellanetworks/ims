package api

import (
	"context"
	"net/http"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

type MCPTTSettings interface {
	Get() mcpttsettings.Settings
	UpdateServiceConfiguration(ctx context.Context, c mcpttsettings.ServiceConfiguration) error
	CreateUser(ctx context.Context, u mcpttsettings.User) (mcpttsettings.User, error)
	UpdateUser(ctx context.Context, u mcpttsettings.User) (mcpttsettings.User, error)
	DeleteUser(ctx context.Context, id string) error
	CreateGroup(ctx context.Context, g mcpttsettings.Group, implicit mcpttsettings.Implicit) (mcpttsettings.Group, error)
	UpdateGroup(ctx context.Context, g mcpttsettings.Group, implicit mcpttsettings.Implicit) (mcpttsettings.Group, error)
	DeleteGroup(ctx context.Context, uri string) error
	PutMember(ctx context.Context, group string, e mcpttsettings.Entry, implicit *bool) (mcpttsettings.Group, bool, error)
	DeleteMember(ctx context.Context, group, id string) (mcpttsettings.Group, error)
}

type MCPTTStatus struct {
	Mode             string   `json:"mode"`
	Name             string   `json:"name"`
	ParticipatingPSI string   `json:"participating_psi"`
	ControllingPSI   string   `json:"controlling_psi"`
	SIP              []string `json:"sip"`
	MediaControl     string   `json:"media_control"`
}

func GetMCPTTStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(r.Context(), w, cfg.MCPTTStatus(), http.StatusOK, cfg.Logger)
	})
}

func mcpttOnly(cfg Config, h func(Config) http.Handler) http.Handler {
	if cfg.MCPTT == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(r.Context(), w, http.StatusNotFound, "MCPTT is not enabled", nil, cfg.Logger)
		})
	}

	return h(cfg)
}
