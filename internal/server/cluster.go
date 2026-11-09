package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
)

type secrets struct {
	current atomic.Pointer[db.Secrets]
}

func newSecrets(ctx context.Context, d *db.DB) (*secrets, error) {
	v, err := d.Secrets(ctx)
	if err != nil {
		return nil, err
	}

	s := &secrets{}
	s.current.Store(&v)

	return s, nil
}

func (s *secrets) reload(ctx context.Context, d *db.DB) error {
	v, err := d.ReadSecrets(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	s.current.Store(&v)

	return nil
}

func (s *secrets) jwt() []byte { return s.current.Load().JWT }

func (s *secrets) cookieKey() []byte { return s.current.Load().OIDCCookieKey }

func (s *Server) fenceOnRemoval(ctx context.Context, node *cluster.Node) {
	select {
	case <-ctx.Done():
		return
	case <-node.Removed():
	}

	s.mu.Lock()
	running := s.running
	s.running = false
	s.mu.Unlock()

	if !running {
		return
	}

	s.Logger.WarnContext(ctx, "removed from the cluster: closing SIP and Diameter, so that phones and peers move to the other nodes")

	s.setHandler(api.NewLimitedHandler(s.baseAPIConfig(), s.unavailable))
	s.stopRunning(ctx)
}

const labelCheckInterval = 200 * time.Millisecond

// RFC 6733 section 4.3.1
func (s *Server) waitForLabel(ctx context.Context) error {
	ticker := time.NewTicker(labelCheckInterval)
	defer ticker.Stop()

	logged := false

	for {
		_, err := s.node.CommittedLabel(ctx)
		if err == nil {
			return nil
		}

		if !logged {
			s.Logger.WarnContext(ctx, "Diameter and SIP wait for the cluster to record this node's Diameter host label",
				slog.Any("error", err))

			logged = true
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.node.Removed():
			return cluster.ErrRemoved
		case <-ticker.C:
		}
	}
}

func apiAddress(cfg config.Config, listening net.Addr) string {
	if cfg.Cluster == nil {
		return ""
	}

	scheme := "http"
	if cfg.API.TLS != nil {
		scheme = "https"
	}

	host := cfg.API.Address.Unmap().String()

	if !cfg.API.Address.IsValid() || cfg.API.Address.IsUnspecified() {
		h, _, err := net.SplitHostPort(cfg.Cluster.AdvertiseAddress())
		if err != nil {
			return ""
		}

		host = h
	}

	_, port, err := net.SplitHostPort(listening.String())
	if err != nil {
		return ""
	}

	return scheme + "://" + net.JoinHostPort(host, port)
}
