package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/ibcf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/trgw"
	"github.com/ellanetworks/ims/internal/trunkmon"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
)

func (s *Server) trunkStates() settings.TrunkStates {
	if s.trunks == nil {
		return nil
	}

	return s.trunks
}

type runtimeSettings struct{ *settings.Live }

func (r runtimeSettings) Get() settings.Settings {
	return r.Runtime()
}

type ibcfSender struct{ s *Server }

func (i ibcfSender) Send(ctx context.Context, t settings.Trunk, a settings.TrunkAddress, req *sip.Request, to sip.Server) (*sip.Response, error) {
	b := i.ibcf()
	if b == nil {
		return nil, trunkmon.ErrNotReady
	}

	res, err := b.Send(ctx, t, a, req, to)
	if err != nil && i.ibcf() != b {
		return nil, trunkmon.ErrNotReady
	}

	return res, err
}

func (i ibcfSender) Transports(t settings.Trunk) ([]sip.Transport, error) {
	b := i.ibcf()
	if b == nil {
		return nil, trunkmon.ErrNotReady
	}

	return b.Transports(t), nil
}

func (i ibcfSender) ibcf() *ibcf.IBCF {
	if c := i.s.core.Load(); c != nil {
		return c.sip.ibcf
	}

	return nil
}

func (s *Server) reprobeTrunks() {
	if s.trunks != nil {
		s.trunks.Reprobe()
	}
}

// TS 24.229 clause 4.4.1
// SIPconnect 2.0 section 17.5
func (s *Server) guardTrunks(ctx context.Context, live *settings.Live) {
	domain := trust.New(s.Config.SIP.Addresses, s.Config.SIP.TrustedNetworks)
	certificate := s.Config.IBCF != nil && s.Config.IBCF.External.Certificate()

	guard := func(t settings.Trunk) error {
		for _, p := range t.InboundCIDRs {
			if domain.Overlaps(p) {
				return settings.Invalidf("inbound_cidrs %s overlaps sip.addresses or sip.trusted_networks of the configuration file", p)
			}
		}

		if t.TLS.Required && !certificate {
			return settings.Invalidf("tls.required needs ibcf.external.tls in the configuration file")
		}

		return nil
	}

	live.SetTrunkGuard(guard)

	for _, t := range live.Get().Trunks {
		if err := guard(t.Effective()); err != nil {
			s.Logger.WarnContext(ctx, "SIP trunk not used: change it", slog.String("trunk", t.Name), slog.Any("error", err))
		}
	}
}

// TS 23.228 clause I.2
func (s *sipServer) startTrGW(cfg config.Config, timeout time.Duration, m *trgw.Metrics) (*ibcf.MediaConfig, error) {
	var addrs []trgw.Address

	for _, a := range cfg.SIP.Addresses {
		addrs = append(addrs, trgw.Address{Bind: a})
	}

	for _, a := range cfg.IBCF.External.Addresses {
		addrs = append(addrs, trgw.Address{Bind: a.Address, Advertise: a.Advertise})
	}

	g, err := trgw.New(trgw.WithAddresses(addrs...), trgw.WithPortRange(uint16(cfg.IBCF.Media.MinPort), uint16(cfg.IBCF.Media.MaxPort)),
		trgw.WithMediaTimeout(timeout), trgw.WithMetrics(m))
	if err != nil {
		return nil, fmt.Errorf("start the TrGW: %w", err)
	}

	s.trgw = g

	return &ibcf.MediaConfig{Gateway: g, Phone: slices.Clone(cfg.SIP.Addresses), Trunk: cfg.IBCF.External.Addrs()}, nil
}

// SIPconnect 2.0 section 17.2
func ibcfTLS(ext config.IBCFExternal) ([]netip.AddrPort, *tls.Certificate, error) {
	addrs := make([]netip.AddrPort, 0, len(ext.Addresses))
	for _, a := range ext.Addresses {
		addrs = append(addrs, netip.AddrPortFrom(a.Address, config.IBCFTLSPort))
	}

	if !ext.Certificate() {
		return addrs, nil, nil
	}

	cert, err := tls.LoadX509KeyPair(ext.TLS.Cert, ext.TLS.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("load the IBCF certificate: %w", err)
	}

	return addrs, &cert, nil
}
