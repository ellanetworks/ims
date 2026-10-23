package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/mcptt"
	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// errMCPTTRestarting is of a change of the MCPTT settings while the AS restarts, for a new home domain.
var errMCPTTRestarting = errors.New("MCPTT is restarting")

// startMCPTT starts the MCPTT AS of the configuration under homeDomain: named after it, its host resolved to it in
// memory through the IMS.
func (s *Server) startMCPTT(ctx context.Context, homeDomain string) error {
	cfg := s.Config.MCPTT

	a, err := mcptt.Start(ctx, mcptt.Config{
		MCPTT: *cfg, Addresses: s.Config.SIP.Addresses, Network: s.ases.network, HomeDomain: homeDomain,
		DB: s.database, Logger: s.Logger,
	})
	if err != nil {
		return err
	}

	if cfg.Mode == config.MCPTTModeIMS {
		s.ases.resolver.Set(mcptt.Host(homeDomain), a.Servers())
	}

	s.mcpttHomeDomain = homeDomain
	s.mcptt.Store(a)

	return nil
}

// stopMCPTT stops the MCPTT AS, if it runs, until ctx is done: its calls end and its subscriptions go, through the
// core, which must still run.
func (s *Server) stopMCPTT(ctx context.Context) {
	a := s.mcptt.Swap(nil)
	if a == nil {
		return
	}

	s.ases.resolver.Set(mcptt.Host(s.mcpttHomeDomain), nil)

	if err := a.Shutdown(ctx); err != nil {
		s.Logger.Warn("failed to stop MCPTT cleanly", slog.Any("error", err))
	}
}

// resubscribeMCPTT has the MCPTT AS, if it runs, subscribe to the reg event package of the IMPUs it has no
// subscription of, now that a core runs to take the SUBSCRIBEs.
func (s *Server) resubscribeMCPTT() {
	if a := s.mcptt.Load(); a != nil {
		a.Resubscribe()
	}
}

// mcpttView serves the API from the MCPTT AS that runs: none while it restarts.
type mcpttView struct {
	s *Server
}

var _ api.MCPTTSettings = mcpttView{}

func (v mcpttView) live() (*mcpttsettings.Live, error) {
	if a := v.s.mcptt.Load(); a != nil {
		return a.Settings(), nil
	}

	return nil, errMCPTTRestarting
}

func (v mcpttView) Status() api.MCPTTStatus {
	a := v.s.mcptt.Load()
	if a == nil {
		return api.MCPTTStatus{Mode: string(v.s.Config.MCPTT.Mode)}
	}

	st := a.Status()

	return api.MCPTTStatus{
		Mode: st.Mode, Name: st.Name, ParticipatingPSI: st.ParticipatingPSI, ControllingPSI: st.ControllingPSI,
		SIP: st.SIP, MediaControl: st.MediaControl,
	}
}

func (v mcpttView) Get() mcpttsettings.Settings {
	if l, err := v.live(); err == nil {
		return l.Get()
	}

	return mcpttsettings.Settings{}
}

func (v mcpttView) UpdateServiceConfiguration(ctx context.Context, c mcpttsettings.ServiceConfiguration) error {
	l, err := v.live()
	if err != nil {
		return err
	}

	return l.UpdateServiceConfiguration(ctx, c)
}

func (v mcpttView) CreateUser(ctx context.Context, u mcpttsettings.User) (mcpttsettings.User, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.User{}, err
	}

	return l.CreateUser(ctx, u)
}

func (v mcpttView) UpdateUser(ctx context.Context, u mcpttsettings.User) (mcpttsettings.User, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.User{}, err
	}

	return l.UpdateUser(ctx, u)
}

func (v mcpttView) DeleteUser(ctx context.Context, id string) error {
	l, err := v.live()
	if err != nil {
		return err
	}

	return l.DeleteUser(ctx, id)
}

func (v mcpttView) CreateGroup(ctx context.Context, g mcpttsettings.Group) (mcpttsettings.Group, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, err
	}

	return l.CreateGroup(ctx, g)
}

func (v mcpttView) UpdateGroup(ctx context.Context, g mcpttsettings.Group) (mcpttsettings.Group, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, err
	}

	return l.UpdateGroup(ctx, g)
}

func (v mcpttView) DeleteGroup(ctx context.Context, uri string) error {
	l, err := v.live()
	if err != nil {
		return err
	}

	return l.DeleteGroup(ctx, uri)
}

func (v mcpttView) PutMember(ctx context.Context, group string, e mcpttsettings.Entry) (mcpttsettings.Group, bool, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, false, err
	}

	return l.PutMember(ctx, group, e)
}

func (v mcpttView) DeleteMember(ctx context.Context, group, id string) (mcpttsettings.Group, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, err
	}

	return l.DeleteMember(ctx, group, id)
}
