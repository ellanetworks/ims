package policy

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

var (
	ErrRefused     = errors.New("refused by the policy function")
	ErrUnreachable = errors.New("policy function unreachable")
	ErrMalformed   = errors.New("malformed answer from the policy function")
	// TS 29.214 clause 4.4.1
	// TS 29.514 clause 5.7.3
	ErrUnknownSession = errors.New("session unknown to the policy function")
	// RFC 6733 section 8.18
	ErrSessionLost = errors.New("session lost with its policy function")
)

type Error struct {
	Kind       error
	Result     string
	RetryAfter time.Duration
	Backoff    time.Duration
	Transient  bool
	Err        error
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil:
		return e.Err.Error()
	case e.Kind != nil:
		return e.Kind.Error()
	}

	return "policy error"
}

func (e *Error) Unwrap() []error {
	var errs []error

	for _, err := range []error{e.Kind, e.Err} {
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

func ResultOf(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) && e.Result != "" {
		return e.Result, true
	}

	return "", false
}

func Transient(err error) bool {
	var e *Error

	return errors.As(err, &e) && e.Transient
}

func RetryAfter(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.RetryAfter
	}

	return 0
}

// TS 29.500 clause 6.4.2
func Backoff(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.Backoff
	}

	return 0
}

type Backend interface {
	Endpoint() string
	NewSessionID() string
	Bind(s Sink)

	OpenSignalling(ctx context.Context, id string, s Signalling, wait bool) (ref string, err error)
	Authorize(ctx context.Context, id, ref string, r Request) (Grant, error)
	Terminate(ctx context.Context, id, ref string, cause Termination, wait bool) error
}

type Sink interface {
	Notify(ctx context.Context, id string, e Event) bool
	Abort(ctx context.Context, id string, a Abort) (terminate func(), known bool)
}

// TS 29.214 clause 4.4.5
// TS 29.514 clause 4.2.6.7
type Signalling struct {
	UE netip.Addr
}

type Request struct {
	UE          netip.Addr
	Initial     bool
	Service     string
	Components  []MediaComponent
	Subscribers []Subscriber
	Forking     Forking
	MCPTT       *MCPTTPriority
}

// TS 29.214 clause A.13.2
// TS 29.514 clause B.10.2
type MCPTTPriority struct {
	Namespace string
	Level     uint8
}

type Grant struct {
	Ref      string
	Charging Charging
}

type MediaType uint8

const (
	MediaAudio MediaType = iota + 1
	MediaVideo
	MediaData
	MediaApplication
	MediaControl
	MediaText
	MediaMessage
	MediaOther
)

type FlowStatus uint8

const (
	FlowEnabledUplink FlowStatus = iota + 1
	FlowEnabledDownlink
	FlowEnabled
	FlowDisabled
	FlowRemoved
)

type FlowUsage uint8

const (
	FlowUsageNone FlowUsage = iota
	FlowUsageRTCP
	FlowUsageAFSignalling
)

type Protocol uint8

const (
	ProtocolTCP Protocol = 6
	ProtocolUDP Protocol = 17
)

// TS 29.213 clause 6.2
// TS 29.513 clause 7.2.3
type MediaComponent struct {
	Number         uint32
	Type           MediaType
	Status         FlowStatus
	MaxRequestedUL *uint64
	MaxRequestedDL *uint64
	RR             *uint32
	RS             *uint32
	Codecs         []Codec
	SubComponents  []SubComponent
}

type SubComponent struct {
	FlowNumber uint32
	Flows      []Flow
	Usage      FlowUsage
}

type Flow struct {
	Uplink          bool
	Protocol        Protocol
	Source          netip.Prefix
	Destination     netip.Prefix
	DestinationPort uint16
}

// TS 29.214 clause 5.3.7
// TS 29.514 clause 5.6.3.2
type Codec struct {
	Uplink bool
	Answer bool
	SDP    string
}

type SubscriberKind uint8

const (
	SubscriberSIPURI SubscriberKind = iota + 1
	SubscriberE164
)

type Subscriber struct {
	Kind SubscriberKind
	ID   string
}

type Forking uint8

const (
	ForkingSingleDialogue Forking = iota
	ForkingSeveralDialogues
)

func (f Forking) String() string {
	if f == ForkingSeveralDialogues {
		return "SEVERAL_DIALOGUES"
	}

	return "SINGLE_DIALOGUE"
}

type Access uint8

const (
	AccessUnknown Access = iota
	AccessEPS
	Access5GS
	AccessOther
)

// TS 29.214 clause 5.3.2
// TS 29.214 clause 5.3.3
// TS 29.514 clause 5.6.2.32
// TS 29.512
type Charging struct {
	Address     netip.Addr
	Access      Access
	Identifiers []ChargingID
}

type ChargingID struct {
	Value []byte
	Flows []Flows
}

type Flows struct {
	Component   uint32
	FlowNumbers []uint32
}

type EventKind int

const (
	EventOther EventKind = iota
	EventChargingCorrelation
	EventBearerLost
	EventBearerReleased
	EventResourcesFailed
)

func (k EventKind) String() string {
	switch k {
	case EventChargingCorrelation:
		return "CHARGING_CORRELATION"
	case EventBearerLost:
		return "BEARER_LOST"
	case EventBearerReleased:
		return "BEARER_RELEASED"
	case EventResourcesFailed:
		return "FAILED_RESOURCES_ALLOCATION"
	}

	return "OTHER"
}

func (k EventKind) MarshalText() ([]byte, error) {
	return []byte(k.String()), nil
}

type Event struct {
	Kinds      []EventKind
	Components []uint32
	Charging   *Charging
}

func (e Event) Has(kinds ...EventKind) bool {
	for _, k := range e.Kinds {
		for _, want := range kinds {
			if k == want {
				return true
			}
		}
	}

	return false
}

// TS 29.214 clause 4.4.6.1
// TS 29.514 clause 4.2.5.3
type Abort struct {
	Cause                 string
	InsufficientResources bool
}

type Termination uint8

const (
	TerminationLogout Termination = iota + 1
	TerminationExpired
	TerminationAdministrative
	TerminationBadAnswer
)

func (t Termination) String() string {
	switch t {
	case TerminationLogout:
		return "logout"
	case TerminationExpired:
		return "expired"
	case TerminationAdministrative:
		return "administrative"
	case TerminationBadAnswer:
		return "bad answer"
	}

	return "unknown"
}
