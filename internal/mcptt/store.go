package mcptt

import (
	"context"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
)

// store is the IMS's database as the AS's: the store of its settings and of its runtime state.
type store struct{ d *db.DB }

var (
	_ settings.Store = store{}
	_ server.State   = store{}
)

func (s store) UpdateSystem(ctx context.Context, x settings.System) error {
	return s.d.UpdateMCPTTSystem(ctx, x)
}

func (s store) UpdateServiceConfiguration(ctx context.Context, c settings.ServiceConfiguration) error {
	return s.d.UpdateMCPTTServiceConfiguration(ctx, c)
}

func (s store) CreateUser(ctx context.Context, u settings.User) error {
	return s.d.CreateMCPTTUser(ctx, u)
}

func (s store) UpdateUser(ctx context.Context, u settings.User) error {
	return s.d.UpdateMCPTTUser(ctx, u)
}

func (s store) DeleteUser(ctx context.Context, mcpttID string) error {
	return s.d.DeleteMCPTTUser(ctx, mcpttID)
}

func (s store) CreateGroup(ctx context.Context, g settings.Group) error {
	return s.d.CreateMCPTTGroup(ctx, g)
}

func (s store) UpdateGroup(ctx context.Context, g settings.Group) error {
	return s.d.UpdateMCPTTGroup(ctx, g)
}

func (s store) DeleteGroup(ctx context.Context, uri string) error {
	return s.d.DeleteMCPTTGroup(ctx, uri)
}

func (s store) PutMember(ctx context.Context, group string, e settings.Entry) error {
	return s.d.PutMCPTTMember(ctx, group, e)
}

func (s store) DeleteMember(ctx context.Context, group, mcpttID string) error {
	return s.d.DeleteMCPTTMember(ctx, group, mcpttID)
}

func (s store) SaveRegistrations(ctx context.Context, records []registrar.Record, deleted []string) error {
	return s.d.SaveMCPTTRegistrations(ctx, records, deleted)
}

func (s store) SaveAffiliations(ctx context.Context, records []affiliation.Record) error {
	return s.d.SaveMCPTTAffiliations(ctx, records)
}
