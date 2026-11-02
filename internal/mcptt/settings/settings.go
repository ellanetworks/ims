package settings

import (
	"time"

	"github.com/ellanetworks/ims/sip"
)

type Settings struct {
	System               System
	ServiceConfiguration ServiceConfiguration
	Users                []User
	Groups               []Group
}

// TS 24.379 clause 4.2
// TS 24.379 annex H
type System struct {
	ParticipatingPSI string
	ControllingPSI   string
}

type Source interface {
	Current() *Snapshot
}

// TS 24.484 clause 8.4.2.1
type ServiceConfiguration struct {
	Domain    string
	OnNetwork OnNetwork
}

// TS 24.484 clause 8.4.2.1
// TS 24.484 clause 8.4.2.7
type OnNetwork struct {
	NumLevelsPriorityHierarchy *int
	TransmitTime               TransmitTime
	FCTimersCounters           FCTimersCounters
}

type TransmitTime struct {
	TimeLimit *time.Duration
}

// TS 24.380 clause 11.1.3
// TS 24.380 clause 11.2.3
type FCTimersCounters struct {
	T1 *time.Duration
	T3 *time.Duration
	T7 *time.Duration
	T8 *time.Duration
	C7 *int
}

// TS 24.484 clause 8.3.2.1
type User struct {
	MCPTTUserID            string
	Alias                  string
	IMPUs                  []string
	ImplicitAffiliations   []string
	MaxAffiliationsN2      *int
	MaxSimultaneousCallsN6 *int

	id       sip.URI
	impus    []string
	implicit []*Group
}

// TS 24.481 clause 7.2.2
type Group struct {
	URI                     string
	DisplayName             string
	InviteMembers           bool
	MaxParticipants         int
	Encodings               []string
	T4                      *time.Duration
	TNG3                    time.Duration
	MinimumToStart          int
	TNG1                    time.Duration
	TNG1Action              Action
	MinimumAffiliated       int
	List                    List
	ReleaseOnInitiatorLeave bool

	id      sip.URI
	entries map[string]int
}

type Action string

const (
	// TS 24.379 clause 6.3.3.3
	Proceed Action = "proceed"
	// TS 24.379 clause 6.3.3.3
	Abandon Action = "abandon"
)

type List struct {
	Entries []Entry
}

// TS 24.481 clause 7.2.2
// TS 24.481 clause 7.2.7
type Entry struct {
	URI                 string
	UserPriority        int
	Required            bool
	ReceiveOnly         bool
	AffiliationRequired bool

	id sip.URI
}
