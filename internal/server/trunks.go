package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/ibcf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/trgw"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
)

func (s *Server) trunkStates() settings.TrunkStates {
	if s.trunks == nil {
		return nil
	}

	return s.trunks
}

type ibcfSender struct{ s *Server }

func (i ibcfSender) Send(ctx context.Context, req *sip.Request, to sip.Server) (*sip.Response, error) {
	c := i.s.core.Load()
	if c == nil || c.sip.ibcf == nil {
		return nil, errNoCore
	}

	return c.sip.ibcf.Send(ctx, req, to)
}

// TS 24.229 clause 4.4.1
func (s *Server) guardTrunks(ctx context.Context, live *settings.Live) {
	domain := trust.New(s.Config.SIP.Addresses, s.Config.SIP.TrustedNetworks)

	guard := func(p netip.Prefix) error {
		if domain.Overlaps(p) {
			return fmt.Errorf("inbound_cidrs %s overlaps sip.addresses or sip.trusted_networks, the trust domain of the IMS", p)
		}

		return nil
	}

	live.SetTrunkGuard(guard)

	for _, t := range live.Get().Trunks {
		for _, p := range t.InboundCIDRs {
			if err := guard(p); err != nil {
				s.Logger.WarnContext(ctx, "a SIP trunk accepts requests from the trust domain: change its inbound CIDRs",
					slog.String("trunk", t.Name), slog.Any("error", err))
			}
		}
	}
}

// TS 23.228 clause I.2
func (s *sipServer) startTrGW(cfg *config.TrGW, m *trgw.Metrics) (*ibcf.MediaConfig, error) {
	if cfg == nil {
		return nil, nil //nolint:nilnil
	}

	var (
		addrs        []trgw.Address
		phone, trunk []netip.Addr
	)

	for _, a := range cfg.PhoneAddresses {
		addrs, phone = append(addrs, trgw.Address{Bind: a.Address, Advertise: a.Advertise}), append(phone, a.Address)
	}

	for _, a := range cfg.TrunkAddresses {
		addrs, trunk = append(addrs, trgw.Address{Bind: a.Address, Advertise: a.Advertise}), append(trunk, a.Address)
	}

	g, err := trgw.New(trgw.WithAddresses(addrs...), trgw.WithPortRange(uint16(cfg.MinPort), uint16(cfg.MaxPort)),
		trgw.WithMediaTimeout(*cfg.MediaTimeout), trgw.WithMetrics(m))
	if err != nil {
		return nil, fmt.Errorf("start the TrGW: %w", err)
	}

	s.trgw = g

	return &ibcf.MediaConfig{Gateway: g, Phone: phone, Trunk: trunk}, nil
}

// SIPconnect 2.0 section 17.2
func ibcfTLS(ext config.IBCFExternal) ([]netip.AddrPort, *tls.Certificate, error) {
	if ext.TLS == nil {
		return nil, nil, nil
	}

	addrs := make([]netip.AddrPort, 0, len(ext.Addresses))
	for _, a := range ext.Addresses {
		addrs = append(addrs, netip.AddrPortFrom(a, uint16(ext.TLS.Port)))
	}

	if ext.TLS.Cert == "" {
		return addrs, nil, nil
	}

	cert, err := tls.LoadX509KeyPair(ext.TLS.Cert, ext.TLS.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("load the IBCF certificate: %w", err)
	}

	return addrs, &cert, nil
}
