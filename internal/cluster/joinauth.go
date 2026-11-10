package cluster

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/listener"
	"github.com/ellanetworks/ims/internal/cluster/pki"
)

const (
	headerNode    = "Ims-Node"
	headerAddress = "Ims-Address"
	headerProof   = "Ims-Proof"

	proofFailureWindow = time.Minute
	proofFailureLimit  = 10
	maxTrackedIPs      = 4096
	helloTimeout       = 3 * time.Second
)

var (
	ErrWrongSecret = errors.New("the peer refused this node's cluster secret proof: check that every node has the same cluster secret")
	errNoSecret    = errors.New("no cluster secret is configured")
	errBadProof    = errors.New("the peer's answer does not carry a valid cluster secret proof")
)

type joinAuth struct {
	path   string
	now    func() time.Time
	logger *slog.Logger

	mu       sync.Mutex
	last     [][]byte
	failures map[string][]time.Time
	reported bool
}

func newJoinAuth(path string, now func() time.Time, logger *slog.Logger) *joinAuth {
	return &joinAuth{path: path, now: now, logger: logger, failures: map[string][]time.Time{}}
}

func (a *joinAuth) keys() ([][]byte, error) {
	if a.path == "" {
		return nil, errNoSecret
	}

	keys, err := pki.ReadSecrets(a.path)

	a.mu.Lock()
	defer a.mu.Unlock()

	if err != nil {
		if a.last == nil {
			return nil, err
		}

		if !a.reported {
			a.reported = true
			a.logger.Error("failed to read the cluster secret, using the last one read", slog.Any("error", err))
		}

		return a.last, nil
	}

	a.last, a.reported = keys, false

	return keys, nil
}

func (a *joinAuth) enabled() bool {
	return a.path != ""
}

func exporter(cs tls.ConnectionState) ([]byte, error) {
	return cs.ExportKeyingMaterial(pki.ExporterLabel(), nil, pki.ExporterSize)
}

func (a *joinAuth) prove(in pki.ProofInput) (string, error) {
	keys, err := a.keys()
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(pki.Prove(keys[0], in)), nil
}

func (a *joinAuth) verify(in pki.ProofInput, proof string) bool {
	keys, err := a.keys()
	if err != nil {
		return false
	}

	raw, err := base64.StdEncoding.DecodeString(proof)
	if err != nil {
		return false
	}

	return pki.VerifyProof(keys, in, raw)
}

func (a *joinAuth) limited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.recent(ip)) >= proofFailureLimit
}

func (a *joinAuth) recent(ip string) []time.Time {
	cutoff := a.now().Add(-proofFailureWindow)
	kept := a.failures[ip][:0]

	for _, t := range a.failures[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) == 0 {
		delete(a.failures, ip)
		return nil
	}

	a.failures[ip] = kept

	return kept
}

func (a *joinAuth) fail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.failures) >= maxTrackedIPs {
		for other := range a.failures {
			a.recent(other)
		}
	}

	if len(a.failures) < maxTrackedIPs || a.failures[ip] != nil {
		a.failures[ip] = append(a.recent(ip), a.now())
	}
}

type connKey struct{}

type caller struct {
	NodeID      string
	Address     string
	Fingerprint string
}

type secretHandler func(r *http.Request, c caller, body []byte) (int, any)

func (n *Node) withSecret(h secretHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)

		conn, _ := r.Context().Value(connKey{}).(net.Conn)

		cs, ok := listener.State(conn)
		if !ok || len(cs.PeerCertificates) == 0 {
			writeJSON(w, http.StatusUnauthorized, rpcError{Error: "a client certificate is required", Code: codeInvalidProof})
			return
		}

		if n.auth.limited(ip) {
			n.metrics.joinRequests.WithLabelValues("rate_limited").Inc()
			writeJSON(w, http.StatusTooManyRequests, rpcError{Error: "too many failed proofs"})

			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJoinBody))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcError{Error: err.Error(), Code: "invalid"})
			return
		}

		_, certNode, idErr := pki.Identity(cs.PeerCertificates[0])
		ekm, ekmErr := exporter(cs)
		c := caller{
			NodeID: r.Header.Get(headerNode), Address: r.Header.Get(headerAddress),
			Fingerprint: pki.Fingerprint(cs.PeerCertificates[0]),
		}

		in := pki.ProofInput{
			Role: pki.RoleClient, Exporter: ekm, NodeID: c.NodeID, Fingerprint: c.Fingerprint, Address: c.Address,
			BodyHash: pki.BodyHash(body),
		}

		if idErr != nil || ekmErr != nil || certNode != c.NodeID || !n.auth.verify(in, r.Header.Get(headerProof)) {
			n.auth.fail(ip)
			n.metrics.joinRequests.WithLabelValues("unauthorized").Inc()
			n.cfg.Logger.WarnContext(r.Context(), "refused a cluster request with a wrong secret proof", slog.String("remote", r.RemoteAddr),
				slog.String("node", c.NodeID))
			writeJSON(w, http.StatusUnauthorized, rpcError{Error: "invalid cluster secret proof", Code: codeInvalidProof})

			return
		}

		status, v := h(r, c, body)

		out, err := json.Marshal(v)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, rpcError{Error: err.Error()})
			return
		}

		own := n.certs.presented()

		proof, err := n.auth.prove(pki.ProofInput{
			Role: pki.RoleServer, Exporter: ekm, NodeID: n.id, Fingerprint: pki.Fingerprint(own.Leaf),
			Address: n.cfg.Advertise, BodyHash: pki.BodyHash(out),
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, rpcError{Error: err.Error()})
			return
		}

		w.Header().Set(headerNode, n.id)
		w.Header().Set(headerAddress, n.cfg.Advertise)
		w.Header().Set(headerProof, proof)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(out)
	}
}

type secretReply struct {
	Status      int
	NodeID      string
	Fingerprint string
	Body        []byte
}

func (n *Node) secretCall(ctx context.Context, addr, path string, in any) (secretReply, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return secretReply{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()

	conn, peer, err := n.ln.Dial(ctx, addr, listener.ALPNJoin, func(string) (string, bool) { return listener.AnyNode, true })
	if err != nil {
		return secretReply{}, fmt.Errorf("%w: %w", errRetry, err)
	}

	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	cs := conn.ConnectionState()

	ekm, err := exporter(cs)
	if err != nil {
		return secretReply{}, err
	}

	own := n.certs.presented()

	proof, err := n.auth.prove(pki.ProofInput{
		Role: pki.RoleClient, Exporter: ekm, NodeID: n.id, Fingerprint: pki.Fingerprint(own.Leaf),
		Address: n.cfg.Advertise, BodyHash: pki.BodyHash(body),
	})
	if err != nil {
		return secretReply{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return secretReply{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerNode, n.id)
	req.Header.Set(headerAddress, n.cfg.Advertise)
	req.Header.Set(headerProof, proof)
	req.Close = true

	if err := req.Write(conn); err != nil {
		return secretReply{}, fmt.Errorf("%w: %w", errRetry, err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return secretReply{}, fmt.Errorf("%w: %w", errRetry, err)
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJoinBody))
	if err != nil {
		return secretReply{}, fmt.Errorf("%w: %w", errRetry, err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return secretReply{}, ErrWrongSecret
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return secretReply{}, fmt.Errorf("%w: %w", errRetry, ErrWrongSecret)
	}

	fp := pki.Fingerprint(cs.PeerCertificates[0])

	if resp.Header.Get(headerNode) != peer || !n.auth.verify(pki.ProofInput{
		Role: pki.RoleServer, Exporter: ekm, NodeID: peer, Fingerprint: fp, Address: resp.Header.Get(headerAddress),
		BodyHash: pki.BodyHash(raw),
	}, resp.Header.Get(headerProof)) {
		return secretReply{}, errBadProof
	}

	return secretReply{Status: resp.StatusCode, NodeID: peer, Fingerprint: fp, Body: raw}, nil
}
