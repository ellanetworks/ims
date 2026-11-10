package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/settings"
)

type Cluster interface {
	ID() string
	Clustered() bool
	Status(ctx context.Context) (cluster.Status, error)
	MintToken(ctx context.Context, ttl time.Duration) (cluster.Token, error)
	Join(ctx context.Context, token string) error
	RemoveMember(ctx context.Context, nodeID string, force bool) error
	SetDrainState(ctx context.Context, nodeID string, state db.DrainState) error
	SetDisplayName(ctx context.Context, nodeID, name string) error
	DisplayName(ctx context.Context) string
	DiameterLabel() string
	LabelGenerated() bool
	Formation() cluster.Formation
}

const (
	defaultTokenTTL = 30 * time.Minute
	maxJoinBody     = 16 << 10
)

type CreateJoinTokenParams struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

type JoinClusterParams struct {
	Token string `json:"token"`
}

type UpdateMemberParams struct {
	DisplayName string `json:"display_name"`
}

func noCluster(w http.ResponseWriter, r *http.Request, cfg Config) bool {
	if cfg.Cluster != nil {
		return false
	}

	writeError(r.Context(), w, http.StatusServiceUnavailable, "The cluster is not available", nil, cfg.Logger)

	return true
}

func notLeader(w http.ResponseWriter, r *http.Request, cfg Config, err error) bool {
	var le *cluster.LeaderError
	if !errors.As(err, &le) {
		return false
	}

	if le.NodeID == "" {
		writeError(r.Context(), w, http.StatusServiceUnavailable, "The cluster has no leader, retry later", err, cfg.Logger)
		return true
	}

	message := "This node is not the cluster leader: send the request to "
	if le.APIAddress != "" {
		message += le.APIAddress
	} else {
		message += "node " + le.NodeID
	}

	cfg.Logger.DebugContext(r.Context(), message, slog.Any("error", err))
	writeJSON(r.Context(), w, ErrorResponse{Error: message, Leader: le.APIAddress}, http.StatusMisdirectedRequest, cfg.Logger)

	return true
}

func clusterError(w http.ResponseWriter, r *http.Request, cfg Config, message string, err error) {
	if notLeader(w, r, cfg, err) {
		return
	}

	switch {
	case errors.Is(err, cluster.ErrNotClustered), errors.Is(err, cluster.ErrAlreadyMember):
		writeError(r.Context(), w, http.StatusConflict, err.Error(), err, cfg.Logger)
	case errors.Is(err, cluster.ErrInvalidJoin), errors.Is(err, db.ErrInvalid), errors.Is(err, db.ErrTokenExpired):
		writeError(r.Context(), w, http.StatusBadRequest, err.Error(), err, cfg.Logger)
	case errors.Is(err, db.ErrTokenUsed), errors.Is(err, settings.ErrConflict):
		writeError(r.Context(), w, http.StatusConflict, err.Error(), err, cfg.Logger)
	case errors.Is(err, db.ErrNotFound):
		writeError(r.Context(), w, http.StatusNotFound, "Member not found", err, cfg.Logger)
	case errors.Is(err, cluster.ErrJoinUnavailable):
		writeError(r.Context(), w, http.StatusBadGateway, err.Error(), err, cfg.Logger)
	case errors.Is(err, cluster.ErrNotDrained), errors.Is(err, cluster.ErrQuorum), errors.Is(err, cluster.ErrLastMember):
		writeError(r.Context(), w, http.StatusConflict, err.Error(), err, cfg.Logger)
	case errors.Is(err, cluster.ErrRemoved):
		writeError(r.Context(), w, http.StatusGone, err.Error(), err, cfg.Logger)
	default:
		writeError(r.Context(), w, http.StatusInternalServerError, message, err, cfg.Logger)
	}
}

func GetCluster(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		st, err := cfg.Cluster.Status(r.Context())
		if err != nil {
			clusterError(w, r, cfg, "Failed to get the cluster status", err)
			return
		}

		writeResponse(r.Context(), w, st, http.StatusOK, cfg.Logger)
	})
}

func CreateJoinToken(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		var params CreateJoinTokenParams
		if r.ContentLength != 0 && !decodeBody(w, r, &params, cfg) {
			return
		}

		ttl := defaultTokenTTL
		if params.TTLSeconds != 0 {
			ttl = time.Duration(params.TTLSeconds) * time.Second
		}

		token, err := cfg.Cluster.MintToken(r.Context(), ttl)
		if err != nil {
			clusterError(w, r, cfg, "Failed to create a join token", err)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "create_join_token", p.Email, "Cluster join token created")
		writeResponse(r.Context(), w, token, http.StatusCreated, cfg.Logger)
	})
}

func JoinCluster(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		if !a.allowLogin(w, r) {
			return
		}

		var params JoinClusterParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		if params.Token == "" {
			writeError(r.Context(), w, http.StatusBadRequest, "token is required", nil, cfg.Logger)
			return
		}

		users, err := cfg.Auth.CountUsers(r.Context())
		if err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to join the cluster", err, cfg.Logger)
			return
		}

		if users > 0 {
			writeError(r.Context(), w, http.StatusForbidden, "Only a node that is not initialized can join a cluster", nil,
				cfg.Logger)

			return
		}

		if err := cfg.Cluster.Join(r.Context(), params.Token); err != nil {
			clusterError(w, r, cfg, "Failed to join the cluster", err)
			return
		}

		a.audit(r, "join_cluster", "", "Node joined a cluster")
		writeResponse(r.Context(), w, SuccessResponse{Message: "Joined the cluster"}, http.StatusOK, cfg.Logger)
	})
}

func UpdateMember(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		var params UpdateMemberParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		id := r.PathValue("node_id")

		if err := cfg.Cluster.SetDisplayName(r.Context(), id, params.DisplayName); err != nil {
			clusterError(w, r, cfg, "Failed to update the member", err)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "update_member", p.Email, "Cluster member "+id+" display name set to "+strconv.Quote(params.DisplayName))
		writeResponse(r.Context(), w, SuccessResponse{Message: "Member updated successfully"}, http.StatusOK, cfg.Logger)
	})
}

func RemoveMember(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		id := r.PathValue("node_id")
		force := r.URL.Query().Get("force") == "true"

		if err := cfg.Cluster.RemoveMember(r.Context(), id, force); err != nil {
			clusterError(w, r, cfg, "Failed to remove the member", err)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "remove_member", p.Email, "Cluster member "+id+" removed")
		writeResponse(r.Context(), w, SuccessResponse{Message: "Member removed successfully"}, http.StatusOK, cfg.Logger)
	})
}

func DrainMember(a *auth, state db.DrainState) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		id := r.PathValue("node_id")

		if err := cfg.Cluster.SetDrainState(r.Context(), id, state); err != nil {
			clusterError(w, r, cfg, "Failed to change the drain state", err)
			return
		}

		action, message := "drain_member", "Member is draining"
		if state == db.DrainActive {
			action, message = "resume_member", "Member resumed"
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, action, p.Email, "Cluster member "+id+": "+string(state))
		writeResponse(r.Context(), w, SuccessResponse{Message: message}, http.StatusOK, cfg.Logger)
	})
}
