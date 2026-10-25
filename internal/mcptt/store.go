package mcptt

import (
	"context"
	"strings"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
)

// store is the IMS's database as the AS's: the store of its settings and of its runtime state. home is the IMS's
// home domain, the domain of the organisation the database stores as empty, which follows the home domain
// (NewSettings).
type store struct {
	d    *db.DB
	home string
}

var (
	_ settings.Store = store{}
	_ server.State   = store{}
)

// UpdateSystem stores nothing: the PSIs are of the home domain (System).
func (store) UpdateSystem(context.Context, settings.System) error { return nil }

func (s store) UpdateServiceConfiguration(ctx context.Context, c settings.ServiceConfiguration) error {
	if strings.EqualFold(strings.TrimSuffix(c.Domain, "."), s.home) {
		c.Domain = ""
	}

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

func (s store) CreateGroup(ctx context.Context, g settings.Group, users []settings.User) error {
	return s.d.CreateMCPTTGroup(ctx, g, users)
}

func (s store) UpdateGroup(ctx context.Context, g settings.Group, users []settings.User) error {
	return s.d.UpdateMCPTTGroup(ctx, g, users)
}

func (s store) DeleteGroup(ctx context.Context, uri string) error {
	return s.d.DeleteMCPTTGroup(ctx, uri)
}

func (s store) PutMember(ctx context.Context, group string, e settings.Entry, users []settings.User) error {
	return s.d.PutMCPTTMember(ctx, group, e, users)
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
