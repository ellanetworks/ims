package n5policy

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/policy"
)

// TS 29.514 clause 5.7
// TS 29.500 clause 5.2.7.2
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

	if n.Cause() == n5.CauseRequestedServiceTemporarilyNotAuthorized {
		e.RetryAfter = n.RetryAfter
	} else {
		e.Backoff = n.RetryAfter
	}

	e.Transient = unanswered(n)

	return e
}

// TS 29.514 clause 5.7.3
// TS 29.514 clause 4.2.2.2
func unknownSession(n *n5.Error) bool {
	if n.Status != http.StatusNotFound || n.Op == n5.OpCreate {
		return false
	}

	c := n.Cause()

	return c == "" || c == n5.CauseAppSessionContextNotFound
}

// TS 29.500 clause 5.2.7.2
func unanswered(n *n5.Error) bool {
	switch n.Status {
	case 0, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}

	return false
}
