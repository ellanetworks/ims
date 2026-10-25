package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/prometheus/client_golang/prometheus"
)

type Config struct {
	Settings      Settings
	Diameter      Diameter
	SIP           SIP
	Registrations Registrations
	Policy        Policy
	CallRecords   CallRecords
	Frontend      fs.FS
	// Metrics are the metrics GET /api/v1/metrics serves.
	Metrics prometheus.Gatherer
	// MCPTT, MCPTTState and MCPTTStatus are of the built-in MCPTT AS, if the IMS runs it: /api/v1/mcptt answers 404
	// otherwise.
	MCPTT       MCPTTSettings
	MCPTTState  MCPTTState
	MCPTTStatus func() MCPTTStatus
	Logger      *slog.Logger
}

// Settings are the settings of the running IMS, which persists and applies a change before it returns. A change
// fails with an error of the kinds settings.ErrInvalid, ErrConflict or ErrNotFound, or another if it could not be
// saved.
type Settings interface {
	Get() settings.Settings
	UpdateOperator(ctx context.Context, o settings.Operator) error
	CreatePeer(ctx context.Context, p settings.Peer) (settings.Peer, error)
	UpdatePeer(ctx context.Context, p settings.Peer) error
	DeletePeer(ctx context.Context, id string) error
	UpdateRoute(ctx context.Context, r settings.Route) error
	UpdatePolicy(ctx context.Context, p settings.Policy) error
	UpdateCallRecords(ctx context.Context, c settings.CallRecords) error
}

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	for _, r := range routes(cfg) {
		mux.Handle(r.pattern, r.handler)
	}

	if cfg.Frontend != nil {
		mux.Handle("GET /", Frontend(cfg.Frontend))
	}

	return mux
}

type route struct {
	pattern string
	handler http.Handler
}

// routes are the API, which openapi.yaml describes.
func routes(cfg Config) []route {
	return []route{
		{"GET /api/v1/status", GetStatus(cfg)},
		{"GET /api/v1/openapi.yaml", OpenAPISpec()},
		{"GET /api/v1/metrics", GetMetrics(cfg)},
		{"GET /api/v1/operator", GetOperator(cfg)},
		{"PUT /api/v1/operator", UpdateOperator(cfg)},
		{"GET /api/v1/diameter", GetDiameterStatus(cfg)},
		{"GET /api/v1/diameter/peers", ListDiameterPeers(cfg)},
		{"POST /api/v1/diameter/peers", CreateDiameterPeer(cfg)},
		{"GET /api/v1/diameter/peers/{id}", GetDiameterPeer(cfg)},
		{"PUT /api/v1/diameter/peers/{id}", UpdateDiameterPeer(cfg)},
		{"DELETE /api/v1/diameter/peers/{id}", DeleteDiameterPeer(cfg)},
		{"GET /api/v1/diameter/routes", ListDiameterRoutes(cfg)},
		{"GET /api/v1/diameter/routes/{application}", GetDiameterRoute(cfg)},
		{"PUT /api/v1/diameter/routes/{application}", UpdateDiameterRoute(cfg)},
		{"GET /api/v1/policy", GetPolicy(cfg)},
		{"PUT /api/v1/policy", UpdatePolicy(cfg)},
		{"GET /api/v1/sip", GetSIPStatus(cfg)},
		{"GET /api/v1/registrations", ListRegistrations(cfg)},
		{"POST /api/v1/registrations/{impi}/reauthenticate", PostReauthentication(cfg)},
		{"GET /api/v1/mcptt/service-configuration", mcpttOnly(cfg, GetMCPTTServiceConfiguration)},
		{"PUT /api/v1/mcptt/service-configuration", mcpttOnly(cfg, UpdateMCPTTServiceConfiguration)},
		{"GET /api/v1/mcptt/users", mcpttOnly(cfg, ListMCPTTUsers)},
		{"POST /api/v1/mcptt/users", mcpttOnly(cfg, CreateMCPTTUser)},
		{"GET /api/v1/mcptt/users/{mcptt_id}", mcpttOnly(cfg, GetMCPTTUser)},
		{"PUT /api/v1/mcptt/users/{mcptt_id}", mcpttOnly(cfg, UpdateMCPTTUser)},
		{"DELETE /api/v1/mcptt/users/{mcptt_id}", mcpttOnly(cfg, DeleteMCPTTUser)},
		{"GET /api/v1/mcptt/groups", mcpttOnly(cfg, ListMCPTTGroups)},
		{"POST /api/v1/mcptt/groups", mcpttOnly(cfg, CreateMCPTTGroup)},
		{"GET /api/v1/mcptt/groups/{group_id}", mcpttOnly(cfg, GetMCPTTGroup)},
		{"PUT /api/v1/mcptt/groups/{group_id}", mcpttOnly(cfg, UpdateMCPTTGroup)},
		{"DELETE /api/v1/mcptt/groups/{group_id}", mcpttOnly(cfg, DeleteMCPTTGroup)},
		{"PUT /api/v1/mcptt/groups/{group_id}/members/{mcptt_id}", mcpttOnly(cfg, PutMCPTTMember)},
		{"DELETE /api/v1/mcptt/groups/{group_id}/members/{mcptt_id}", mcpttOnly(cfg, DeleteMCPTTMember)},
		{"GET /api/v1/mcptt/registrations", mcpttOnly(cfg, ListMCPTTRegistrations)},
		{"GET /api/v1/mcptt/affiliations", mcpttOnly(cfg, ListMCPTTAffiliations)},
		{"GET /api/v1/mcptt/calls", mcpttOnly(cfg, ListMCPTTCalls)},
		{"GET /api/v1/mcptt/calls/{call_id}", mcpttOnly(cfg, GetMCPTTCall)},
		{"GET /api/v1/call-records", ListCallRecords(cfg)},
		{"GET /api/v1/call-records/retention", GetCallRecordRetention(cfg)},
		{"PUT /api/v1/call-records/retention", UpdateCallRecordRetention(cfg)},
		{"GET /api/v1/call-records/{id}", GetCallRecord(cfg)},
	}
}
