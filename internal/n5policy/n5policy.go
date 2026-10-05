// Package n5policy is the policy backend over N5 (TS 29.514).
package n5policy

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/policy"
)

// TS 29.514 §5.7, TS 29.500 §5.2.7.2
func classify(err error) error {
	var n *n5.Error
	if !errors.As(err, &n) {
		return err
	}

	e := &policy.Error{Err: err}

	if n.Status != 0 {
		e.Result = strconv.Itoa(n.Status)
		if c := n.Cause(); c != "" {
			e.Result += " " + c
		}
	}

	switch {
	case errors.Is(err, n5.ErrMalformedResponse):
		e.Kind = policy.ErrMalformed
	case n.Status == 0:
		if errors.Is(err, n5.ErrConnect) {
			e.Kind = policy.ErrUnreachable
		}
	case unknownSession(n):
		e.Kind = policy.ErrUnknownSession
	default:
		e.Kind = policy.ErrRefused
	}

	// TS 29.514 §4.2.2.2, §4.2.3.2: the same service information waits for the retry interval. Elsewhere,
	// Retry-After is overload: no request until it elapses (TS 29.500 §6.4.2.2, §6.4.2.3).
	if n.Cause() == n5.CauseRequestedServiceTemporarilyNotAuthorized {
		e.RetryAfter = n.RetryAfter
	} else {
		e.Backoff = n.RetryAfter
	}

	e.Transient = unanswered(n)

	return e
}

// TS 29.514 §5.7.3: APPLICATION_SESSION_CONTEXT_NOT_FOUND answers a request to an existing context. A create
// names no context, so its 404 is a refusal (Open5GS answers 404 where TS 29.514 §4.2.2.2 has 500
// PDU_SESSION_NOT_AVAILABLE).
func unknownSession(n *n5.Error) bool {
	if n.Status != http.StatusNotFound || n.Op == n5.OpCreate {
		return false
	}

	c := n.Cause()

	return c == "" || c == n5.CauseAppSessionContextNotFound
}

// TS 29.500 §5.2.7.2: without a response, or with one saying the PCF did not process the request (timeout,
// overload, an intermediary that could not reach it), the request may succeed if sent again.
func unanswered(n *n5.Error) bool {
	switch n.Status {
	case 0, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}

	return false
}
