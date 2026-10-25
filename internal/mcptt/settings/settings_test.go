package settings

import (
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/sip"
)

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// valid are settings New accepts, of which each case of TestInvalid changes a part.
func valid() Settings {
	return Settings{
		System: System{ParticipatingPSI: "sip:pf@example.com", ControllingPSI: "sip:cf@example.com"},
		ServiceConfiguration: ServiceConfiguration{
			Domain:    "example.com",
			OnNetwork: OnNetwork{FCTimersCounters: FCTimersCounters{T7: ptr(time.Second)}},
		},
		Users: []User{
			{MCPTTUserID: "sip:a@example.com", IMPUs: []string{"sip:a@ims.example.com"}},
			{MCPTTUserID: "sip:b@example.com", IMPUs: []string{"sip:b@ims.example.com"}},
		},
		Groups: []Group{{
			URI: "sip:g@example.com", InviteMembers: true, TNG3: time.Minute,
			List: List{Entries: []Entry{{URI: "sip:a@example.com"}, {URI: "sip:b@example.com"}}},
		}},
	}
}

func TestValid(t *testing.T) {
	s, err := New(valid())
	if err != nil {
		t.Fatal(err)
	}

	// The defaults of TS 24.484 §8.4.2.7 and TS 26.179 §4.1.1.
	g := s.Group(mustURI(t, "sip:g@example.com"))
	if *s.ServiceConfiguration().OnNetwork.NumLevelsPriorityHierarchy != 4 || len(g.Encodings) != 1 || g.Encodings[0] != "AMR-WB" {
		t.Errorf("defaults: %+v, %v", s.ServiceConfiguration().OnNetwork, g.Encodings)
	}

	// Those of TS 24.380 Table 11.1.3-1 and Table 11.2.3-1.
	want := floor.DefaultServerTimers
	want.T7 = time.Second

	if timers := s.ServerTimers(g); timers != want {
		t.Errorf("server timers %+v", timers)
	}

	// T7, of no default of the table, the server's.
	c := valid()
	c.ServiceConfiguration.OnNetwork.FCTimersCounters.T7 = nil

	if s, err := New(c); err != nil || s.ServerTimers(s.Group(mustURI(t, "sip:g@example.com"))).T7 != DefaultT7 {
		t.Errorf("T7 of none: %v", err)
	}

	if n, limited := s.User(mustURI(t, "sip:a@example.com")).N2(); limited {
		t.Errorf("N2 %d of a user without", n)
	}

	if n, limited := s.User(mustURI(t, "sip:a@example.com")).N6(); limited {
		t.Errorf("N6 %d of a user without", n)
	}

	// Identities in their canonical form (TS 23.003 §13.4): of the user and host only, the user part as it is.
	for _, id := range []string{"tel:+1", "sip:A@example.com", "sip:a@example.org"} {
		if s.User(mustURI(t, id)) != nil {
			t.Errorf("user %s found", id)
		}
	}

	for _, id := range []string{"sip:a@EXAMPLE.com:5060;transport=tcp", "sips:a@example.com", "sip:a@example.com?x=y"} {
		if s.User(mustURI(t, id)) == nil {
			t.Errorf("user %s not found", id)
		}
	}

	// RFC 3261 §19.1.4: escapes of unreserved characters are the characters, of reserved ones not.
	if u := s.User(mustURI(t, "sip:%61@example.com;transport=tcp")); u == nil || u.ID().String() != "sip:a@example.com" {
		t.Errorf("escaped user: %+v", u)
	}

	if g.List.Entries[1].ID().String() != "sip:b@example.com" {
		t.Errorf("entry ID %s", g.List.Entries[1].ID())
	}
}

// An explicit zero is not an absent element: T3 of a group with audio cut-in, C7 of one Floor Idle, T4 releasing the
// call at once (TS 24.380 Table 11.1.3-1, Table 11.2.3-1), N2 of no groups (TS 24.484 §8.3.2.1).
func TestZeros(t *testing.T) {
	c := valid()
	fc := &c.ServiceConfiguration.OnNetwork.FCTimersCounters
	fc.T3, fc.C7 = ptr(time.Duration(0)), ptr(0)
	c.Groups[0].T4 = ptr(time.Duration(0))
	c.Users[0].MaxAffiliationsN2 = ptr(0)
	c.Users[1].ImplicitAffiliations = []string{"sip:g@example.com"}

	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}

	timers := s.ServerTimers(s.Group(mustURI(t, "sip:g@example.com")))
	if timers.T3 != 0 || timers.C7 != 0 || timers.T4 != 0 {
		t.Errorf("server timers %+v", timers)
	}

	if n, limited := s.User(mustURI(t, "sip:a@example.com")).N2(); n != 0 || !limited {
		t.Errorf("N2 %d %v", n, limited)
	}

	if gs := s.User(mustURI(t, "sip:b@example.com")).ImplicitGroups(); len(gs) != 1 || gs[0].URI != "sip:g@example.com" {
		t.Errorf("implicit groups %v", gs)
	}
}

// RFC 3966 §4: tel URIs are compared without their visual separators, their parameters in any order.
func TestTelIMPU(t *testing.T) {
	c := valid()
	c.Users[0].IMPUs = []string{"tel:+1-555-0100;phone-context=+1;ext=1"}

	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}

	if !s.User(mustURI(t, "sip:a@example.com")).HasIMPU(mustURI(t, "tel:+1(555)0100;EXT=1;phone-context=+1")) {
		t.Error("tel IMPU not found")
	}

	c.Users[0].IMPUs = []string{"tel:+1-555-0100", "tel:+15550100"}
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "users[0].impus[1]: tel:+15550100 is listed twice") {
		t.Errorf("tel IMPU twice: %v", err)
	}
}

// New takes a copy: a change of the settings after it does not change the snapshot.
func TestNewCopies(t *testing.T) {
	c := valid()

	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}

	c.Groups[0].List.Entries[0].ReceiveOnly = true
	c.Users[0].IMPUs[0] = "sip:x@example.com"

	if s.Group(mustURI(t, "sip:g@example.com")).List.Entries[0].ReceiveOnly ||
		!s.User(mustURI(t, "sip:a@example.com")).HasIMPU(mustURI(t, "sip:a@ims.example.com")) {
		t.Error("snapshot changed")
	}
}

func TestInvalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Settings)
		want   []string
	}{
		{
			"no PSIs", func(c *Settings) { c.System.ParticipatingPSI, c.System.ControllingPSI = "", "" },
			[]string{"system.participating-psi: is required", "system.controlling-psi: is required"},
		},
		{"PSI not a URI", func(c *Settings) { c.System.ParticipatingPSI = "pf" }, []string{"system.participating-psi: URI"}},
		{
			"PSI with parameters", func(c *Settings) { c.System.ControllingPSI = "sip:cf@example.com;transport=tcp" },
			[]string{"system.controlling-psi: \"sip:cf@example.com;transport=tcp\" is not a SIP URI without parameters"},
		},
		{"PSI tel", func(c *Settings) { c.System.ControllingPSI = "tel:+1" }, []string{"system.controlling-psi: \"tel:+1\" is not a SIP URI"}},
		{
			"same PSIs", func(c *Settings) { c.System.ControllingPSI = "sip:pf@EXAMPLE.com" },
			[]string{"system.controlling-psi: sip:pf@EXAMPLE.com is also the participating PSI"},
		},
		{
			"domain", func(c *Settings) { c.ServiceConfiguration.Domain = "-x.example.com" },
			[]string{"service-configuration.domain: \"-x.example.com\" is not a domain name"},
		},
		{
			"priority levels", func(c *Settings) { c.ServiceConfiguration.OnNetwork.NumLevelsPriorityHierarchy = ptr(3) },
			[]string{"service-configuration.on-network.num-levels-priority-hierarchy: 3 is not within 4 and 256"},
		},
		{
			"negative T2", func(c *Settings) { c.ServiceConfiguration.OnNetwork.TransmitTime.TimeLimit = ptr(-time.Second) },
			[]string{"service-configuration.on-network.transmit-time.time-limit: -1s is negative"},
		},
		{
			"T1 above its maximum", func(c *Settings) { c.ServiceConfiguration.OnNetwork.FCTimersCounters.T1 = ptr(7 * time.Second) },
			[]string{"service-configuration.on-network.fc-timers-counters.T1-end-of-rtp-media: 7s is above its maximum of 6s"},
		},
		{
			"T1 below the server's minimum", func(c *Settings) { c.ServiceConfiguration.OnNetwork.FCTimersCounters.T1 = ptr(500 * time.Millisecond) },
			[]string{"service-configuration.on-network.fc-timers-counters.T1-end-of-rtp-media: 500ms is below 1s"},
		},
		{
			"C7", func(c *Settings) { c.ServiceConfiguration.OnNetwork.FCTimersCounters.C7 = ptr(65536) },
			[]string{"service-configuration.on-network.fc-timers-counters.C7-floor-idle: 65536 is not an unsignedShort"},
		},
		{"zero timers", func(c *Settings) {
			on := &c.ServiceConfiguration.OnNetwork
			on.TransmitTime.TimeLimit = ptr(time.Duration(0))
			on.FCTimersCounters.T1, on.FCTimersCounters.T7, on.FCTimersCounters.T8 = ptr(time.Duration(0)), ptr(time.Duration(0)), ptr(time.Duration(0))
		}, []string{
			"service-configuration.on-network.transmit-time.time-limit: is zero",
			"service-configuration.on-network.fc-timers-counters.T1-end-of-rtp-media: is zero",
			"service-configuration.on-network.fc-timers-counters.T7-floor-idle: is zero",
			"service-configuration.on-network.fc-timers-counters.T8-floor-revoke: is zero",
		}},
		{
			"IMPU as a PSI", func(c *Settings) { c.Users[0].IMPUs = []string{"sip:pf@example.com"} },
			[]string{"users[0].impus[0]: sip:pf@example.com is also the participating PSI"},
		},
		{
			"group as an IMPU", func(c *Settings) { c.Users[0].IMPUs = []string{"sip:g@example.com"} },
			[]string{"groups[0].uri: sip:g@example.com is also an IMPU of sip:a@example.com"},
		},
		{"IMPU with headers", func(c *Settings) { c.Users[0].IMPUs = []string{"sip:a@ims.example.com?x=y"} }, []string{"users[0].impus[0]: \"sip:a@ims.example.com?x=y\" is not a SIP or tel URI"}},
		{"no MCPTT ID", func(c *Settings) { c.Users[1].MCPTTUserID = "" }, []string{"users[1].mcptt-user-id: is required", "groups[0].list.entries[1].uri: sip:b@example.com is not a configured user"}},
		{
			"user twice", func(c *Settings) { c.Users[1].MCPTTUserID = "sip:a@EXAMPLE.com" },
			[]string{"users[1].mcptt-user-id: sip:a@EXAMPLE.com is also the user sip:a@example.com", "groups[0].list.entries[1].uri: sip:b@example.com is not a configured user"},
		},
		{
			"user as a PSI", func(c *Settings) { c.Users[1].MCPTTUserID = "sip:pf@example.com" },
			[]string{"users[1].mcptt-user-id: sip:pf@example.com is also the participating PSI", "groups[0].list.entries[1].uri: sip:b@example.com is not a configured user"},
		},
		{"no IMPU", func(c *Settings) { c.Users[0].IMPUs = nil }, []string{"users[0].impus: needs at least one IMPU"}},
		{
			"IMPU", func(c *Settings) {
				c.Users[0].IMPUs = []string{"sip:a@ims.example.com", "mailto:a@example.com", "sip:a@IMS.example.com"}
			},
			[]string{"users[0].impus[1]: \"mailto:a@example.com\" is not a SIP or tel URI", "users[0].impus[2]: sip:a@IMS.example.com is listed twice"},
		},
		{"N2", func(c *Settings) { c.Users[0].MaxAffiliationsN2 = ptr(-1) }, []string{"users[0].max-affiliations-n2: -1 is negative"}},
		// TS 24.484 §8.3.2.1: a positiveInteger.
		{"N6", func(c *Settings) { c.Users[0].MaxSimultaneousCallsN6 = ptr(0) }, []string{"users[0].max-simultaneous-calls-n6: 0 is not positive"}},
		{"implicit affiliations", func(c *Settings) {
			c.Groups = append(c.Groups, Group{
				URI: "sip:h@example.com", InviteMembers: true, TNG3: time.Minute,
				List: List{Entries: []Entry{{URI: "sip:b@example.com"}}},
			})
			c.Users[0].ImplicitAffiliations = []string{"sip:g@example.com", "sip:x@example.com", "sip:g@example.com", "sip:h@example.com", "x"}
			c.Users[0].MaxAffiliationsN2 = ptr(1)
		}, []string{
			"users[0].implicit-affiliations[1]: sip:x@example.com is not a configured group",
			"users[0].implicit-affiliations[2]: sip:g@example.com is listed twice",
			"users[0].implicit-affiliations[3]: sip:h@example.com is a group sip:a@example.com is not a member of",
			"users[0].implicit-affiliations[4]: URI",
			"users[0].implicit-affiliations: 5 groups, more than max-affiliations-n2 1",
		}},
		{
			"group as a user", func(c *Settings) { c.Groups[0].URI = "sip:b@example.com" },
			[]string{"groups[0].uri: sip:b@example.com is also the user sip:b@example.com"},
		},
		{
			"chat group", func(c *Settings) { c.Groups[0].InviteMembers = false },
			[]string{"groups[0].on-network-invite-members: is false, of a chat group"},
		},
		{"no TNG3", func(c *Settings) { c.Groups[0].TNG3 = 0 }, []string{"groups[0].on-network-maximum-duration: is required of a prearranged group"}},
		{"negative T4", func(c *Settings) { c.Groups[0].T4 = ptr(-time.Second) }, []string{"groups[0].on-network-hang-timer: -1s is negative"}},
		{
			"encodings", func(c *Settings) { c.Groups[0].Encodings = []string{"AMR-WB", "AMR-WB/16000", "amr-wb"} },
			[]string{"groups[0].preferred-voice-encodings[1]: \"AMR-WB/16000\" is not an encoding name", "groups[0].preferred-voice-encodings[2]: amr-wb is listed twice"},
		},
		{"entries", func(c *Settings) {
			c.Groups[0].List.Entries = []Entry{
				{URI: "sip:x@example.com"},
				{URI: ""},
				{URI: "sip:a@example.com", UserPriority: 256},
				{URI: "sip:a@example.com"},
				{URI: "sip:b@example.com;user=phone"},
			}
		}, []string{
			"groups[0].list.entries[0].uri: sip:x@example.com is not a configured user",
			"groups[0].list.entries[1].uri: is required",
			"groups[0].list.entries[2].user-priority: 256 is not within 0 and 255",
			"groups[0].list.entries[3].uri: sip:a@example.com is listed twice",
			"groups[0].list.entries[4].uri: \"sip:b@example.com;user=phone\" is not a SIP URI without parameters",
		}},
		{
			"one participant", func(c *Settings) { c.Groups[0].MaxParticipants = 1 },
			[]string{"groups[0].on-network-max-participant-count: 1: a call of fewer than 2 participants is released"},
		},
		{"fewer participants than required", func(c *Settings) {
			c.Groups[0].MaxParticipants = 2
			c.Groups[0].TNG1, c.Groups[0].TNG1Action = time.Second, Abandon
			c.Users = append(c.Users, User{MCPTTUserID: "sip:c@example.com", IMPUs: []string{"sip:c@ims.example.com"}})
			c.Groups[0].List.Entries = []Entry{{URI: "sip:a@example.com", Required: true}, {URI: "sip:b@example.com", Required: true}, {URI: "sip:c@example.com", Required: true}}
		}, []string{"groups[0].on-network-max-participant-count: 2 is fewer than the 3 required members"}},
		{
			"minimum to start", func(c *Settings) { c.Groups[0].MinimumToStart = 2 },
			[]string{"groups[0].on-network-minimum-number-to-start: 2 is not within 0 and the 1 members to invite"},
		},
		{
			"minimum affiliated", func(c *Settings) { c.Groups[0].MinimumAffiliated = 3 },
			[]string{"groups[0].on-network-minimum-number-of-affiliated-members: 3 is not within 0 and the 2 members"},
		},
		{"required members without TNG1", func(c *Settings) { c.Groups[0].List.Entries[1].Required = true }, []string{
			"groups[0].on-network-timeout-for-acknowledgement-of-required-members: is required with required members",
			"groups[0].on-network-action-upon-expiration-of-timeout-for-acknowledgement-of-required-members: is required with required members",
		}},
		{
			"TNG1 action", func(c *Settings) { c.Groups[0].TNG1Action = "retry" },
			[]string{"groups[0].on-network-action-upon-expiration-of-timeout-for-acknowledgement-of-required-members: \"retry\" is neither"},
		},
		{
			"group twice", func(c *Settings) { c.Groups = append(c.Groups, c.Groups[0]) },
			[]string{"groups[1].uri: sip:g@example.com is also the group sip:g@example.com"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.change(&c)

			_, err := New(c)
			if err == nil {
				t.Fatal("accepted")
			}

			lines := strings.Split(err.Error(), "\n")
			if len(lines) != len(tc.want) {
				t.Errorf("%d errors, want %d:\n%v", len(lines), len(tc.want), err)
			}

			for _, w := range tc.want {
				found := false

				for _, l := range lines {
					found = found || strings.HasPrefix(l, w)
				}

				if !found {
					t.Errorf("no %q in:\n%v", w, err)
				}
			}
		})
	}
}
