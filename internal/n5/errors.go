package n5

import (
	"encoding/json"
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
	CauseDuplicatedAFSession                      = "DUPLICATED_AF_SESSION"
	CauseRequestedServiceNotAuthorized            = "REQUESTED_SERVICE_NOT_AUTHORIZED"
	CauseRequestedServiceTemporarilyNotAuthorized = "REQUESTED_SERVICE_TEMPORARILY_NOT_AUTHORIZED"
	CauseTemporaryNetworkFailure                  = "TEMPORARY_NETWORK_FAILURE"
	CauseAppSessionContextNotFound                = "APPLICATION_SESSION_CONTEXT_NOT_FOUND"
	CausePDUSessionNotAvailable                   = "PDU_SESSION_NOT_AVAILABLE"

	// TS 29.500 Table 5.2.7.2-1: a notification for a context the consumer does not know.
	CauseResourceContextNotFound = "RESOURCE_CONTEXT_NOT_FOUND"
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
	if !errors.Is(err, ErrConnect) && errors.As(err, &dial) && dial.Op == "dial" {
		err = fmt.Errorf("%w: %w", ErrConnect, err)
	}

	return &Error{Op: op, Err: err}
}

// maxRetryAfter caps a Retry-After, in seconds; a larger one is read as this.
const maxRetryAfter = 1<<32 - 1

// retryAfter reads a Retry-After header: seconds, or an HTTP-date (TS 29.500 §5.2.2.2, RFC 9110 §10.2.3).
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

// WriteProblem answers a request with an error status and its ProblemDetails (TS 29.500 §5.2.7, TS 29.514
// §5.2.2.2). A zero Status is 500.
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
