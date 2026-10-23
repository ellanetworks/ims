package mcptt

import (
	"context"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
)

// exampleSettings are alice, bob and carol, members of fire, affiliated to it on registration.
func exampleSettings() settings.Settings {
	const fire = "sip:fire@mcptt.example.com"

	return settings.Settings{
		System: settings.System{
			ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com", ControllingPSI: "sip:mcptt-controlling@mcptt.example.com",
		},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    "mcptt.example.com",
			OnNetwork: settings.OnNetwork{FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)}},
		},
		Users: []settings.User{
			{MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:alice@ims.example.com"}, ImplicitAffiliations: []string{fire}},
			{MCPTTUserID: "sip:bob@mcptt.example.com", IMPUs: []string{"sip:bob@ims.example.com"}, ImplicitAffiliations: []string{fire}},
			{MCPTTUserID: "sip:carol@mcptt.example.com", IMPUs: []string{"sip:carol@ims.example.com"}, ImplicitAffiliations: []string{fire}},
		},
		Groups: []settings.Group{{
			URI: fire, InviteMembers: true, TNG3: 10 * time.Minute,
			List: settings.List{Entries: []settings.Entry{
				{URI: "sip:alice@mcptt.example.com"}, {URI: "sip:bob@mcptt.example.com"}, {URI: "sip:carol@mcptt.example.com"},
			}},
		}},
	}
}

// provision changes l to s as the API would: the system and service configuration, the users without their
// implicit affiliations, the groups, then the users' implicit affiliations.
func provision(ctx context.Context, l *settings.Live, s settings.Settings) error {
	if err := l.UpdateSystem(ctx, s.System); err != nil {
		return err
	}

	if err := l.UpdateServiceConfiguration(ctx, s.ServiceConfiguration); err != nil {
		return err
	}

	for _, u := range s.Users {
		u.ImplicitAffiliations = nil
		if _, err := l.CreateUser(ctx, u); err != nil {
			return err
		}
	}

	for _, g := range s.Groups {
		if _, err := l.CreateGroup(ctx, g); err != nil {
			return err
		}
	}

	for _, u := range s.Users {
		if len(u.ImplicitAffiliations) == 0 {
			continue
		}

		if _, err := l.UpdateUser(ctx, u); err != nil {
			return err
		}
	}

	return nil
}
