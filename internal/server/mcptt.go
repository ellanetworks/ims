package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/homedomain"
	"github.com/ellanetworks/ims/internal/mcptt"
	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/controlling"
	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

var errMCPTTRestarting = errors.New("MCPTT is restarting")

func (s *Server) startMCPTT(ctx context.Context, domains homedomain.Domains) error {
	cfg := s.Config.MCPTT
	homeDomain := domains.Primary()

	mc := mcptt.Config{
		MCPTT: *cfg, Addresses: s.Config.SIP.Addresses, Network: s.ases.network, HomeDomain: homeDomain,
		DB: s.database, Logger: s.Logger, TracerProvider: s.tracerProvider,
	}

	var shared *mcpttCluster

	if s.Config.Cluster != nil && cfg.Mode == config.MCPTTModeIMS {
		shared = &mcpttCluster{Shared: &mcptt.Shared{DB: s.database, Node: s.node.ID(), Logger: s.Logger}, peers: s.peers}

		for _, a := range s.Config.SIP.Addresses {
			shared.locals = append(shared.locals, netip.AddrPortFrom(a.Unmap(), uint16(cfg.Port)))
		}

		mc.Cluster = shared
	}

	a, err := mcptt.Start(ctx, mc)
	if err != nil {
		return err
	}

	s.mcpttHomeDomain = homeDomain
	s.mcpttCluster = shared
	s.mcptt.Store(a)
	s.routeMCPTT(domains)

	if shared != nil {
		s.node.SetMCPTTAddresses(a.Listeners())
	}

	return nil
}

func (s *Server) stopMCPTT(ctx context.Context) {
	a := s.mcptt.Swap(nil)
	if a == nil {
		return
	}

	s.mcpttHosts = s.ases.route(s.mcpttHosts, nil, nil)

	if err := a.Shutdown(ctx); err != nil {
		s.Logger.WarnContext(ctx, "failed to stop MCPTT cleanly", slog.Any("error", err))
	}

	if c := s.mcpttCluster; c != nil {
		c.Wait()
	}
}

func (s *Server) routeMCPTT(domains homedomain.Domains) {
	a := s.mcptt.Load()
	if a == nil || s.Config.MCPTT.Mode != config.MCPTTModeIMS {
		return
	}

	s.mcpttHosts = s.ases.route(s.mcpttHosts, asHosts(domains, mcptt.Host), a.Servers())
}

func (s *Server) resubscribeMCPTT(ctx context.Context) {
	if a := s.mcptt.Load(); a != nil {
		a.Resubscribe(ctx)
	}
}

type mcpttView struct {
	s *Server
}

var (
	_ api.MCPTTSettings = mcpttView{}
	_ api.MCPTTState    = mcpttView{}
)

func (v mcpttView) as() (*mcptt.AS, error) {
	if a := v.s.mcptt.Load(); a != nil {
		return a, nil
	}

	return nil, errMCPTTRestarting
}

func (v mcpttView) Registrations() ([]api.MCPTTRegistration, error) {
	a, err := v.as()
	if err != nil {
		return nil, err
	}

	clients := a.Clients()
	out := make([]api.MCPTTRegistration, len(clients))

	for i, b := range clients {
		out[i] = api.MCPTTRegistration{
			MCPTTID: b.MCPTTID.String(), ClientID: b.ClientID, IMPU: b.IMPU.String(), IMPUs: uriStrings(b.IMPUs),
			Since: b.Since, Expires: b.Expires,
		}

		for _, c := range b.Contacts {
			out[i].Contacts = append(out[i].Contacts, c.URI.String())
		}
	}

	return out, nil
}

func (v mcpttView) Affiliations() ([]api.MCPTTAffiliation, error) {
	a, err := v.as()
	if err != nil {
		return nil, err
	}

	var out []api.MCPTTAffiliation

	for _, c := range a.Affiliations() {
		for _, g := range c.Groups {
			if g.Status == affiliation.Deaffiliated {
				continue
			}

			out = append(out, api.MCPTTAffiliation{
				MCPTTID: c.User.String(), ClientID: c.Client, GroupID: g.ID.String(), Status: string(g.Status),
				Implicit: g.Configured,
			})
		}
	}

	return out, nil
}

func (v mcpttView) Calls(ctx context.Context) ([]api.MCPTTCall, error) {
	a, err := v.as()
	if err != nil {
		return nil, err
	}

	calls, err := a.Calls(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]api.MCPTTCall, len(calls))
	for i, c := range calls {
		out[i] = mcpttCall(c)
	}

	return out, nil
}

func (v mcpttView) Call(ctx context.Context, id string) (api.MCPTTCall, bool, error) {
	a, err := v.as()
	if err != nil {
		return api.MCPTTCall{}, false, err
	}

	c, ok, err := a.Call(ctx, id)
	if err != nil || !ok {
		return api.MCPTTCall{}, false, err
	}

	return mcpttCall(c), true, nil
}

func mcpttCall(c controlling.Call) api.MCPTTCall {
	out := api.MCPTTCall{
		ID: c.ID, SessionID: c.Session.String(), GroupID: c.Group.String(), Originator: c.Originator.String(),
		Created: c.Created, Started: c.Started, Ends: c.Ends, TalkingSince: c.TalkingSince,
	}

	if c.Talker.Host != "" {
		out.Talker = c.Talker.String()
	}

	for _, p := range c.Participants {
		out.Participants = append(out.Participants, api.MCPTTParticipant{
			MCPTTID: p.ID.String(), Joined: p.Joined, Required: p.Required, ReceiveOnly: p.ReceiveOnly,
		})
	}

	return out
}

func uriStrings(uris []sip.URI) []string {
	out := make([]string, len(uris))
	for i, u := range uris {
		out[i] = u.String()
	}

	return out
}

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

func (v mcpttView) CreateGroup(ctx context.Context, g mcpttsettings.Group, implicit mcpttsettings.Implicit) (mcpttsettings.Group, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, err
	}

	return l.CreateGroup(ctx, g, implicit)
}

func (v mcpttView) UpdateGroup(ctx context.Context, g mcpttsettings.Group, implicit mcpttsettings.Implicit) (mcpttsettings.Group, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, err
	}

	return l.UpdateGroup(ctx, g, implicit)
}

func (v mcpttView) DeleteGroup(ctx context.Context, uri string) error {
	l, err := v.live()
	if err != nil {
		return err
	}

	return l.DeleteGroup(ctx, uri)
}

func (v mcpttView) PutMember(ctx context.Context, group string, e mcpttsettings.Entry, implicit *bool) (mcpttsettings.Group, bool, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, false, err
	}

	return l.PutMember(ctx, group, e, implicit)
}

func (v mcpttView) DeleteMember(ctx context.Context, group, id string) (mcpttsettings.Group, error) {
	l, err := v.live()
	if err != nil {
		return mcpttsettings.Group{}, err
	}

	return l.DeleteMember(ctx, group, id)
}
