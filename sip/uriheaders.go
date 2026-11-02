package sip

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// RFC 3261 section 25.1
const hnvMarks = "-_.!~*'()[]/?:+$"

const bodyHname = "body"

// RFC 3261 section 25.1
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

// RFC 3261 section 19.1.1
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

// RFC 3261 section 19.1.1
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

// RFC 3261 section 19.1.4
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

// RFC 3261 section 19.1.5
var unhonoured = []string{
	"From", "Call-ID", "CSeq", "Via", "Record-Route", "Route",
	"Accept", "Accept-Encoding", "Accept-Language", "Allow", "Contact", "Organization", "Supported", "User-Agent",
	"Content-Length",
}

// RFC 3261 section 19.1.5
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

		if isMultipart(body.MediaType()) {
			if _, err := body.Multipart(); err != nil {
				return nil, fmt.Errorf("form a request: %w", err)
			}
		}

		req.SetBodies(body)
	}

	return req, nil
}
