package sip

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// hnvMarks are the characters other than alphanumerics allowed unescaped in an hname or an hvalue: the marks of
// unreserved and hnv-unreserved (RFC 3261 §25.1).
const hnvMarks = "-_.!~*'()[]/?:+$"

const bodyHname = "body"

// RFC 3261 §25.1: headers = "?" header *( "&" header ), header = hname "=" hvalue.
func checkURIHeaders(s string) error {
	for h := range strings.SplitSeq(s, "&") {
		name, value, ok := strings.Cut(h, "=")
		if !ok || name == "" {
			return fmt.Errorf("header %q: want hname=hvalue", h)
		}

		if err := checkEscaped(name, hnvMarks); err != nil {
			return fmt.Errorf("hname %q: %w", name, err)
		}

		if err := checkEscaped(value, hnvMarks); err != nil {
			return fmt.Errorf("hvalue of %s: %w", name, err)
		}
	}

	return nil
}

// Embedded returns the header fields and the body the headers component of a SIP URI asks to put in a request formed
// from it, unescaped (RFC 3261 §19.1.1). The special hname "body" is the body, described by the Content-* fields.
func (u URI) Embedded() (Envelope, error) {
	var e Envelope

	if u.Headers == "" {
		return e, nil
	}

	for h := range strings.SplitSeq(u.Headers, "&") {
		rawName, rawValue, ok := strings.Cut(h, "=")
		if !ok || rawName == "" {
			return Envelope{}, fmt.Errorf("URI header %q: want hname=hvalue", h)
		}

		name, err := url.PathUnescape(rawName)
		if err != nil || !isToken(name) {
			return Envelope{}, fmt.Errorf("URI hname %q: invalid header name", rawName)
		}

		value, err := url.PathUnescape(rawValue)
		if err != nil {
			return Envelope{}, fmt.Errorf("URI hvalue of %s: %w", name, err)
		}

		if strings.EqualFold(name, bodyHname) {
			if e.Body != nil {
				return Envelope{}, errors.New("URI headers: two bodies")
			}

			e.Body = []byte(value)

			continue
		}

		if hasCTL(value) {
			return Envelope{}, fmt.Errorf("URI hvalue of %s: control character", name)
		}

		e.Header.Add(name, value)
	}

	if len(e.Body) == 0 {
		e.Body = nil
	}

	return e, nil
}

// SetEmbedded replaces the headers component of a SIP URI with the header fields and the body of e, escaped
// (RFC 3261 §19.1.1). Content-Length is left out: the body is its own length.
func (u *URI) SetEmbedded(e Envelope) {
	var b []byte

	add := func(name, value string) {
		if len(b) > 0 {
			b = append(b, '&')
		}

		b = appendHnv(b, name)
		b = append(b, '=')
		b = appendHnv(b, value)
	}

	for _, f := range e.Header {
		if !strings.EqualFold(LongName(f.Name), "Content-Length") {
			add(f.Name, f.Value)
		}
	}

	if len(e.Body) > 0 {
		add(bodyHname, string(e.Body))
	}

	u.Headers = string(b)
}

func appendHnv(b []byte, s string) []byte {
	const hex = "0123456789ABCDEF"

	for i := range len(s) {
		c := s[i]
		if isAlpha(c) || '0' <= c && c <= '9' || strings.IndexByte(hnvMarks, c) >= 0 {
			b = append(b, c)
			continue
		}

		b = append(b, '%', hex[c>>4], hex[c&0xf])
	}

	return b
}

// RFC 3261 §19.1.4: header components are never ignored, and match field by field, in any order. Values match
// case-insensitively, bodies exactly.
func uriHeadersEquivalent(a, b string) bool {
	if a == b {
		return true
	}

	ea, err := URI{Headers: a}.Embedded()
	if err != nil {
		return false
	}

	eb, err := URI{Headers: b}.Embedded()
	if err != nil || len(ea.Header) != len(eb.Header) || string(ea.Body) != string(eb.Body) {
		return false
	}

	rest := slices.Clone(eb.Header)

	for _, f := range ea.Header {
		i := slices.IndexFunc(rest, func(g Field) bool {
			return strings.EqualFold(LongName(f.Name), LongName(g.Name)) && strings.EqualFold(f.Value, g.Value)
		})
		if i < 0 {
			return false
		}

		rest = slices.Delete(rest, i, i+1)
	}

	return true
}

// unhonoured are the header fields a request formed from a URI leaves out: the dangerous ones, Route, and the ones
// that would advertise the capabilities or the location of the sender in its stead (RFC 3261 §19.1.5).
var unhonoured = []string{
	"From", "Call-ID", "CSeq", "Via", "Record-Route", "Route",
	"Accept", "Accept-Encoding", "Accept-Language", "Allow", "Contact", "Organization", "Supported", "User-Agent",
	"Content-Length",
}

// FormRequest forms a request from a SIP URI (RFC 3261 §19.1.5): the method parameter, or method when there is none,
// the URI without its method parameter and headers component as Request-URI, and the header fields and the body
// of the headers component, except those that must not be honoured. The caller adds the From, To, Call-ID, CSeq,
// Via, Max-Forwards and Contact header fields.
func FormRequest(u URI, method string) (*Request, error) {
	if !u.IsSIP() {
		return nil, fmt.Errorf("form a request from %s: not a SIP URI", u.Scheme)
	}

	e, err := u.Embedded()
	if err != nil {
		return nil, err
	}

	target := u.Clone()
	target.Headers = ""

	if raw, ok := target.Params.Get("method"); ok {
		m, err := url.PathUnescape(raw)
		if err != nil || !isToken(m) {
			return nil, fmt.Errorf("form a request: invalid method %q", raw)
		}

		method = m
		target.Params.Del("method")
	}

	req := NewRequest(method, target)

	for _, f := range e.Header {
		if !isContentField(f.Name) && !slices.ContainsFunc(unhonoured, func(n string) bool { return strings.EqualFold(LongName(f.Name), n) }) {
			req.Header.Add(f.Name, f.Value)
		}
	}

	if e.Body != nil {
		body := e.Content()
		if !body.Header.Has("Content-Type") {
			return nil, errors.New("form a request: body without Content-Type")
		}

		// RFC 3261 §19.1.5: descriptive header fields are verified, here that a multipart body is one.
		if isMultipart(body.MediaType()) {
			if _, err := body.Multipart(); err != nil {
				return nil, fmt.Errorf("form a request: %w", err)
			}
		}

		req.SetBodies(body)
	}

	return req, nil
}
