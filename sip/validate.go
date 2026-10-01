package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

type StatusError struct {
	StatusCode int
	Err        error
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("sip: %d %s: %v", e.StatusCode, ReasonPhrase(e.StatusCode), e.Err)
}

func (e *StatusError) Unwrap() error { return e.Err }

func badRequest(err error) *StatusError {
	return &StatusError{StatusCode: 400, Err: err}
}

var ErrUnanswerable = errors.New("request cannot be answered")

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

	for _, name := range []string{"Max-Forwards", "Expires", "Content-Type"} {
		if r.Header.Count(name) > 1 {
			return badRequest(fmt.Errorf("several %s fields", name))
		}
	}

	if len(r.Body) > 0 && !r.Header.Has("Content-Type") {
		return badRequest(errors.New("body without Content-Type"))
	}

	if r.Header.Has("Max-Forwards") {
		if _, err := r.Header.MaxForwards(); err != nil {
			return badRequest(err)
		}
	}

	return nil
}

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
