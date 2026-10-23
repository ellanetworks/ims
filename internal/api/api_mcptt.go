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
	UpdateSystem(ctx context.Context, s mcpttsettings.System) error
	UpdateServiceConfiguration(ctx context.Context, c mcpttsettings.ServiceConfiguration) error
	CreateUser(ctx context.Context, u mcpttsettings.User) (mcpttsettings.User, error)
	UpdateUser(ctx context.Context, u mcpttsettings.User) (mcpttsettings.User, error)
	DeleteUser(ctx context.Context, id string) error
	CreateGroup(ctx context.Context, g mcpttsettings.Group) (mcpttsettings.Group, error)
	UpdateGroup(ctx context.Context, g mcpttsettings.Group) (mcpttsettings.Group, error)
	DeleteGroup(ctx context.Context, uri string) error
	PutMember(ctx context.Context, group string, e mcpttsettings.Entry) (mcpttsettings.Group, bool, error)
	DeleteMember(ctx context.Context, group, id string) (mcpttsettings.Group, error)
}

// MCPTTStatus is what the MCPTT AS runs: its mode, the addresses its SIP listens on, UDP and TCP, and that of its
// floor control channels.
type MCPTTStatus struct {
	Mode         string   `json:"mode"`
	SIP          []string `json:"sip"`
	MediaControl string   `json:"media_control"`
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
