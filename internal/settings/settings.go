// Package settings holds the IMS settings that live in the database and change at runtime, over the API.
package settings

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// Settings are the settings of the IMS in the database. Their slices are never modified in place, so that a copy
// can be read while the next is made.
type Settings struct {
	Operator Operator
	// Peers are in the order they were created in.
	Peers  []Peer
	Policy Policy
}

type Operator struct {
	MCC       string
	MNC       string
	Numbering Numbering
}

// Numbering is how dial strings become E.164 numbers.
type Numbering struct {
	CountryCode         string
	NationalPrefix      string
	InternationalPrefix string
}

// HomeDomain is the home network domain name of the PLMN, as a UE without an ISIM derives it from its IMSI
// (TS 23.003 §13.2). The names of the IMS are all derived from it.
func (o Operator) HomeDomain() string {
	mnc := o.MNC
	if len(mnc) == 2 {
		mnc = "0" + mnc
	}

	return "ims.mnc" + mnc + ".mcc" + o.MCC + ".3gppnetwork.org"
}

// DiameterHost is the Origin-Host of the IMS. TS 23.003 has no operator subdomain under the IMS domain, unlike
// node.epc (§19.4.2.8) and node.5gc (§28.17), so the host is directly under the home domain.
func (o Operator) DiameterHost() string {
	return "ims." + o.HomeDomain()
}

// DiameterRealm is the Origin-Realm of the IMS.
func (o Operator) DiameterRealm() string {
	return o.HomeDomain()
}

func (o Operator) PCSCFHost() string {
	return "pcscf." + o.HomeDomain()
}

func (o Operator) SCSCFHost() string {
	return "scscf." + o.HomeDomain()
}

// SCSCFName is the SIP URI of the S-CSCF on a port, the Server-Name it gives the HSS.
func (o Operator) SCSCFName(port int) string {
	return "sip:" + o.SCSCFHost() + ":" + strconv.Itoa(port)
}

// SIPAliases are the host names the IMS answers to.
func (o Operator) SIPAliases() []string {
	return []string{o.HomeDomain(), o.PCSCFHost(), o.SCSCFHost()}
}

// SameIdentity reports whether two operators give the IMS the same names, so that changing from one to the other
// needs no restart.
func (o Operator) SameIdentity(p Operator) bool {
	return o.HomeDomain() == p.HomeDomain()
}

// SameCore reports whether two settings run the same Diameter, policy function and SIP, and only differ in what
// those read when they use it, such as the numbering plan.
func (s Settings) SameCore(t Settings) bool {
	return s.Operator.SameIdentity(t.Operator) && slices.EqualFunc(s.Peers, t.Peers, Peer.equal) && s.Policy == t.Policy
}

func (s Settings) Validate() error {
	if err := s.Operator.Validate(); err != nil {
		return err
	}

	if err := s.validatePeers(); err != nil {
		return err
	}

	if err := s.Policy.Validate(); err != nil {
		return err
	}

	if _, ok := s.PeerServing(ApplicationRx); s.Policy.Interface == PolicyRx && !ok {
		return conflictf("rx requires a Diameter peer serving rx")
	}

	return nil
}

func (o Operator) Validate() error {
	switch {
	case !isDigits(o.MCC) || len(o.MCC) != 3:
		return invalidf("mcc must be 3 digits")
	case !isDigits(o.MNC) || len(o.MNC) < 2 || len(o.MNC) > 3:
		return invalidf("mnc must be 2 or 3 digits")
	}

	return o.Numbering.Validate()
}

func (n Numbering) Validate() error {
	switch {
	case !isDigits(n.CountryCode) || len(n.CountryCode) > 3 || n.CountryCode[0] == '0':
		return invalidf("numbering.country_code must be 1 to 3 digits, not starting with 0")
	case !isPrefix(n.NationalPrefix):
		return invalidf("numbering.national_prefix must be up to 4 digits")
	case !isPrefix(n.InternationalPrefix):
		return invalidf("numbering.international_prefix must be up to 4 digits")
	case n.NationalPrefix != "" && n.NationalPrefix == n.InternationalPrefix:
		return invalidf("numbering.national_prefix and numbering.international_prefix must differ")
	}

	return nil
}

type Store interface {
	UpdateOperator(ctx context.Context, o Operator) error
	CreatePeer(ctx context.Context, p Peer) error
	UpdatePeer(ctx context.Context, p Peer) error
	DeletePeer(ctx context.Context, id string) error
	UpdatePolicy(ctx context.Context, p Policy) error
}

// Live serves the current settings to the running IMS and persists changes before they take effect.
type Live struct {
	store Store
	check func(Settings) error

	current atomic.Pointer[Settings]

	mu      sync.Mutex
	changed chan struct{}
}

// NewLive serves settings from a store. Check, if any, makes the checks the settings cannot make themselves, such
// as against the configuration file, on every change.
func NewLive(store Store, initial Settings, check func(Settings) error) *Live {
	l := &Live{store: store, check: check, changed: make(chan struct{})}
	l.current.Store(&initial)

	return l
}

// Get returns the current settings. Callers must not modify its slices.
func (l *Live) Get() Settings {
	return *l.current.Load()
}

func (l *Live) UpdateOperator(ctx context.Context, o Operator) error {
	return l.change(func(next *Settings) error {
		next.Operator = o
		return nil
	}, func() error { return l.store.UpdateOperator(ctx, o) })
}

// CreatePeer adds a peer, and returns it with the ID it was given.
func (l *Live) CreatePeer(ctx context.Context, p Peer) (Peer, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Peer{}, fmt.Errorf("peer ID: %w", err)
	}

	p.ID = id.String()

	err = l.change(func(next *Settings) error {
		next.Peers = append(slices.Clone(next.Peers), p)
		return nil
	}, func() error { return l.store.CreatePeer(ctx, p) })

	return p, err
}

// UpdatePeer replaces the peer of the same ID.
func (l *Live) UpdatePeer(ctx context.Context, p Peer) error {
	return l.change(func(next *Settings) error {
		i, ok := next.peer(p.ID)
		if !ok {
			return kindError{ErrNotFound, "Diameter peer not found"}
		}

		next.Peers = slices.Clone(next.Peers)
		next.Peers[i] = p

		return nil
	}, func() error { return l.store.UpdatePeer(ctx, p) })
}

func (l *Live) DeletePeer(ctx context.Context, id string) error {
	return l.change(func(next *Settings) error {
		i, ok := next.peer(id)
		if !ok {
			return kindError{ErrNotFound, "Diameter peer not found"}
		}

		if next.Peers[i].Serves(ApplicationRx) && next.Policy.Interface == PolicyRx {
			return conflictf("Policy uses this peer")
		}

		next.Peers = slices.Delete(slices.Clone(next.Peers), i, i+1)

		return nil
	}, func() error { return l.store.DeletePeer(ctx, id) })
}

func (l *Live) UpdatePolicy(ctx context.Context, p Policy) error {
	return l.change(func(next *Settings) error {
		next.Policy = p
		return nil
	}, func() error { return l.store.UpdatePolicy(ctx, p) })
}

// change makes the next settings with edit, checks them, then saves and applies them.
func (l *Live) change(edit func(next *Settings) error, save func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	next := *l.current.Load()

	if err := edit(&next); err != nil {
		return err
	}

	if err := next.Validate(); err != nil {
		return err
	}

	if l.check != nil {
		if err := l.check(next); err != nil {
			return err
		}
	}

	if err := save(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	l.current.Store(&next)

	close(l.changed)
	l.changed = make(chan struct{})

	return nil
}

// Changed returns a channel that is closed on the next successful update.
func (l *Live) Changed() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.changed
}

func isPrefix(s string) bool {
	return s == "" || (isDigits(s) && len(s) <= 4)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
