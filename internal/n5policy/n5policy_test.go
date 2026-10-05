package n5policy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/policy"
)

func problem(cause string) *n5.ProblemDetails {
	return &n5.ProblemDetails{Cause: cause}
}

func TestClassify(t *testing.T) {
	malformed := fmt.Errorf("%w: no Location", n5.ErrMalformedResponse)
	connect := fmt.Errorf("%w: connection refused", n5.ErrConnect)

	for name, tc := range map[string]struct {
		err       *n5.Error
		kind      error
		transient bool
		result    string
	}{
		"invalid service information": {
			&n5.Error{Op: n5.OpCreate, Status: 400, Problem: problem(n5.CauseInvalidServiceInformation)},
			policy.ErrRefused, false, "400 INVALID_SERVICE_INFORMATION",
		},
		"not authorized": {
			&n5.Error{Op: n5.OpModify, Status: 403, Problem: problem(n5.CauseRequestedServiceNotAuthorized)},
			policy.ErrRefused, false, "403 REQUESTED_SERVICE_NOT_AUTHORIZED",
		},
		"no binding": {
			&n5.Error{Op: n5.OpCreate, Status: 500, Problem: problem(n5.CausePDUSessionNotAvailable)},
			policy.ErrRefused, false, "500 PDU_SESSION_NOT_AVAILABLE",
		},
		"create not found":       {&n5.Error{Op: n5.OpCreate, Status: 404}, policy.ErrRefused, false, "404"},
		"modify unknown context": {&n5.Error{Op: n5.OpModify, Status: 404}, policy.ErrUnknownSession, false, "404"},
		"delete unknown context": {
			&n5.Error{Op: n5.OpDelete, Status: 404, Problem: problem(n5.CauseAppSessionContextNotFound)},
			policy.ErrUnknownSession, false, "404 APPLICATION_SESSION_CONTEXT_NOT_FOUND",
		},
		"delete other not found": {
			&n5.Error{Op: n5.OpDelete, Status: 404, Problem: problem("RESOURCE_URI_STRUCTURE_NOT_FOUND")},
			policy.ErrRefused, false, "404 RESOURCE_URI_STRUCTURE_NOT_FOUND",
		},
		"congestion":   {&n5.Error{Op: n5.OpCreate, Status: 503, Problem: problem("NF_CONGESTION")}, policy.ErrRefused, true, "503 NF_CONGESTION"},
		"rate limited": {&n5.Error{Op: n5.OpModify, Status: 429}, policy.ErrRefused, true, "429"},
		"gateway":      {&n5.Error{Op: n5.OpDelete, Status: 504}, policy.ErrRefused, true, "504"},
		"server error": {&n5.Error{Op: n5.OpDelete, Status: 500}, policy.ErrRefused, false, "500"},
		"malformed":    {&n5.Error{Op: n5.OpCreate, Status: 201, Err: malformed}, policy.ErrMalformed, false, "201"},
		"connect":      {&n5.Error{Op: n5.OpCreate, Err: connect}, policy.ErrUnreachable, true, ""},
		"no answer":    {&n5.Error{Op: n5.OpModify, Err: context.DeadlineExceeded}, nil, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := classify(tc.err)

			if !errors.Is(err, tc.err) && !errors.Is(err, tc.err.Err) {
				t.Fatalf("%v does not wrap %v", err, tc.err)
			}

			for _, k := range []error{policy.ErrRefused, policy.ErrMalformed, policy.ErrUnreachable, policy.ErrUnknownSession} {
				if errors.Is(err, k) != (k == tc.kind) {
					t.Fatalf("errors.Is(%v, %v) = %t, want kind %v", err, k, k != tc.kind, tc.kind)
				}
			}

			if policy.Transient(err) != tc.transient {
				t.Fatalf("Transient = %t, want %t", policy.Transient(err), tc.transient)
			}

			if r, _ := policy.ResultOf(err); r != tc.result {
				t.Fatalf("ResultOf = %q, want %q", r, tc.result)
			}
		})
	}
}

// TS 29.514 §4.2.2.2: only REQUESTED_SERVICE_TEMPORARILY_NOT_AUTHORIZED holds the same service information back.
func TestClassifyRetryAfter(t *testing.T) {
	temporary := &n5.Error{
		Op: n5.OpCreate, Status: 403, RetryAfter: 30 * time.Second,
		Problem: problem(n5.CauseRequestedServiceTemporarilyNotAuthorized),
	}

	if got := policy.RetryAfter(classify(temporary)); got != 30*time.Second {
		t.Fatalf("RetryAfter = %s, want 30s", got)
	}

	overload := &n5.Error{Op: n5.OpCreate, Status: 503, RetryAfter: 30 * time.Second}
	if got := policy.RetryAfter(classify(overload)); got != 0 {
		t.Fatalf("RetryAfter = %s for an overload, want none", got)
	}
}

func TestClassifyLocalError(t *testing.T) {
	local := errors.New("create: no notifUri")

	if err := classify(local); err != local {
		t.Fatalf("classify(%v) = %v, want it unchanged", local, err)
	}
}
