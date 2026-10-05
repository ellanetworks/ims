// Package policy is the P-CSCF's view of the PCRF or PCF, independent of Rx (TS 29.214) and N5 (TS 29.514).
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
	// ErrUnknownSession: the policy function no longer knows the session (TS 29.214 §4.4.1 DIAMETER_UNKNOWN_SESSION_ID,
	// TS 29.514 §5.7.3 APPLICATION_SESSION_CONTEXT_NOT_FOUND).
	ErrUnknownSession = errors.New("session unknown to the policy function")
)

// Error carries a backend error with its class: ErrRefused, ErrUnreachable, ErrMalformed, ErrUnknownSession or none.
// Transient means the policy function may not have handled the request, so sending it again may succeed.
// RetryAfter holds back the same service information; Backoff holds back any request to an overloaded policy
// function.
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

// ResultOf returns the policy function's result for err, if it answered.
func ResultOf(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) && e.Result != "" {
		return e.Result, true
	}

	return "", false
}

// Transient reports whether the request that failed with err may succeed if sent again.
func Transient(err error) bool {
	var e *Error

	return errors.As(err, &e) && e.Transient
}

// RetryAfter returns how long the same service information must not be sent again after err.
func RetryAfter(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.RetryAfter
	}

	return 0
}

// Backoff returns how long the policy function asked to receive no request after err (TS 29.500 §6.4.2).
func Backoff(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.Backoff
	}

	return 0
}

// Backend opens, modifies and closes sessions with one policy function. The id names a session locally and on
// the P-CSCF's side of the interface; the ref is the backend's state for it, kept across restarts. Endpoint
// identifies the policy function itself, not its local configuration, so stored sessions survive renames.
type Backend interface {
	Endpoint() string
	NewSessionID() string
	Bind(s Sink)

	// TS 29.214 §4.4.5, TS 29.514 §4.2.6.7
	OpenSignalling(ctx context.Context, id string, s Signalling, wait bool) (ref string, err error)
	// TS 29.214 §4.4.1, §4.4.2, TS 29.514 §4.2.2.2, §4.2.3.2
	Authorize(ctx context.Context, id, ref string, r Request) (Grant, error)
	// TS 29.214 §4.4.4, TS 29.514 §4.2.4.2
	Terminate(ctx context.Context, id, ref string, cause Termination, wait bool) error
}

// Sink receives what the policy function reports about the P-CSCF's sessions.
type Sink interface {
	Notify(id string, e Event) bool
	Abort(id string, a Abort) (terminate func(), known bool)
}

// TS 29.214 §4.4.5, TS 29.514 §4.2.6.7
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

// TS 29.213 §6.2, TS 29.513 §7.2.3. Bandwidths are in bit/s.
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

// Flow is an IP flow: uplink flows leave the UE.
type Flow struct {
	Uplink          bool
	Protocol        Protocol
	Source          netip.Prefix
	Destination     netip.Prefix
	DestinationPort uint16
}

// TS 29.214 §5.3.7, TS 29.514 §5.6.3.2 (CodecData). Uplink means the SDP came from the UE.
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

// TS 29.214 §5.3.2, §5.3.3, TS 29.514 §5.6.2.32 (AccessNetChargingIdentifier), TS 29.512 (AccNetChargingAddress)
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

// Event is a notification for one session. Components lists the media components it is about, all of them when
// empty; Charging is set with EventChargingCorrelation.
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

// TS 29.214 §4.4.6.1, TS 29.514 §4.2.5.3
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
