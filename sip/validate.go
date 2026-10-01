package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// StatusError is a request rejected by validation, with the status code
// to answer it with.
type StatusError struct {
	StatusCode int
	Err        error
}

// Error returns the status and the cause.
func (e *StatusError) Error() string {
	return fmt.Sprintf("sip: %d %s: %v", e.StatusCode, ReasonPhrase(e.StatusCode), e.Err)
}

// Unwrap returns the cause.
func (e *StatusError) Unwrap() error { return e.Err }

func badRequest(err error) *StatusError {
	return &StatusError{StatusCode: 400, Err: err}
}

// ErrUnanswerable is returned, wrapped, for a request that cannot be
// answered because it has no usable top Via (RFC 3261 §18.2.2). Such a
// request is dropped.
var ErrUnanswerable = errors.New("request cannot be answered")

// Validate checks a received request before a transaction is created
// (RFC 3261 §8.2, §16.3, RFC 4475). It returns an error wrapping
// ErrUnanswerable when the top Via is missing or malformed. Otherwise it
// returns a *StatusError:
//   - 400 for missing, repeated or malformed core header fields (Call-ID,
//     CSeq, From, To, Max-Forwards, Content-Length, the top Via and its
//     received and rport parameters), a CSeq method that differs from the
//     request method, or a top Via without an RFC 3261 branch: RFC 2543
//     peers are not supported;
//   - 505 for a SIP-Version other than 2.0;
//   - 416 for a Request-URI scheme other than sip, sips, tel and urn
//     (emergency service URNs, RFC 5031).
//
// Validate leaves to the proxy and the UAS the checks that depend on their
// role, such as Max-Forwards reaching 0, Require, Content-Type and Expires.
func (r *Request) Validate() error {
	top, err := r.Header.TopVia()
	if err != nil {
		return fmt.Errorf("sip: %w: %w", ErrUnanswerable, err)
	}

	if err := checkCommon(r.Header); err != nil {
		return badRequest(err)
	}

	if err := checkVia(top); err != nil {
		return badRequest(err)
	}

	if b := top.Branch(); len(b) <= len(MagicCookie) || !strings.HasPrefix(b, MagicCookie) {
		return badRequest(fmt.Errorf("top Via branch %q is not an RFC 3261 branch", b))
	}

	if r.Version != "" && !strings.EqualFold(r.Version, Version) {
		return &StatusError{StatusCode: 505, Err: fmt.Errorf("SIP-Version %s", r.Version)}
	}

	if u := r.URI; !u.IsSIP() && !u.IsTel() && !strings.EqualFold(u.Scheme, "urn") {
		return &StatusError{StatusCode: 416, Err: fmt.Errorf("scheme %s", r.URI.Scheme)}
	}

	cseq, _ := r.Header.CSeq()
	if cseq.Method != r.Method {
		return badRequest(fmt.Errorf("CSeq method %s in a %s request", cseq.Method, r.Method))
	}

	if r.Header.Count("Max-Forwards") > 1 {
		return badRequest(errors.New("several Max-Forwards fields"))
	}

	if r.Header.Has("Max-Forwards") {
		if _, err := r.Header.MaxForwards(); err != nil {
			return badRequest(err)
		}
	}

	return nil
}

// checkVia checks the parameters of a top Via that the transport uses to
// send responses (RFC 3261 §18.2.2, RFC 3581).
func checkVia(v Via) error {
	if r, ok := v.Params.Get("rport"); ok && r != "" {
		if _, err := parsePort(r); err != nil {
			return fmt.Errorf("Via rport: %w", err)
		}
	}

	if r, ok := v.Params.Get("received"); ok {
		a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(r, "["), "]"))
		if err != nil || a.Zone() != "" {
			return fmt.Errorf("Via received %q: not an IP address", r)
		}
	}

	return nil
}

// Validate checks a received response (RFC 3261 §8.1.3, §18.1.2). A
// response that fails is to be dropped. Whether it carries exactly one
// Via depends on the role of the receiver and is left to the caller.
func (r *Response) Validate() error {
	if r.Version != "" && !strings.EqualFold(r.Version, Version) {
		return fmt.Errorf("sip: SIP-Version %s", r.Version)
	}

	if r.StatusCode < 100 || r.StatusCode > 699 {
		return fmt.Errorf("sip: status code %d", r.StatusCode)
	}

	if _, err := r.Header.TopVia(); err != nil {
		return fmt.Errorf("sip: %w", err)
	}

	if err := checkCommon(r.Header); err != nil {
		return fmt.Errorf("sip: %w", err)
	}

	return nil
}

// checkCommon checks the fields that requests and responses share. Only
// the top Via is checked: lower ones belong to other hops.
func checkCommon(fs Header) error {
	for _, name := range []string{"Call-ID", "CSeq", "From", "To"} {
		switch n := fs.Count(name); {
		case n == 0:
			return missing(name)
		case n > 1:
			return fmt.Errorf("several %s fields", name)
		}
	}

	if fs.CallID() == "" {
		return errors.New("empty Call-ID")
	}

	if _, err := fs.CSeq(); err != nil {
		return err
	}

	if _, err := fs.From(); err != nil {
		return err
	}

	if _, err := fs.To(); err != nil {
		return err
	}

	if fs.Count("Content-Length") > 1 {
		return errors.New("several Content-Length fields")
	}

	return nil
}
