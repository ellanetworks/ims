package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/settings"
)

type fakeCluster struct {
	err error
}

func (fakeCluster) ID() string { return "node" }

func (fakeCluster) Clustered() bool { return true }

func (f fakeCluster) Status(context.Context) (cluster.Status, error) { return cluster.Status{}, f.err }

func (f fakeCluster) MintToken(context.Context, time.Duration) (cluster.Token, error) {
	return cluster.Token{}, f.err
}

func (f fakeCluster) Join(context.Context, string) error { return f.err }

func (f fakeCluster) RemoveMember(context.Context, string, bool) error { return f.err }

func (f fakeCluster) SetDrainState(context.Context, string, db.DrainState) error { return f.err }

func (f fakeCluster) SetDisplayName(context.Context, string, string) error { return f.err }

func (fakeCluster) DisplayName(context.Context) string { return "" }

func (fakeCluster) DiameterLabel() string { return "ims-a" }

func (fakeCluster) LabelGenerated() bool { return false }

func (fakeCluster) Formation() cluster.Formation {
	return cluster.Formation{State: cluster.FormationMember}
}

func clusterHandler(t *testing.T, err error) http.Handler {
	t.Helper()

	cfg := testConfig(t, openStore(t))
	cfg.Cluster = fakeCluster{err: err}

	return api.NewHandler(cfg)
}

func TestJoinClusterErrorCodes(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"token expired before redeeming": {fmt.Errorf("%w: %w", cluster.ErrInvalidJoin, pki.ErrInvalidToken), http.StatusBadRequest},
		"token expired at admission":     {fmt.Errorf("join token x: %w", db.ErrTokenExpired), http.StatusBadRequest},
		"token expired on the leader": {
			&cluster.RemoteError{Status: http.StatusConflict, Code: db.CodeOf(db.ErrTokenExpired)}, http.StatusBadRequest,
		},
		"token used":      {db.ErrTokenUsed, http.StatusConflict},
		"no member":       {cluster.ErrJoinUnavailable, http.StatusBadGateway},
		"already member":  {cluster.ErrAlreadyMember, http.StatusConflict},
		"invalid request": {cluster.ErrInvalidJoin, http.StatusBadRequest},
		"label taken": {
			&cluster.RemoteError{Status: http.StatusConflict, Code: db.CodeOf(settings.ErrConflict)}, http.StatusConflict,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := serve(clusterHandler(t, tt.err), request(http.MethodPost, "/api/v1/cluster/join", `{"token": "x"}`, ""))
			if rec.Code != tt.want {
				t.Fatalf("join = %d %s, want %d", rec.Code, rec.Body.String(), tt.want)
			}
		})
	}
}

func TestRemovedNodeAnswersGone(t *testing.T) {
	h := clusterHandler(t, cluster.ErrRemoved)
	token := initialize(t, h)

	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/cluster/tokens", `{}`},
		{http.MethodPut, "/api/v1/cluster/members/x", `{"display_name": "a"}`},
		{http.MethodPost, "/api/v1/cluster/members/x/drain", ``},
		{http.MethodPost, "/api/v1/cluster/members/x/resume", ``},
	} {
		if rec := serve(h, request(r.method, r.path, r.body, token)); rec.Code != http.StatusGone {
			t.Errorf("%s %s = %d %s, want %d", r.method, r.path, rec.Code, rec.Body.String(), http.StatusGone)
		}
	}
}

func TestLeaderOnlyActionsOnAFollower(t *testing.T) {
	tests := map[string]struct {
		err        error
		want       int
		wantLeader string
	}{
		"known leader":                  {&cluster.LeaderError{NodeID: "b", APIAddress: "https://10.0.0.2:5020"}, http.StatusMisdirectedRequest, "https://10.0.0.2:5020"},
		"leader without an API address": {&cluster.LeaderError{NodeID: "b"}, http.StatusMisdirectedRequest, ""},
		"no leader":                     {&cluster.LeaderError{}, http.StatusServiceUnavailable, ""},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := clusterHandler(t, tt.err)
			token := initialize(t, h)

			rec := serve(h, request(http.MethodDelete, "/api/v1/cluster/members/x?force=true", ``, token))
			if rec.Code != tt.want {
				t.Fatalf("DELETE member = %d %s, want %d", rec.Code, rec.Body.String(), tt.want)
			}

			var body api.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}

			if body.Leader != tt.wantLeader {
				t.Fatalf("leader = %q, want %q", body.Leader, tt.wantLeader)
			}
		})
	}
}
