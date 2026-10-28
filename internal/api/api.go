package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

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
	// Ready reports why the IMS cannot serve requests, or nil when it can. GET /api/v1/readyz answers 503 when it
	// fails. Nil is always ready.
	Ready func(ctx context.Context) error
	// Metrics are the metrics GET /api/v1/metrics serves.
	Metrics prometheus.Gatherer
	// MCPTT, MCPTTState and MCPTTStatus are of the built-in MCPTT AS, if the IMS runs it: /api/v1/mcptt answers 404
	// otherwise.
	MCPTT       MCPTTSettings
	MCPTTState  MCPTTState
	MCPTTStatus func() MCPTTStatus
	// Auth stores the users, their sessions and their API tokens.
	Auth AuthStore
	// JWTSecret signs the access tokens.
	JWTSecret []byte
	// SecureCookie marks the session cookie Secure, and turns on HSTS, for an API served over TLS.
	SecureCookie bool
	// BcryptCost is the cost of the password hashes: bcrypt.DefaultCost when zero.
	BcryptCost int
	Now        func() time.Time
	Logger     *slog.Logger
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
	UpdateIPSMGW(ctx context.Context, g settings.IPSMGW) error
}

func NewHandler(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := newAuth(cfg)
	mux := http.NewServeMux()

	for _, rt := range routes(cfg, a) {
		h := rt.handler
		if rt.perm != public {
			h = a.authenticate(a.authorize(rt.perm, h))
		}

		mux.Handle(rt.pattern, h)
	}

	// The UI is static files: all its data comes from the API, which authenticates it.
	if cfg.Frontend != nil {
		mux.Handle("GET /", Frontend(cfg.Frontend))
	}

	// The session cookie authenticates the refresh and the logout, so a page of another origin must not be able to
	// send them. CrossOriginProtection rejects the unsafe requests that a browser tells are cross-origin, and lets
	// through those of other clients, which send none of the headers it checks.
	return securityHeaders(cfg, http.NewCrossOriginProtection().Handler(mux))
}

func securityHeaders(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		if cfg.SecureCookie {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		next.ServeHTTP(w, r)
	})
}

// route is a route of the API, and the permission that it needs: public for none.
type route struct {
	pattern string
	perm    Permission
	handler http.Handler
}

// routes are the API, which openapi.yaml describes.
func routes(cfg Config, a *auth) []route {
	return []route{
		{"GET /api/v1/livez", public, Livez()},
		{"GET /api/v1/readyz", public, Readyz(cfg)},
		{"GET /api/v1/status", public, GetStatus(cfg)},
		{"GET /api/v1/openapi.yaml", public, OpenAPISpec()},
		{"GET /api/v1/metrics", public, GetMetrics(cfg)},
		{"POST /api/v1/init", public, Initialize(a)},
		{"POST /api/v1/auth/login", public, Login(a)},
		{"POST /api/v1/auth/refresh", public, Refresh(a)},
		{"POST /api/v1/auth/logout", public, Logout(a)},

		{"GET /api/v1/operator", PermReadOperator, GetOperator(cfg)},
		{"PUT /api/v1/operator", PermUpdateOperator, UpdateOperator(cfg)},
		{"GET /api/v1/diameter", PermReadDiameter, GetDiameterStatus(cfg)},
		{"GET /api/v1/diameter/peers", PermReadDiameter, ListDiameterPeers(cfg)},
		{"POST /api/v1/diameter/peers", PermUpdateDiameter, CreateDiameterPeer(cfg)},
		{"GET /api/v1/diameter/peers/{id}", PermReadDiameter, GetDiameterPeer(cfg)},
		{"PUT /api/v1/diameter/peers/{id}", PermUpdateDiameter, UpdateDiameterPeer(cfg)},
		{"DELETE /api/v1/diameter/peers/{id}", PermUpdateDiameter, DeleteDiameterPeer(cfg)},
		{"GET /api/v1/diameter/routes", PermReadDiameter, ListDiameterRoutes(cfg)},
		{"GET /api/v1/diameter/routes/{application}", PermReadDiameter, GetDiameterRoute(cfg)},
		{"PUT /api/v1/diameter/routes/{application}", PermUpdateDiameter, UpdateDiameterRoute(cfg)},
		{"GET /api/v1/policy", PermReadPolicy, GetPolicy(cfg)},
		{"PUT /api/v1/policy", PermUpdatePolicy, UpdatePolicy(cfg)},
		{"GET /api/v1/sip", PermReadSIP, GetSIPStatus(cfg)},
		{"GET /api/v1/registrations", PermReadRegistrations, ListRegistrations(cfg)},
		{"POST /api/v1/registrations/{impi}/reauthenticate", PermReauthenticate, PostReauthentication(cfg)},
		{"GET /api/v1/mcptt", PermReadMCPTT, mcpttOnly(cfg, GetMCPTTStatus)},
		{"GET /api/v1/mcptt/service-configuration", PermReadMCPTT, mcpttOnly(cfg, GetMCPTTServiceConfiguration)},
		{"PUT /api/v1/mcptt/service-configuration", PermUpdateMCPTT, mcpttOnly(cfg, UpdateMCPTTServiceConfiguration)},
		{"GET /api/v1/mcptt/users", PermReadMCPTT, mcpttOnly(cfg, ListMCPTTUsers)},
		{"POST /api/v1/mcptt/users", PermUpdateMCPTT, mcpttOnly(cfg, CreateMCPTTUser)},
		{"GET /api/v1/mcptt/users/{mcptt_id}", PermReadMCPTT, mcpttOnly(cfg, GetMCPTTUser)},
		{"PUT /api/v1/mcptt/users/{mcptt_id}", PermUpdateMCPTT, mcpttOnly(cfg, UpdateMCPTTUser)},
		{"DELETE /api/v1/mcptt/users/{mcptt_id}", PermUpdateMCPTT, mcpttOnly(cfg, DeleteMCPTTUser)},
		{"GET /api/v1/mcptt/groups", PermReadMCPTT, mcpttOnly(cfg, ListMCPTTGroups)},
		{"POST /api/v1/mcptt/groups", PermUpdateMCPTT, mcpttOnly(cfg, CreateMCPTTGroup)},
		{"GET /api/v1/mcptt/groups/{group_id}", PermReadMCPTT, mcpttOnly(cfg, GetMCPTTGroup)},
		{"PUT /api/v1/mcptt/groups/{group_id}", PermUpdateMCPTT, mcpttOnly(cfg, UpdateMCPTTGroup)},
		{"DELETE /api/v1/mcptt/groups/{group_id}", PermUpdateMCPTT, mcpttOnly(cfg, DeleteMCPTTGroup)},
		{"PUT /api/v1/mcptt/groups/{group_id}/members/{mcptt_id}", PermUpdateMCPTT, mcpttOnly(cfg, PutMCPTTMember)},
		{"DELETE /api/v1/mcptt/groups/{group_id}/members/{mcptt_id}", PermUpdateMCPTT, mcpttOnly(cfg, DeleteMCPTTMember)},
		{"GET /api/v1/mcptt/registrations", PermReadMCPTT, mcpttOnly(cfg, ListMCPTTRegistrations)},
		{"GET /api/v1/mcptt/affiliations", PermReadMCPTT, mcpttOnly(cfg, ListMCPTTAffiliations)},
		{"GET /api/v1/mcptt/calls", PermReadMCPTT, mcpttOnly(cfg, ListMCPTTCalls)},
		{"GET /api/v1/mcptt/calls/{call_id}", PermReadMCPTT, mcpttOnly(cfg, GetMCPTTCall)},
		{"GET /api/v1/call-records", PermReadCallRecords, ListCallRecords(cfg)},
		{"GET /api/v1/call-records/retention", PermReadCallRecordRetention, GetCallRecordRetention(cfg)},
		{"PUT /api/v1/call-records/retention", PermUpdateCallRecordRetention, UpdateCallRecordRetention(cfg)},
		{"GET /api/v1/call-records/{id}", PermReadCallRecords, GetCallRecord(cfg)},
		{"GET /api/v1/ip-sm-gw", PermReadIPSMGW, GetIPSMGW(cfg)},
		{"PUT /api/v1/ip-sm-gw", PermUpdateIPSMGW, UpdateIPSMGW(cfg)},

		// The routes that create credentials or manage users need a login rather than an API token, so that a leaked
		// API token cannot make itself others that outlive its revocation, or take an account.
		{"GET /api/v1/users/me", PermSelf, GetMyUser(a)},
		{"PUT /api/v1/users/me/password", PermSelf, a.needsLogin(UpdateMyPassword(a))},
		{"GET /api/v1/users/me/api-tokens", PermSelf, ListAPITokens(a, myself(cfg))},
		{"POST /api/v1/users/me/api-tokens", PermSelf, a.needsLogin(CreateAPIToken(a, myself(cfg)))},
		{"DELETE /api/v1/users/me/api-tokens/{id}", PermSelf, DeleteAPIToken(a, myself(cfg))},

		{"GET /api/v1/users", PermManageUsers, a.needsLogin(ListUsers(a))},
		{"POST /api/v1/users", PermManageUsers, a.needsLogin(CreateUser(a))},
		{"GET /api/v1/users/{email}", PermManageUsers, a.needsLogin(GetUser(a))},
		{"PUT /api/v1/users/{email}", PermManageUsers, a.needsLogin(UpdateUser(a))},
		{"DELETE /api/v1/users/{email}", PermManageUsers, a.needsLogin(DeleteUser(a))},
		{"PUT /api/v1/users/{email}/password", PermManageUsers, a.needsLogin(UpdateUserPassword(a))},
		{"GET /api/v1/users/{email}/api-tokens", PermManageUsers, a.needsLogin(ListAPITokens(a, userOfPath(cfg)))},
		{"POST /api/v1/users/{email}/api-tokens", PermManageUsers, a.needsLogin(CreateAPIToken(a, userOfPath(cfg)))},
		{"DELETE /api/v1/users/{email}/api-tokens/{id}", PermManageUsers, a.needsLogin(DeleteAPIToken(a, userOfPath(cfg)))},
	}
}
