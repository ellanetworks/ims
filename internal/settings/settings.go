package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

type Settings struct {
	Operator    Operator
	Peers       []Peer
	Routes      []Route
	Policy      Policy
	CallRecords CallRecords
	IPSMGW      IPSMGW
	SMSRoutes   []SMSRoute
	Trunks      []Trunk
	SIPRoutes   []SIPRoute
}

type Operator struct {
	MCC       string
	MNC       string
	Numbering Numbering
}

type Numbering struct {
	CountryCode         string
	NationalPrefix      string
	InternationalPrefix string
	MinNationalDigits   int
}

const (
	DefaultMinNationalDigits = 7
	MaxMinNationalDigits     = 15
)

// TS 23.003 clause 13.2
func (o Operator) HomeDomain() string {
	mnc := o.MNC
	if len(mnc) == 2 {
		mnc = "0" + mnc
	}

	return "ims.mnc" + mnc + ".mcc" + o.MCC + ".3gppnetwork.org"
}

const DefaultDiameterLabel = "ims"

func (o Operator) DiameterHost(label string) string {
	if label == "" {
		label = DefaultDiameterLabel
	}

	return label + "." + o.HomeDomain()
}

const maxLabelLength = 63

// RFC 1035 section 2.3.1
// RFC 1123 section 2.1
func ValidLabel(label string) error {
	if label == "" || len(label) > maxLabelLength {
		return fmt.Errorf("must be 1 to %d characters", maxLabelLength)
	}

	if label[0] == '-' || label[len(label)-1] == '-' {
		return errors.New("must not start or end with a hyphen")
	}

	for _, c := range label {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return errors.New("must contain only lowercase letters, digits and hyphens")
		}
	}

	return nil
}

func (o Operator) DiameterRealm() string {
	return o.HomeDomain()
}

func (o Operator) PCSCFHost() string {
	return "pcscf." + o.HomeDomain()
}

func (o Operator) SCSCFHost() string {
	return "scscf." + o.HomeDomain()
}

func (o Operator) SCSCFName(port int) string {
	return "sip:" + o.SCSCFHost() + ":" + strconv.Itoa(port)
}

func (o Operator) SIPAliases() []string {
	return []string{o.HomeDomain(), o.PCSCFHost(), o.SCSCFHost()}
}

func (o Operator) SameIdentity(p Operator) bool {
	return o.HomeDomain() == p.HomeDomain()
}

func (s Settings) SameCore(t Settings) bool {
	return s.SameNode(t) && slices.EqualFunc(s.Peers, t.Peers, Peer.equal) && slices.Equal(s.Routes, t.Routes)
}

func (s Settings) SameNode(t Settings) bool {
	return s.Operator.SameIdentity(t.Operator) && s.Policy == t.Policy && slices.Equal(s.Transports(), t.Transports())
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

	if err := s.CallRecords.Validate(); err != nil {
		return err
	}

	if err := s.IPSMGW.Validate(); err != nil {
		return err
	}

	if err := s.validateSMSRoutes(); err != nil {
		return err
	}

	return s.validateSIPTrunks()
}

func (s Settings) keepsPCRF(had bool) error {
	if had && s.Policy.Interface == PolicyRx && len(s.Serving(ApplicationRx)) == 0 {
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
	case n.MinNationalDigits < 0 || n.MinNationalDigits > MaxMinNationalDigits:
		return invalidf("numbering.min_national_digits must be between 0 and %d", MaxMinNationalDigits)
	}

	return nil
}

type Store interface {
	UpdateOperator(ctx context.Context, o Operator) error
	CreatePeer(ctx context.Context, p Peer) error
	UpdatePeer(ctx context.Context, p Peer) error
	DeletePeer(ctx context.Context, id string) error
	UpdateRoute(ctx context.Context, r Route) error
	UpdatePolicy(ctx context.Context, p Policy) error
	UpdateCallRecords(ctx context.Context, c CallRecords) error
	UpdateIPSMGW(ctx context.Context, g IPSMGW) error
	CreateSMSRoute(ctx context.Context, r SMSRoute) error
	UpdateSMSRoute(ctx context.Context, r SMSRoute) error
	DeleteSMSRoute(ctx context.Context, id string) error
	CreateTrunk(ctx context.Context, t Trunk) error
	UpdateTrunk(ctx context.Context, t Trunk) error
	DeleteTrunk(ctx context.Context, id string) error
	CreateSIPRoute(ctx context.Context, r SIPRoute) error
	UpdateSIPRoute(ctx context.Context, r SIPRoute) error
	DeleteSIPRoute(ctx context.Context, id string) error
}

type Live struct {
	store Store
	check func(context.Context, Settings) error

	current atomic.Pointer[Settings]
	runtime atomic.Pointer[Settings]

	mu         sync.Mutex
	trunkGuard func(Trunk) error
	changed    chan struct{}
	causes     []trace.SpanContext
}

func NewLive(store Store, initial Settings, check func(context.Context, Settings) error) *Live {
	l := &Live{store: store, check: check, changed: make(chan struct{})}
	l.publish(&initial)

	return l
}

func (l *Live) Get() Settings {
	return *l.current.Load()
}

func (l *Live) Runtime() Settings {
	return *l.runtime.Load()
}

func (l *Live) publish(s *Settings) {
	rt := *s
	rt.Trunks = make([]Trunk, 0, len(s.Trunks))

	for _, t := range s.Trunks {
		if e := t.Effective(); l.checkTrunk(e) == nil {
			rt.Trunks = append(rt.Trunks, e)
		}
	}

	l.current.Store(s)
	l.runtime.Store(&rt)
}

func (l *Live) UpdateOperator(ctx context.Context, o Operator) error {
	return l.change(ctx, func(next *Settings) error {
		next.Operator = o
		return nil
	}, func() error { return l.store.UpdateOperator(ctx, o) })
}

func (l *Live) CreatePeer(ctx context.Context, p Peer) (Peer, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Peer{}, fmt.Errorf("peer ID: %w", err)
	}

	p.ID = id.String()

	err = l.change(ctx, func(next *Settings) error {
		next.Peers = append(slices.Clone(next.Peers), p)
		return nil
	}, func() error { return l.store.CreatePeer(ctx, p) })

	return p, err
}

func (l *Live) UpdatePeer(ctx context.Context, p Peer) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.peer(p.ID)
		if !ok {
			return kindError{ErrNotFound, "Diameter peer not found"}
		}

		had := len(next.Serving(ApplicationRx)) > 0
		next.Peers = slices.Clone(next.Peers)
		next.Peers[i] = p

		return next.keepsPCRF(had)
	}, func() error { return l.store.UpdatePeer(ctx, p) })
}

func (l *Live) DeletePeer(ctx context.Context, id string) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.peer(id)
		if !ok {
			return kindError{ErrNotFound, "Diameter peer not found"}
		}

		if err := next.peerInUse(id); err != nil {
			return err
		}

		had := len(next.Serving(ApplicationRx)) > 0
		next.Peers = slices.Delete(slices.Clone(next.Peers), i, i+1)

		return next.keepsPCRF(had)
	}, func() error { return l.store.DeletePeer(ctx, id) })
}

func (l *Live) UpdateRoute(ctx context.Context, r Route) error {
	return l.change(ctx, func(next *Settings) error {
		i := slices.IndexFunc(next.Routes, func(q Route) bool { return q.Application == r.Application })
		if i < 0 {
			return kindError{ErrNotFound, "Diameter route not found"}
		}

		next.Routes = slices.Clone(next.Routes)
		next.Routes[i] = r

		return nil
	}, func() error { return l.store.UpdateRoute(ctx, r) })
}

func (l *Live) UpdatePolicy(ctx context.Context, p Policy) error {
	return l.change(ctx, func(next *Settings) error {
		next.Policy = p
		return nil
	}, func() error { return l.store.UpdatePolicy(ctx, p) })
}

func (l *Live) UpdateIPSMGW(ctx context.Context, g IPSMGW) error {
	return l.change(ctx, func(next *Settings) error {
		next.IPSMGW = g
		return nil
	}, func() error { return l.store.UpdateIPSMGW(ctx, g) })
}

func (l *Live) CreateSMSRoute(ctx context.Context, r SMSRoute) (SMSRoute, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return SMSRoute{}, fmt.Errorf("SMS route ID: %w", err)
	}

	r.ID = id.String()

	err = l.change(ctx, func(next *Settings) error {
		next.SMSRoutes = append(slices.Clone(next.SMSRoutes), r)
		return nil
	}, func() error { return l.store.CreateSMSRoute(ctx, r) })

	return r, err
}

func (l *Live) UpdateSMSRoute(ctx context.Context, r SMSRoute) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.smsRoute(r.ID)
		if !ok {
			return kindError{ErrNotFound, "SMS route not found"}
		}

		next.SMSRoutes = slices.Clone(next.SMSRoutes)
		next.SMSRoutes[i] = r

		return nil
	}, func() error { return l.store.UpdateSMSRoute(ctx, r) })
}

func (l *Live) DeleteSMSRoute(ctx context.Context, id string) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.smsRoute(id)
		if !ok {
			return kindError{ErrNotFound, "SMS route not found"}
		}

		next.SMSRoutes = slices.Delete(slices.Clone(next.SMSRoutes), i, i+1)

		return nil
	}, func() error { return l.store.DeleteSMSRoute(ctx, id) })
}

func (l *Live) UpdateCallRecords(ctx context.Context, c CallRecords) error {
	return l.change(ctx, func(next *Settings) error {
		next.CallRecords = c
		return nil
	}, func() error { return l.store.UpdateCallRecords(ctx, c) })
}

func (l *Live) CreateTrunk(ctx context.Context, t Trunk) (Trunk, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Trunk{}, fmt.Errorf("SIP trunk ID: %w", err)
	}

	t.ID = id.String()

	err = l.change(ctx, func(next *Settings) error {
		if err := t.TLS.checkCurrent(TrunkTLS{}, time.Now()); err != nil {
			return err
		}

		if err := l.checkTrunk(t.Effective()); err != nil {
			return err
		}

		next.Trunks = append(slices.Clone(next.Trunks), t)

		return nil
	}, func() error { return l.store.CreateTrunk(ctx, t) })

	return t, err
}

func (l *Live) UpdateTrunk(ctx context.Context, t Trunk) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.trunk(t.ID)
		if !ok {
			return kindError{ErrNotFound, "SIP trunk not found"}
		}

		if err := t.TLS.checkCurrent(next.Trunks[i].TLS, time.Now()); err != nil {
			return err
		}

		if err := l.checkTrunk(t.Effective()); err != nil {
			return err
		}

		next.Trunks = slices.Clone(next.Trunks)
		next.Trunks[i] = t

		return nil
	}, func() error { return l.store.UpdateTrunk(ctx, t) })
}

func (l *Live) SetTrunkGuard(guard func(Trunk) error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.trunkGuard = guard
	l.publish(l.current.Load())
}

func (l *Live) Usable(id string) bool {
	_, ok := l.Runtime().Trunk(id)
	return ok
}

func (l *Live) checkTrunk(t Trunk) error {
	if l.trunkGuard == nil {
		return nil
	}

	err := l.trunkGuard(t)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalid):
		return err
	default:
		return kindError{ErrInvalid, err.Error()}
	}
}

func (l *Live) DeleteTrunk(ctx context.Context, id string) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.trunk(id)
		if !ok {
			return kindError{ErrNotFound, "SIP trunk not found"}
		}

		if err := next.trunkInUse(id); err != nil {
			return err
		}

		next.Trunks = slices.Delete(slices.Clone(next.Trunks), i, i+1)

		return nil
	}, func() error { return l.store.DeleteTrunk(ctx, id) })
}

func (l *Live) CreateSIPRoute(ctx context.Context, r SIPRoute) (SIPRoute, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return SIPRoute{}, fmt.Errorf("SIP route ID: %w", err)
	}

	r.ID = id.String()

	err = l.change(ctx, func(next *Settings) error {
		next.SIPRoutes = append(slices.Clone(next.SIPRoutes), r)
		return nil
	}, func() error { return l.store.CreateSIPRoute(ctx, r) })

	return r, err
}

func (l *Live) UpdateSIPRoute(ctx context.Context, r SIPRoute) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.sipRoute(r.ID)
		if !ok {
			return kindError{ErrNotFound, "SIP route not found"}
		}

		next.SIPRoutes = slices.Clone(next.SIPRoutes)
		next.SIPRoutes[i] = r

		return nil
	}, func() error { return l.store.UpdateSIPRoute(ctx, r) })
}

func (l *Live) DeleteSIPRoute(ctx context.Context, id string) error {
	return l.change(ctx, func(next *Settings) error {
		i, ok := next.sipRoute(id)
		if !ok {
			return kindError{ErrNotFound, "SIP route not found"}
		}

		next.SIPRoutes = slices.Delete(slices.Clone(next.SIPRoutes), i, i+1)

		return nil
	}, func() error { return l.store.DeleteSIPRoute(ctx, id) })
}

func (l *Live) change(ctx context.Context, edit func(next *Settings) error, save func() error) error {
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
		if err := l.check(ctx, next); err != nil {
			return err
		}
	}

	if err := save(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	l.publish(&next)

	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		l.causes = append(l.causes[max(0, len(l.causes)+1-maxCauses):], sc)
	}

	close(l.changed)
	l.changed = make(chan struct{})

	return nil
}

func (l *Live) Changed() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.changed
}

const maxCauses = 32

func (l *Live) Take() (Settings, []trace.SpanContext) {
	l.mu.Lock()
	defer l.mu.Unlock()

	causes := l.causes
	l.causes = nil

	return *l.current.Load(), causes
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
