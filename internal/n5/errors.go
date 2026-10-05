package n5

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Op names a service operation of Npcf_PolicyAuthorization.
type Op string

// TS 29.514 §4.2.2, §4.2.3, §4.2.4
const (
	OpCreate Op = "create"
	OpModify Op = "modify"
	OpDelete Op = "delete"
)

var (
	// ErrMalformedResponse: the PCF accepted the request, but its response breaks TS 29.514.
	ErrMalformedResponse = errors.New("malformed response from the PCF")
	// ErrConnect: no connection to the PCF, so the request was not sent.
	ErrConnect = errors.New("cannot connect to the PCF")
)

// TS 29.514 §5.7.3, TS 29.500 §5.2.7.2
const (
	CauseInvalidServiceInformation                = "INVALID_SERVICE_INFORMATION"
	CauseFilterRestrictions                       = "FILTER_RESTRICTIONS"
	CauseRequestedServiceNotAuthorized            = "REQUESTED_SERVICE_NOT_AUTHORIZED"
	CauseRequestedServiceTemporarilyNotAuthorized = "REQUESTED_SERVICE_TEMPORARILY_NOT_AUTHORIZED"
	CauseTemporaryNetworkFailure                  = "TEMPORARY_NETWORK_FAILURE"
	CauseAppSessionContextNotFound                = "APPLICATION_SESSION_CONTEXT_NOT_FOUND"
	CausePDUSessionNotAvailable                   = "PDU_SESSION_NOT_AVAILABLE"
)

// Error is a failed operation. Status is set when an HTTP response came back, from the PCF or an intermediary;
// otherwise Err says why there is none. A response that is not what TS 29.514 defines wraps ErrMalformedResponse.
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

// Cause returns the application error of the ProblemDetails, if any (TS 29.514 §5.7.3).
func (e *Error) Cause() string {
	if e.Problem == nil {
		return ""
	}

	return e.Problem.Cause
}

// transportError wraps a request that got no response, telling apart one that never reached the PCF.
func transportError(op Op, err error) *Error {
	var dial *net.OpError
	if errors.As(err, &dial) && dial.Op == "dial" {
		err = fmt.Errorf("%w: %w", ErrConnect, err)
	}

	return &Error{Op: op, Err: err}
}

// retryAfter reads a Retry-After header: seconds, or an HTTP-date (TS 29.500 §5.2.2.2, RFC 9110 §10.2.3).
func retryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}

	if s, err := strconv.ParseUint(v, 10, 32); err == nil {
		return time.Duration(s) * time.Second
	}

	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now).Round(time.Second)
	}

	return 0
}
