package scscf

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/sip"
)

type Directory interface {
	Serves(ctx context.Context, number sip.URI) (bool, error)
}

type destination int

const (
	onNetwork destination = iota
	offNet
	noUsableTrunk
)

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.6.2
func (s *Sessions) breakout(ctx context.Context, out *sip.Request, onNet bool) destination {
	if onNet || out.Method != "INVITE" || s.cfg.BGCF == nil || len(s.cfg.IBCF) == 0 || s.cfg.Directory == nil {
		return onNetwork
	}

	number, ok := globalNumber(out.URI)
	if !ok {
		return onNetwork
	}

	_, err := s.cfg.BGCF.Trunks(number)
	if err != nil && !errors.Is(err, bgcf.ErrNoUsableTrunk) {
		return onNetwork
	}

	if s.served(ctx, out.URI) {
		return onNetwork
	}

	if err != nil {
		return noUsableTrunk
	}

	s.log.DebugContext(ctx, "number not in the home network routed to the IBCF", slog.String("request-uri", out.URI.String()))

	return offNet
}

// TS 24.229 clause 5.4.3.2
func (s *Sessions) served(ctx context.Context, u sip.URI) bool {
	regs, barred, err := Recipients(ctx, s.r.cfg.DB, u, s.r.cfg.HomeDomain)
	if err == nil && (len(regs) > 0 || barred) {
		return true
	}

	known, err := s.cfg.Directory.Serves(ctx, u)
	if err != nil {
		s.log.InfoContext(ctx, "number translation failed: routed in the home network", slog.String("request-uri", u.String()),
			slog.Any("error", err))

		return true
	}

	return known
}

// TS 24.229 clause 5.4.3.2
func withoutAccessNetworkChargingInfo(h *sip.Header) {
	ps, err := sip.ParseParams(h.Get("P-Charging-Vector"))
	if err != nil || !ps.Has("access-network-charging-info") {
		return
	}

	ps.Del("access-network-charging-info")
	h.Set("P-Charging-Vector", strings.TrimPrefix(ps.String(), ";"))
}
