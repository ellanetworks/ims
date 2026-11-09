package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/listener"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const (
	maxJoinBody     = 64 << 10
	rpcReadTimeout  = time.Minute
	rpcWriteTimeout = 5 * time.Minute
)

const (
	codeInvalidJoin   = "invalid_join"
	codeInvalidBackup = "invalid_backup"
	codeUnknown       = "outcome_unknown"
	codeNotDrained    = "not_drained"
	codeQuorum        = "quorum"
	codeRemoved       = "removed"
	codeInvalidProof  = "invalid_proof"
)

type (
	fencedKey struct{}
)

func fenced(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if removed, _ := r.Context().Value(fencedKey{}).(bool); removed {
			writeJSON(w, http.StatusGone, rpcError{Error: ErrRemoved.Error(), Code: codeRemoved})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (n *Node) serveRPC() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cluster/v1/propose", n.leaderOnly(n.handlePropose))
	mux.HandleFunc("POST /cluster/v1/join", n.leaderOnly(n.handleJoin))
	mux.HandleFunc("POST /cluster/v1/probe", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct{}{})
	})

	errorLog := slog.NewLogLogger(n.cfg.Logger.Handler(), slog.LevelDebug)

	n.rpc = &http.Server{
		Handler:           fenced(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       rpcReadTimeout,
		WriteTimeout:      rpcWriteTimeout,
		ErrorLog:          errorLog,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, fencedKey{}, listener.Fenced(c))
		},
	}

	go func() { _ = n.rpc.Serve(n.ln.Stream(listener.ALPNHTTP, n.cfg.Advertise)) }()

	join := http.NewServeMux()
	join.HandleFunc("POST /cluster/v1/join", n.handleBootstrapJoin)
	join.HandleFunc("POST /cluster/v1/hello", n.withSecret(n.handleHello))

	joinServer := &http.Server{
		Handler:           join,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      time.Minute,
		ErrorLog:          errorLog,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
	}

	go func() { _ = joinServer.Serve(n.ln.Stream(listener.ALPNJoin, n.cfg.Advertise)) }()

	n.background.Go(func() {
		<-n.done

		_ = joinServer.Close()
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPCError(w http.ResponseWriter, err error) {
	status, e := rpcErrorOf(err)
	writeJSON(w, status, e)
}

func rpcErrorOf(err error) (int, rpcError) {
	var remote *RemoteError

	switch {
	case errors.Is(err, errUnknown):
		return http.StatusServiceUnavailable, rpcError{Error: err.Error(), Code: codeUnknown}
	case errors.Is(err, errRetry), errors.Is(err, raft.ErrRaftShutdown):
		return http.StatusServiceUnavailable, rpcError{Error: err.Error()}
	case errors.As(err, &remote):
		return remote.Status, rpcError{Error: remote.Message, Code: remote.Code}
	case errors.Is(err, ErrInvalidJoin):
		return http.StatusBadRequest, rpcError{Error: err.Error(), Code: codeInvalidJoin}
	case errors.Is(err, ErrNotDrained):
		return http.StatusConflict, rpcError{Error: err.Error(), Code: codeNotDrained}
	case errors.Is(err, ErrQuorum):
		return http.StatusConflict, rpcError{Error: err.Error(), Code: codeQuorum}
	case errors.Is(err, ErrInvalidBackup):
		return http.StatusBadRequest, rpcError{Error: err.Error(), Code: codeInvalidBackup}
	}

	code := db.CodeOf(err)

	status := http.StatusInternalServerError
	if code != "" {
		status = http.StatusConflict
	}

	return status, rpcError{Error: err.Error(), Code: code}
}

func (n *Node) leaderOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if leading, _ := n.Leader(); leading {
			h(w, r)
			return
		}

		raftNode, _ := n.current()

		if raftNode.State() == raft.Leader {
			writeJSON(w, http.StatusServiceUnavailable, rpcError{Error: "the leader term is starting"})
			return
		}

		writeJSON(w, http.StatusMisdirectedRequest, rpcError{Error: "not the leader"})
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcError{Error: err.Error(), Code: "invalid"})
		return false
	}

	return true
}

func (n *Node) handlePropose(w http.ResponseWriter, r *http.Request) {
	var req proposeRequest
	if !decodeBody(w, r, maxRPCBody, &req) {
		return
	}

	_, b := n.current()
	if b == nil {
		writeJSON(w, http.StatusServiceUnavailable, rpcError{Error: "raft is restarting"})
		return
	}

	results, index, err := b.propose(r.Context(), req.Commands)

	switch {
	case errors.Is(err, db.ErrBusy):
		writeJSON(w, http.StatusOK, proposeReply{Busy: true})
	case err != nil:
		writeRPCError(w, classify(err))
	default:
		writeJSON(w, http.StatusOK, proposeReply{Results: results, Index: index})
	}
}

func (n *Node) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if !decodeBody(w, r, maxJoinBody, &req) {
		return
	}

	reply, err := n.admit(r.Context(), req)
	if err != nil {
		writeRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, reply)
}

func (n *Node) handleBootstrapJoin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(headerProof) != "" {
		n.withSecret(n.handleSecretJoin)(w, r)
		return
	}

	var req joinRequest
	if !decodeBody(w, r, maxJoinBody, &req) {
		return
	}

	req.Method = ""

	if _, err := n.verifyToken(r.Context(), req.Token); err != nil {
		if !errors.Is(err, ErrInvalidJoin) {
			err = fmt.Errorf("%w: %w", errRetry, err)
		}

		n.metrics.joinRequests.WithLabelValues("rejected").Inc()
		writeRPCError(w, err)

		return
	}

	reply, err := n.relayJoin(r.Context(), req)
	if err != nil {
		writeRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, reply)
}

func (n *Node) handleSecretJoin(r *http.Request, c caller, body []byte) (int, any) {
	var req joinRequest
	if err := json.Unmarshal(body, &req); err != nil || req.NodeID != c.NodeID {
		n.metrics.joinRequests.WithLabelValues("rejected").Inc()
		return http.StatusBadRequest, rpcError{Error: "invalid join request", Code: codeInvalidJoin}
	}

	req.Token, req.Method = "", joinMethodSecret

	reply, err := n.relayJoin(r.Context(), req)
	if err != nil {
		return rpcErrorOf(err)
	}

	return http.StatusOK, reply
}

func (n *Node) relayJoin(ctx context.Context, req joinRequest) (joinReply, error) {
	var (
		reply joinReply
		err   error
	)

	if leading, _ := n.Leader(); leading {
		reply, err = n.admit(ctx, req)
	} else if n.formation.current() != FormationMember {
		err = fmt.Errorf("%w: this node is not a cluster member yet", errRetry)
	} else {
		err = n.peers.call(ctx, "/cluster/v1/join", req, &reply)
	}

	switch {
	case err == nil:
		n.metrics.joinRequests.WithLabelValues("admitted").Inc()
	case errors.Is(err, errRetry), errors.Is(err, errUnknown):
		n.metrics.joinRequests.WithLabelValues("unavailable").Inc()
	default:
		n.metrics.joinRequests.WithLabelValues("rejected").Inc()
	}

	return reply, err
}
