package server

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt"
	mcpttserver "github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/sip"
)

type mcpttCluster struct {
	*mcptt.Shared

	peers  *peerDirectory
	locals []netip.AddrPort
}

var _ mcpttserver.Cluster = (*mcpttCluster)(nil)

// TS 24.379 clause 10.1.1.4.1.1
func (c *mcpttCluster) Route(node string) (sip.URI, netip.AddrPort, bool) {
	for _, to := range c.peers.mcpttOf(node) {
		for _, l := range c.locals {
			if l.Addr().Unmap().Is4() == to.Addr().Is4() {
				u := sip.URI{Scheme: "sip", Host: sip.FormatHost(to.Addr()), Port: to.Port()}
				u.Params.Set("lr", "")

				return u, l, true
			}
		}
	}

	return sip.URI{}, netip.AddrPort{}, false
}

func (c *mcpttCluster) Peer(a netip.Addr) bool { return c.peers.peer(a) }

type mcpttSweep struct {
	database *db.DB
	node     *cluster.Node
	after    time.Duration
	logger   *slog.Logger
}

// TS 23.379 clause 7.4.2.3.2
func (w mcpttSweep) run(ctx context.Context) {
	ticker := time.NewTicker(min(max(w.after/10, 100*time.Millisecond), time.Minute))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if leading, _ := w.node.Leader(); leading {
			w.sweep(ctx)
		}
	}
}

func (w mcpttSweep) sweep(ctx context.Context) {
	calls, err := w.database.MCPTTGroupCalls(ctx)
	if err != nil || len(calls) == 0 {
		return
	}

	members, err := w.database.Members(ctx)
	if err != nil {
		return
	}

	known := map[string]bool{}
	for _, m := range members {
		known[m.NodeID] = true
	}

	done := map[string]bool{}

	for _, c := range calls {
		if done[c.NodeID] || known[c.NodeID] && !w.node.Unheard(c.NodeID, w.after) {
			continue
		}

		done[c.NodeID] = true

		n, err := w.database.ReleaseMCPTTGroupCallsOf(ctx, c.NodeID)

		switch {
		case err != nil && ctx.Err() == nil:
			w.logger.WarnContext(ctx, "MCPTT group calls of a lost node not released", slog.String("node", c.NodeID), slog.Any("error", err))
		case err == nil && n > 0:
			w.logger.InfoContext(ctx, "released the MCPTT group calls of a lost node", slog.String("node", c.NodeID), slog.Int64("calls", n))
		}
	}
}
