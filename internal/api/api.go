package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/ellanetworks/ims/internal/settings"
)

type Config struct {
	Settings      Settings
	Diameter      Diameter
	SIP           SIP
	Registrations Registrations
	Policy        Policy
	CallRecords   CallRecords
	Frontend      fs.FS
	Logger        *slog.Logger
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
		{"GET /api/v1/operator", GetOperator(cfg)},
		{"PUT /api/v1/operator", UpdateOperator(cfg)},
		{"GET /api/v1/diameter", GetDiameterStatus(cfg)},
		{"GET /api/v1/diameter/peers", ListDiameterPeers(cfg)},
		{"POST /api/v1/diameter/peers", CreateDiameterPeer(cfg)},
		{"GET /api/v1/diameter/peers/{id}", GetDiameterPeer(cfg)},
		{"PUT /api/v1/diameter/peers/{id}", UpdateDiameterPeer(cfg)},
		{"DELETE /api/v1/diameter/peers/{id}", DeleteDiameterPeer(cfg)},
		{"GET /api/v1/policy", GetPolicy(cfg)},
		{"PUT /api/v1/policy", UpdatePolicy(cfg)},
		{"GET /api/v1/sip", GetSIPStatus(cfg)},
		{"GET /api/v1/registrations", ListRegistrations(cfg)},
		{"POST /api/v1/registrations/{impi}/reauthenticate", PostReauthentication(cfg)},
		{"GET /api/v1/call-records", ListCallRecords(cfg)},
		{"GET /api/v1/call-records/retention", GetCallRecordRetention(cfg)},
		{"PUT /api/v1/call-records/retention", UpdateCallRecordRetention(cfg)},
		{"GET /api/v1/call-records/{id}", GetCallRecord(cfg)},
	}
}
