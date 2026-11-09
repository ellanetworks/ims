package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/icscf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
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

type peerDirectory struct {
	self     string
	database *db.DB
	operator func() settings.Operator
	fallback icscf.Resolver
	logger   *slog.Logger

	hosts  atomic.Pointer[map[string][]sip.Server]
	addrs  atomic.Pointer[[]netip.Addr]
	domain atomic.Pointer[trust.Domain]
}

func (p *peerDirectory) refresh(ctx context.Context) {
	members, err := p.database.Members(ctx)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to read the cluster members", slog.Any("error", err))
		return
	}

	op := p.operator()
	hosts := map[string][]sip.Server{}

	var addrs []netip.Addr

	for _, m := range members {
		if m.NodeID == p.self {
			continue
		}

		var servers []sip.Server

		for _, s := range m.SCSCFAddresses {
			a, err := netip.ParseAddrPort(s)
			if err != nil {
				continue
			}

			servers = append(servers, sip.Server{Transport: sip.TCP, Addr: a}, sip.Server{Transport: sip.UDP, Addr: a})

			if !slices.Contains(addrs, a.Addr().Unmap()) {
				addrs = append(addrs, a.Addr().Unmap())
			}
		}

		if len(servers) > 0 {
			hosts[canonicalHost(op.SCSCFHost(m.DiameterLabel))] = servers
		}
	}

	p.hosts.Store(&hosts)
	p.addrs.Store(&addrs)

	if d := p.domain.Load(); d != nil {
		d.SetPeers(addrs)
	}
}

func (p *peerDirectory) bind(d *trust.Domain) {
	p.domain.Store(d)

	if addrs := p.addrs.Load(); addrs != nil {
		d.SetPeers(*addrs)
	}
}

// RFC 3263 section 4
func (p *peerDirectory) Resolve(ctx context.Context, u sip.URI) ([]sip.Server, error) {
	if hosts := p.hosts.Load(); hosts != nil && u.IsSIP() && !u.IsSIPS() {
		if servers := (*hosts)[canonicalHost(u.Host)]; len(servers) > 0 {
			v, ok := u.Params.Get("transport")
			if !ok {
				return servers, nil
			}

			return slices.DeleteFunc(slices.Clone(servers), func(s sip.Server) bool {
				return !strings.EqualFold(string(s.Transport), v)
			}), nil
		}
	}

	return p.fallback.Resolve(ctx, u)
}

func canonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
