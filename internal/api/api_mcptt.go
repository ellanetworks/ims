package api

import (
	"context"
	"net/http"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// MCPTTSettings are the settings of the running MCPTT AS, which persists and applies a change before it returns, with
// what it stored. A change fails with a ChangeError of the MCPTT settings package, or another error if it could not
// be saved.
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

// MCPTTStatus is what the MCPTT AS runs: its mode; its name and PSIs, of the home domain, which the HSS gives the
// IMS (the server name of its iFCs, the PSIs it routes to it); the addresses of its SIP, UDP and TCP, in memory
// through the IMS; and that of its floor control channels.
type MCPTTStatus struct {
	Mode             string   `json:"mode"`
	Name             string   `json:"name"`
	ParticipatingPSI string   `json:"participating_psi"`
	ControllingPSI   string   `json:"controlling_psi"`
	SIP              []string `json:"sip"`
	MediaControl     string   `json:"media_control"`
}

// GetMCPTTStatus serves what the MCPTT AS runs.
func GetMCPTTStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, cfg.MCPTTStatus(), http.StatusOK, cfg.Logger)
	})
}

// mcpttOnly is h when the IMS runs the MCPTT AS, and a 404 otherwise.
func mcpttOnly(cfg Config, h func(Config) http.Handler) http.Handler {
	if cfg.MCPTT == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "MCPTT is not enabled", nil, cfg.Logger)
		})
	}

	return h(cfg)
}
