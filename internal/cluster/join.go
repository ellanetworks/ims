package cluster

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/listener"
	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const (
	joinTimeout        = time.Minute
	joinAttemptTimeout = 10 * time.Second
	addServerTimeout   = 10 * time.Second
)

var (
	ErrInvalidJoin     = errors.New("invalid join request")
	ErrAlreadyMember   = errors.New("this node already has other cluster members: remove it from its cluster first")
	ErrJoinUnavailable = errors.New("no node of the cluster accepted the join request")
)

type joinRequest struct {
	Token       string `json:"token"`
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	Certificate string `json:"certificate"`
	Schema      int    `json:"schema"`
	Protocol    int    `json:"protocol"`
	Version     string `json:"version"`
	Label       string `json:"diameter_label,omitempty"`
	Generated   bool   `json:"label_generated,omitempty"`
	Method      string `json:"method,omitempty"`
}

type joinReply struct {
	Pins map[string]string `json:"pins"`
}

type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (n *Node) MintToken(ctx context.Context, ttl time.Duration) (Token, error) {
	if !n.cfg.clustered() {
		return Token{}, ErrNotClustered
	}

	if ttl < pki.MinTokenTTL || ttl > pki.MaxTokenTTL {
		return Token{}, fmt.Errorf("%w: the token lifetime must be between %s and %s", ErrInvalidJoin, pki.MinTokenTTL,
			pki.MaxTokenTTL)
	}

	secrets, err := n.db.Secrets(ctx)
	if err != nil {
		return Token{}, err
	}

	members, err := n.db.Members(ctx)
	if err != nil {
		return Token{}, err
	}

	claims := pki.Claims{ID: pki.NewTokenID(), ClusterID: secrets.ClusterID}

	for _, m := range members {
		if m.Fingerprint != "" && m.Address != "" {
			claims.Pins = append(claims.Pins, m.Fingerprint)
			claims.Addresses = append(claims.Addresses, m.Address)
		}
	}

	if len(claims.Pins) == 0 {
		return Token{}, fmt.Errorf("%w: the cluster has not registered its first member yet", ErrNoLeader)
	}

	now := n.cfg.Now()
	expires := now.Add(ttl)
	claims.IssuedAt, claims.ExpiresAt = now.Unix(), expires.Unix()

	token, err := pki.MintToken(secrets.JoinKey, claims)
	if err != nil {
		return Token{}, err
	}

	if err := n.db.CreateJoinToken(ctx, claims.ID, time.Unix(claims.ExpiresAt, 0)); err != nil {
		return Token{}, err
	}

	return Token{Token: token, ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC()}, nil
}

func (n *Node) verifyToken(ctx context.Context, token string) (pki.Claims, error) {
	secrets, err := n.db.ReadSecrets(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return pki.Claims{}, fmt.Errorf("%w: this node has not caught up with the cluster", errRetry)
	}

	if err != nil {
		return pki.Claims{}, err
	}

	claims, err := pki.VerifyToken(secrets.JoinKey, token, n.cfg.Now())
	if err != nil {
		return pki.Claims{}, fmt.Errorf("%w: %w", ErrInvalidJoin, err)
	}

	if claims.ClusterID != secrets.ClusterID {
		return pki.Claims{}, fmt.Errorf("%w: the token is for another cluster", ErrInvalidJoin)
	}

	return claims, nil
}

func (n *Node) admit(ctx context.Context, req joinRequest) (joinReply, error) {
	var tokenID, clusterID string

	if req.Method == joinMethodSecret {
		secrets, err := n.db.ReadSecrets(ctx)
		if err != nil {
			return joinReply{}, fmt.Errorf("%w: %w", errRetry, err)
		}

		clusterID = secrets.ClusterID
	} else {
		claims, err := n.verifyToken(ctx, req.Token)
		if err != nil {
			return joinReply{}, err
		}

		tokenID, clusterID = claims.ID, claims.ClusterID
	}

	cert, err := pki.ParseCert([]byte(req.Certificate))
	if err != nil {
		return joinReply{}, fmt.Errorf("%w: %w", ErrInvalidJoin, err)
	}

	certCluster, nodeID, err := pki.Identity(cert)

	switch {
	case err != nil:
		return joinReply{}, fmt.Errorf("%w: %w", ErrInvalidJoin, err)
	case certCluster != clusterID || nodeID != req.NodeID:
		return joinReply{}, fmt.Errorf("%w: the certificate is not node %s's in this cluster", ErrInvalidJoin, req.NodeID)
	case cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil:
		return joinReply{}, fmt.Errorf("%w: the certificate is not self-signed", ErrInvalidJoin)
	case req.Schema < n.db.Schema() || req.Protocol != db.ProtocolVersion:
		return joinReply{}, fmt.Errorf("%w: the node supports schema %d and protocol %d, the cluster runs %d and %d",
			ErrInvalidJoin, req.Schema, req.Protocol, n.db.Schema(), db.ProtocolVersion)
	case req.NodeID == n.id:
		return joinReply{}, fmt.Errorf("%w: the node has the leader's ID", ErrInvalidJoin)
	}

	if err := validAddress(req.Address); err != nil {
		return joinReply{}, err
	}

	m := db.Member{
		NodeID: req.NodeID, Address: req.Address, Fingerprint: pki.Fingerprint(cert),
		SchemaVersion: req.Schema, Version: req.Version, DiameterLabel: req.Label, LabelGenerated: req.Generated,
	}

	if err := n.db.AdmitMember(ctx, tokenID, m); err != nil {
		return joinReply{}, err
	}

	if err := n.addServer(raft.ServerID(m.NodeID), raft.ServerAddress(m.Address)); err != nil {
		return joinReply{}, err
	}

	n.cfg.Logger.InfoContext(ctx, "node joined the cluster", slog.String("node", m.NodeID),
		slog.String("address", m.Address))

	members, err := n.db.Members(ctx)
	if err != nil {
		return joinReply{}, err
	}

	pins := map[string]string{}

	for _, m := range members {
		if m.Fingerprint != "" {
			pins[m.Fingerprint] = m.NodeID
		}
	}

	return joinReply{Pins: pins}, nil
}

func validAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("%w: address %q is not a host:port", ErrInvalidJoin, addr)
	}

	if ip := net.ParseIP(host); ip != nil && (ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast()) {
		return fmt.Errorf("%w: address %q is not a node's", ErrInvalidJoin, addr)
	}

	return nil
}

func (n *Node) addServer(id raft.ServerID, addr raft.ServerAddress) error {
	r, _ := n.current()

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return classify(err)
	}

	for _, s := range future.Configuration().Servers {
		if s.ID != id {
			continue
		}

		if s.Address == addr {
			return nil
		}

		if s.Suffrage == raft.Voter {
			return classify(r.AddVoter(id, addr, 0, addServerTimeout).Error())
		}
	}

	return classify(r.AddNonvoter(id, addr, 0, addServerTimeout).Error())
}

func (n *Node) Join(ctx context.Context, token string) error {
	if !n.cfg.clustered() {
		return ErrNotClustered
	}

	n.joinMu.Lock()
	defer n.joinMu.Unlock()

	claims, err := pki.ReadToken(strings.TrimSpace(token))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidJoin, err)
	}

	r, _ := n.current()

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return err
	}

	for _, s := range future.Configuration().Servers {
		if s.ID != raft.ServerID(n.id) {
			return ErrAlreadyMember
		}
	}

	cert, err := n.certs.pending(claims.ClusterID, n.cfg.Now())
	if err != nil {
		return err
	}

	certPEM, _, err := pki.EncodeCert(cert)
	if err != nil {
		return err
	}

	req := joinRequest{
		Token: strings.TrimSpace(token), NodeID: n.id, Address: n.cfg.Advertise, Certificate: string(certPEM),
		Schema: db.SchemaVersion(), Protocol: db.ProtocolVersion, Version: n.cfg.Version, Label: n.label,
		Generated: n.generated,
	}

	reply, err := n.redeem(ctx, claims, req)
	if err != nil {
		return err
	}

	if err := n.reset(context.WithoutCancel(ctx), cert, reply.Pins); err != nil {
		return fmt.Errorf("restart as a cluster member: %w", err)
	}

	n.cfg.Logger.InfoContext(ctx, "joined the cluster", slog.String("cluster", claims.ClusterID))

	return nil
}

func (n *Node) redeem(ctx context.Context, claims pki.Claims, req joinRequest) (joinReply, error) {
	ctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()

	pins := make(map[string]bool, len(claims.Pins))
	for _, p := range claims.Pins {
		pins[p] = true
	}

	client := &http.Client{Timeout: joinAttemptTimeout, Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			conn, _, err := n.ln.Dial(ctx, addr, listener.ALPNJoin, func(fp string) (string, bool) {
				return listener.AnyNode, pins[fp]
			})

			return conn, err
		},
		DisableKeepAlives: true,
	}}

	var last error

	for attempt := 0; ; attempt++ {
		for _, addr := range claims.Addresses {
			var reply joinReply

			err := n.postJoin(ctx, client, addr, req, &reply)
			if err == nil {
				return reply, nil
			}

			if !retryableJoin(err) {
				return joinReply{}, err
			}

			last = err

			n.cfg.Logger.DebugContext(ctx, "join attempt failed", slog.String("seed", addr), slog.Any("error", err))
		}

		select {
		case <-ctx.Done():
			return joinReply{}, fmt.Errorf("%w: %w", ErrJoinUnavailable, last)
		case <-time.After(backoff(attempt + 4)):
		}
	}
}

func retryableJoin(err error) bool {
	if errors.Is(err, errRetry) {
		return true
	}

	var remote *RemoteError

	return errors.As(err, &remote) && remote.Status >= http.StatusInternalServerError && remote.Code == ""
}

func (n *Node) postJoin(ctx context.Context, client *http.Client, addr string, req joinRequest, out *joinReply) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/cluster/v1/join", bytes.NewReader(body))
	if err != nil {
		return err
	}

	r.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(r)
	if err != nil {
		return fmt.Errorf("%w: %w", errRetry, err)
	}

	defer func() { _ = resp.Body.Close() }()

	return decodeReply(resp, out)
}

func (n *Node) reset(ctx context.Context, cert tls.Certificate, pins map[string]string) error {
	select {
	case <-n.done:
		return raft.ErrRaftShutdown
	default:
	}

	n.stepDown()

	if err := n.stopRaft(); err != nil {
		n.cfg.Logger.WarnContext(ctx, "failed to stop raft cleanly before joining", slog.Any("error", err))
	}

	if err := writeFile(filepath.Join(n.cfg.Dir, joiningFile), nil); err != nil {
		return err
	}

	if err := n.certs.install(cert, pins); err != nil {
		return err
	}

	if err := n.finishJoin(); err != nil {
		return err
	}

	n.checkpointed = true

	return n.startRaft(ctx)
}

func (n *Node) finishJoin() error {
	if err := writeFile(filepath.Join(n.cfg.Dir, joinedFile), nil); err != nil {
		return err
	}

	for _, name := range []string{logFile, "snapshots"} {
		if err := os.RemoveAll(filepath.Join(n.cfg.Dir, name)); err != nil {
			return err
		}
	}

	n.certs.dropPending()

	if err := os.Remove(filepath.Join(n.cfg.Dir, joiningFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return syncDir(n.cfg.Dir)
}
