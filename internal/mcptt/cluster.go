package mcptt

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/sip"
)

const releaseTimeout = 5 * time.Second

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 9.2.2.3.2
type Shared struct {
	DB     *db.DB
	Node   string
	Logger *slog.Logger

	releases sync.WaitGroup
}

func (s *Shared) GroupAffiliations(ctx context.Context, group sip.URI) ([]affiliation.Row, error) {
	return s.DB.MCPTTGroupAffiliations(ctx, group)
}

func (s *Shared) NodeAffiliations(ctx context.Context) ([]affiliation.Row, error) {
	return s.DB.MCPTTNodeAffiliations(ctx, s.Node)
}

func (s *Shared) PutAffiliation(ctx context.Context, r affiliation.Row) error {
	return s.DB.PutMCPTTAffiliation(ctx, s.Node, r)
}

func (s *Shared) Self() string { return s.Node }

func (s *Shared) GroupCall(ctx context.Context, group sip.URI) (string, bool, error) {
	call, err := s.DB.MCPTTGroupCall(ctx, group)

	switch {
	case errors.Is(err, db.ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, err
	}

	return call.NodeID, true, nil
}

func (s *Shared) Claim(ctx context.Context, group sip.URI, claim string) (string, error) {
	call, err := s.DB.ClaimMCPTTGroupCall(ctx, group, s.Node, claim)
	if err != nil {
		return "", err
	}

	return call.NodeID, nil
}

func (s *Shared) Release(group sip.URI, claim string) {
	s.releases.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		defer cancel()

		if err := s.DB.ReleaseMCPTTGroupCall(ctx, group, s.Node, claim); err != nil && s.Logger != nil {
			s.Logger.WarnContext(ctx, "MCPTT group call not released: the leader releases it once this node is lost",
				slog.String("group", group.String()), slog.Any("error", err))
		}
	})
}

func (s *Shared) Wait() { s.releases.Wait() }
