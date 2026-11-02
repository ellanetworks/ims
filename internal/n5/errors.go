package n5

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Op string

// TS 29.514 clause 4.2.2
// TS 29.514 clause 4.2.3
// TS 29.514 clause 4.2.4
const (
	OpCreate Op = "create"
	OpModify Op = "modify"
	OpDelete Op = "delete"
)

var (
	ErrMalformedResponse = errors.New("malformed response from the PCF")
	ErrConnect           = errors.New("cannot connect to the PCF")
)

// TS 29.514 clause 5.7.3
// TS 29.500 clause 5.2.7.2
const (
	CauseInvalidServiceInformation                = "INVALID_SERVICE_INFORMATION"
	CauseFilterRestrictions                       = "FILTER_RESTRICTIONS"
	CauseDuplicatedAFSession                      = "DUPLICATED_AF_SESSION"
	CauseRequestedServiceNotAuthorized            = "REQUESTED_SERVICE_NOT_AUTHORIZED"
	CauseRequestedServiceTemporarilyNotAuthorized = "REQUESTED_SERVICE_TEMPORARILY_NOT_AUTHORIZED"
	CauseTemporaryNetworkFailure                  = "TEMPORARY_NETWORK_FAILURE"
	CauseAppSessionContextNotFound                = "APPLICATION_SESSION_CONTEXT_NOT_FOUND"
	CausePDUSessionNotAvailable                   = "PDU_SESSION_NOT_AVAILABLE"

	// TS 29.500 clause 5.2.7.2 table 5.2.7.2-1
	CauseResourceContextNotFound = "RESOURCE_CONTEXT_NOT_FOUND"
)

type Error struct {
	Op         Op
	Status     int
	Problem    *ProblemDetails
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	var b strings.Builder

	b.WriteString("npcf-policyauthorization " + string(e.Op))

	if e.Status != 0 {
		b.WriteString(": " + strconv.Itoa(e.Status) + " " + http.StatusText(e.Status))
	}

	if c := e.Cause(); c != "" {
		b.WriteString(" (" + c + ")")
	}

	if e.Problem != nil && e.Problem.Detail != "" {
		b.WriteString(": " + e.Problem.Detail)
	}

	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}

	return b.String()
}

func (e *Error) Unwrap() error {
	return e.Err
}

// TS 29.514 clause 5.7.3
func (e *Error) Cause() string {
	if e.Problem == nil {
		return ""
	}

	return e.Problem.Cause
}

func transportError(op Op, err error) *Error {
	var oe *net.OpError

	switch {
	case errors.Is(err, ErrConnect):
	case errors.As(err, &oe) && (oe.Op == "dial" || rejectedCertificate(oe)):
		err = fmt.Errorf("%w: %w", ErrConnect, err)
	case notEstablished(err):
		err = fmt.Errorf("%w: %w (did the PCF refuse the TLS certificate?)", ErrConnect, err)
	}

	return &Error{Op: op, Err: err}
}

func notEstablished(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err.Error() == "http2: client conn could not be established" {
			return true
		}
	}

	return false
}

// RFC 8446 section 6.2
var certificateAlerts = []uint64{42, 43, 44, 45, 46, 48, 49, 116}

// RFC 8446 section 4.4.2.4
func rejectedCertificate(oe *net.OpError) bool {
	if oe.Op != "remote error" || oe.Err == nil {
		return false
	}

	v := reflect.ValueOf(oe.Err)

	return v.Kind() == reflect.Uint8 && slices.Contains(certificateAlerts, v.Uint())
}

const maxRetryAfter = 1<<32 - 1

// TS 29.500 clause 5.2.2.2
// RFC 9110 section 10.2.3
func retryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}

	if digits(v) {
		s, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			s = maxRetryAfter
		}

		return time.Duration(min(s, maxRetryAfter)) * time.Second
	}

	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now).Round(time.Second)
	}

	return 0
}

// TS 29.500 clause 5.2.7
// TS 29.514 clause 5.2.2.2
func WriteProblem(w http.ResponseWriter, p ProblemDetails) {
	if p.Status == 0 {
		p.Status = http.StatusInternalServerError
	}

	b, err := json.Marshal(p)
	if err != nil {
		b = []byte("{}")
	}

	w.Header().Set("Content-Type", ContentProblem)
	w.WriteHeader(p.Status)
	_, _ = w.Write(b)
}
