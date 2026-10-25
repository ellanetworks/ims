// Package settings are the settings of the MCPTT server that change at runtime: the public service identities of its
// functions, the MCPTT service configuration of its organisation (TS 24.484 §8.4), its users (the MCPTT user profile,
// TS 24.484 §8.3) and its groups (the MCPTT group document, TS 24.481 §7.2). One system and one organisation per
// instance [PM 8].
//
// The paths of the errors of New are the names of the elements: verbatim where the specification has them in kebab
// case (TS 24.481, the service configuration), in kebab case where it has them in camel case (the user profile).
// Only what the functions read is configured; an element the specification gives a default to may be left out. An
// optional element is a pointer, so an explicit zero is told from an absent element: New applies the defaults of the
// absent ones, once, and the snapshot has them all.
//
// A Snapshot is immutable: a call keeps the one it started with, and a change is for the calls and requests after it.
// A management server (GMS, CMS) builds its documents from the same types.
package settings

import (
	"time"

	"github.com/ellanetworks/ims/sip"
)

// Settings are the settings as written: the document New validates.
type Settings struct {
	System               System
	ServiceConfiguration ServiceConfiguration
	Users                []User
	Groups               []Group
}

// System are the public service identities of the functions (TS 24.379 §4.2, Annex H).
type System struct {
	// ParticipatingPSI is the public service identity of the participating function (TS 24.379 §4.2 1c): the
	// Request-URI of its clients' requests, the <Server-URI> of their <MCPTT-Service-Details> (TS 24.484 §7.2.2.7
	// 10), and the P-Asserted-Identity of what it sends them (Table H.3-1).
	ParticipatingPSI string
	// ControllingPSI is the public service identity of the controlling function of the groups (TS 24.379 §6.3.1.1,
	// Table H.2-1): the address it sends from.
	ControllingPSI string
}

// Source is where the functions read the settings: Current, for each new call or request, which keeps the snapshot
// it started with.
type Source interface {
	Current() *Snapshot
}

// ServiceConfiguration is the <service-configuration-params> document of the organisation (TS 24.484 §8.4.2.1).
type ServiceConfiguration struct {
	// Domain is the "domain" attribute: the domain name of the mission critical organisation (§8.4.2.7), of the
	// registrar in direct mode [PM 14].
	Domain    string
	OnNetwork OnNetwork
}

// OnNetwork is the <on-network> element (TS 24.484 §8.4.2.1, §8.4.2.7).
type OnNetwork struct {
	// NumLevelsPriorityHierarchy is <num-levels-priority-hierarchy>, 4 to 256; 4, the lowest, if absent (§8.4.2.7
	// 6). It bounds the floor priority a participant is granted (TS 24.380 §14.3.3).
	NumLevelsPriorityHierarchy *int
	TransmitTime               TransmitTime
	FCTimersCounters           FCTimersCounters
}

// TransmitTime is the <transmit-time> element.
type TransmitTime struct {
	// TimeLimit is <time-limit>, the transmit time limit (§8.4.2.7 7): T2 (Stop talking) of TS 24.380 Table 11.1.3-1,
	// its default if absent.
	TimeLimit *time.Duration
}

// FCTimersCounters is the <fc-timers-counters> element: the floor control server's timers and counters of TS 24.380
// Table 11.1.3-1 and Table 11.2.3-1 that S1 runs, their defaults of the tables (floor.DefaultServerTimers) if absent;
// T7, which has none, DefaultT7.
type FCTimersCounters struct {
	T1 *time.Duration
	T3 *time.Duration
	T7 *time.Duration
	T8 *time.Duration
	C7 *int
}

// User is an MCPTT user: of its user profile (TS 24.484 §8.3.2.1), what the server reads, and its IMS public user
// identities.
type User struct {
	// MCPTTUserID is the <uri-entry> of <MCPTTUserID>: the MCPTT ID (§8.3.2.7), a SIP URI.
	MCPTTUserID string
	// Alias is the <alias-entry> of <UserAlias>: the alphanumeric alias of the user (§8.3.2.7), its name for people;
	// none if empty. The functions do not read it.
	Alias string
	// IMPUs are the IMS public user identities the user may be served at (TS 23.003 §13.4), SIP or tel URIs, at least
	// one; an IMPU may be of several users (TS 23.280 §8.3.1). They stand in for the validation of the access token
	// until an identity management server issues tokens [PM 4]: in S1 the MCPTT ID comes from an access token that is
	// not validated, so service authorisation binds it only to an IMPU of the user's (TS 24.379 §7.3.2 step 4). Once
	// tokens are validated (M4), the IMPU of the third-party REGISTER is bound to the MCPTT ID of the token (TS 33.180
	// §5.1.3.2.2 steps 9 to 11), the server keeping that mapping (TS 23.280 §8.3.1), and the configured IMPUs go.
	IMPUs []string
	// ImplicitAffiliations are the <entry> elements of <ImplicitAffiliations>: the groups the user is affiliated to
	// on service authorisation (§8.3.2.7; TS 24.379 §9.2.2.2.15), groups it is a member of.
	ImplicitAffiliations []string
	// MaxAffiliationsN2 is <MaxAffiliationsN2>, N2: the most groups the user may be affiliated to at once (TS
	// 24.379 §3.1, §9.2.2.2.12 step 9), a nonNegativeInteger (TS 24.484 §8.3.2.1): 0 for none. As many as it is a
	// member of if absent, a local default.
	MaxAffiliationsN2 *int
	// MaxSimultaneousCallsN6 is <MaxSimultaneousCallsN6> of <MCPTT-group-call> of <Common>, N6: "the maximum number of
	// simultaneously received MCPTT group calls" (TS 24.484 §8.3.2.7), which the participating function holds the
	// user's calls to (TS 24.379 §10.1.1.3.1.1 step 5), a positiveInteger. The profile requires it (§8.3.2.1 8 e i;
	// TS 24.483 §5.2.31), without a default; absent, by local default, no limit.
	MaxSimultaneousCallsN6 *int

	id sip.URI
	// impus are the keys of the IMPUs.
	impus    []string
	implicit []*Group
}

// Group is the <list-service> element of an MCPTT group document (TS 24.481 §7.2.2) and the server's local policy
// for the group.
type Group struct {
	// URI is the "uri" attribute: the MCPTT group ID (§7.2.7), a SIP URI.
	URI string
	// DisplayName is <display-name>: the group name (TS 24.481 §7.2.2); none if empty. The functions do not read it.
	DisplayName string
	// InviteMembers is <on-network-invite-members>: true for a prearranged group, false, the default, for a chat
	// group (§7.2.7), not supported yet.
	InviteMembers bool
	// MaxParticipants is <on-network-max-participant-count>: the maximum number of participants in a group session
	// (TS 24.481 §7.2.8), which bounds the members invited to a call (TS 24.379 §6.3.5.5), at least 2 and the number
	// of required members; zero, if absent, for no limit.
	MaxParticipants int
	// Encodings are the "name" attributes of the <encoding> elements of <preferred-voice-encodings>, in order of
	// preference: encoding names of a=rtpmap (§7.2.2). AMR-WB, mandatory for the clients (TS 26.179 §4.1.1), if
	// absent. A call relays one of them.
	Encodings []string
	// T4 is <on-network-hang-timer>: T4 (Inactivity) of TS 24.380 Table 11.1.3-1, whose expiry releases the call
	// (TS 24.379 §6.3.8.1 1); the table's default if absent.
	T4 *time.Duration
	// TNG3 is <on-network-maximum-duration>: the group call timer (TS 24.379 §6.3.3.5, Table B.2.1-1), required for
	// a prearranged group (§7.2.7).
	TNG3 time.Duration
	// MinimumToStart is <on-network-minimum-number-to-start>: how many invited members acknowledge before the call
	// starts (TS 24.379 §6.3.3.3); zero, if absent, for none.
	MinimumToStart int
	// TNG1 is <on-network-timeout-for-acknowledgement-of-required-members>: the acknowledged call setup timer (TS
	// 24.379 §6.3.3.3, Table B.2.1-1), and TNG1Action
	// <on-network-action-upon-expiration-of-timeout-for-acknowledgement-of-required-members>, what its expiry does.
	// Both required if a member is required.
	TNG1       time.Duration
	TNG1Action Action
	// MinimumAffiliated is <on-network-minimum-number-of-affiliated-members>: fewer affiliated members, and a call
	// is not set up (TS 24.379 §10.1.1.4.2) or is released (§6.3.8.1 4); zero, if absent, for none.
	MinimumAffiliated int
	List              List
	// ReleaseOnInitiatorLeave is the local policy of TS 24.379 §6.3.8.1 3: the call is released when its initiator
	// leaves. Not of the group document.
	ReleaseOnInitiatorLeave bool

	id      sip.URI
	entries map[string]int
}

// Action is a value of <on-network-action-upon-expiration-of-timeout-for-acknowledgement-of-required-members> (TS
// 24.481 Table 7.2.2-1).
type Action string

const (
	// Proceed: the call proceeds without the required members (TS 24.379 §6.3.3.3 1).
	Proceed Action = "proceed"
	// Abandon: the call is abandoned (TS 24.379 §6.3.3.3 2).
	Abandon Action = "abandon"
)

// List is the <list> element of the group's members.
type List struct {
	Entries []Entry
}

// Entry is an <entry> element of the list: a member (TS 24.481 §7.2.2, §7.2.7).
type Entry struct {
	// URI is the "uri" attribute: the member's MCPTT ID, of a configured user.
	URI string
	// UserPriority is <user-priority>, 0 to 255, higher the higher; 0, the lowest, if absent. It bounds the floor
	// priority the member is granted (TS 24.380 §14.2.3, §14.3.3).
	UserPriority int
	// Required is <on-network-required>: a required member, whose acknowledgement TNG1 waits for (TS 24.379
	// §6.3.3.3).
	Required bool
	// ReceiveOnly is <on-network-recvonly>: the member may not send media, nor is granted a floor priority (TS
	// 24.380 §14.3.3).
	ReceiveOnly bool
	// AffiliationRequired is <on-network-affiliation-to-group-required>: unless the member is affiliated, a call is
	// not set up (TS 24.379 §10.1.1.4.2).
	AffiliationRequired bool

	id sip.URI
}
