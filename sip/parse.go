package sip

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// maxBodySize bounds Content-Length so that it fits an int everywhere.
const maxBodySize = 1<<31 - 1

// ParseError reports a message that could not be parsed.
type ParseError struct {
	Err error
	// Request is set when the Request-Line could be read, so that a 400
	// can be sent (RFC 3261 §8.2, §16.3). It holds the header fields that
	// could be parsed; any other part may be incomplete.
	Request *Request
}

// Error returns the cause with a "sip: " prefix.
func (e *ParseError) Error() string {
	return "sip: " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *ParseError) Unwrap() error { return e.Err }

// parseErrorf returns a *ParseError; %w wraps a cause as in fmt.Errorf.
func parseErrorf(req *Request, format string, args ...any) *ParseError {
	return &ParseError{Err: fmt.Errorf(format, args...), Request: req}
}

var crlfcrlf = []byte("\r\n\r\n")

// IsKeepalive reports whether a datagram holds only CRLFs, as sent by
// clients keeping a NAT binding open.
func IsKeepalive(data []byte) bool {
	return len(data) > 0 && len(bytes.Trim(data, "\r\n")) == 0
}

// Parse parses one message from a datagram (RFC 3261 §7, §18.3). CRLFs
// before the start line are skipped. With a Content-Length, octets after
// the body are ignored; without one, the body runs to the end of the
// datagram. Parse does not validate the message: see Request.Validate and
// Response.Validate.
func Parse(data []byte) (Message, error) {
	for len(data) >= 2 && data[0] == '\r' && data[1] == '\n' {
		data = data[2:]
	}

	end := bytes.Index(data, crlfcrlf)
	if end < 0 {
		return nil, parseErrorf(nil, "no end of header section")
	}

	start, fields, ferr := splitHead(string(data[:end]))
	body := data[end+len(crlfcrlf):]

	msg, err := newMessage(start, fields)
	if err != nil {
		return nil, err
	}

	if ferr != nil {
		ferr.Request = asRequest(msg)
		return nil, ferr
	}

	if fields.Has("Content-Length") {
		n, err := contentLength(fields)
		if err != nil {
			return nil, parseErrorf(asRequest(msg), "%w", err)
		}

		if n > len(body) {
			return nil, parseErrorf(asRequest(msg), "body shorter than Content-Length")
		}

		body = body[:n]
	}

	setMessageBody(msg, cloneBytes(body))

	return msg, nil
}

// contentLength reads the single Content-Length of a message.
func contentLength(fields Header) (int, error) {
	if fields.Count("Content-Length") > 1 {
		return 0, errors.New("several Content-Length fields")
	}

	return fields.ContentLength()
}

func asRequest(m Message) *Request {
	r, _ := m.(*Request)
	return r
}

func setMessageBody(m Message, body []byte) {
	if len(body) == 0 {
		body = nil
	}

	m.Env().Body = body
}

// splitHead splits a header section, without its final empty line, into
// the start line and unfolded header fields (RFC 3261 §7.3.1). A malformed
// field is skipped and reported after the others are split, so that the
// message can still be framed and answered. The start line is checked by
// newMessage.
func splitHead(head string) (string, Header, *ParseError) {
	start, rest, _ := strings.Cut(head, "\r\n")

	var (
		fields Header
		first  *ParseError
	)

	for rest != "" {
		// A field runs until the next line that does not start with
		// whitespace.
		end := 0
		for {
			i := strings.Index(rest[end:], "\r\n")
			if i < 0 {
				end = len(rest)
				break
			}

			end += i
			if end+2 >= len(rest) || !isWSP(rest[end+2]) {
				break
			}

			end += 2
		}

		f, err := parseField(rest[:end])
		switch {
		case err == nil:
			fields = append(fields, f)
		case first == nil:
			first = err
		}

		rest = strings.TrimPrefix(rest[end:], "\r\n")
	}

	return start, fields, first
}

// parseField parses one header field, continuation lines included.
func parseField(raw string) (Field, *ParseError) {
	if raw == "" || isWSP(raw[0]) {
		return Field{}, parseErrorf(nil, "continuation line without a header field")
	}

	if err := checkFieldOctets(raw); err != nil {
		return Field{}, err
	}

	name, value, ok := strings.Cut(raw, ":")
	if !ok {
		return Field{}, parseErrorf(nil, "header line without colon: %q", truncate(raw))
	}

	name = strings.TrimRight(name, " \t")
	if !isToken(name) {
		return Field{}, parseErrorf(nil, "invalid header name %q", truncate(name))
	}

	if strings.IndexByte(value, '\n') >= 0 || strings.IndexByte(value, '\r') >= 0 {
		value = unfold(value)
	} else {
		value = trimWSP(value)
	}

	f := Field{Name: name, Value: value}

	if len(raw) != len(name)+2+len(value) || raw[len(name):len(name)+2] != ": " || raw[len(name)+2:] != value {
		f.wire = &wireField{text: raw, name: name, value: value}
	}

	return f, nil
}

// checkFieldOctets rejects line breaks that are not folding and control
// characters outside a quoted-pair (RFC 3261 §25.1). A bare CR or LF would
// otherwise travel inside a value, and a peer that ends lines at LF would
// read the rest as another header field.
func checkFieldOctets(raw string) *ParseError {
	quoted := false

	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c == '\r':
			if i+2 >= len(raw) || raw[i+1] != '\n' || !isWSP(raw[i+2]) {
				return parseErrorf(nil, "bare CR in header field")
			}

			i++
		case c == '\n':
			return parseErrorf(nil, "bare LF in header field")
		case quoted && c == '\\':
			// quoted-pair: any octet but CR and LF.
			if i+1 < len(raw) && raw[i+1] != '\r' && raw[i+1] != '\n' {
				i++
			}
		case c == '"':
			quoted = !quoted
		case isCTL(c) && c != '\t':
			return parseErrorf(nil, "control character %q in header field", c)
		}
	}

	return nil
}

// isCTL reports US-ASCII control characters, DEL included.
func isCTL(c byte) bool {
	return c < 0x20 || c == 0x7f
}

// unfold replaces each line break and the whitespace around it with a
// single space.
func unfold(v string) string {
	var b strings.Builder

	for line := range strings.SplitSeq(v, "\r\n") {
		if line = trimWSP(line); line == "" {
			continue
		}

		if b.Len() > 0 {
			b.WriteByte(' ')
		}

		b.WriteString(line)
	}

	return b.String()
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}

	return s
}

// newMessage interprets the start line.
func newMessage(start string, fields Header) (Message, error) {
	if start == "" || isWSP(start[0]) {
		return nil, parseErrorf(nil, "invalid start line")
	}

	for i := range len(start) {
		if isCTL(start[i]) && start[i] != '\t' {
			return nil, parseErrorf(nil, "control character %q in start line", start[i])
		}
	}

	first, rest, _ := strings.Cut(start, " ")

	// A method is a token and cannot hold the '/' of a SIP-Version.
	if strings.IndexByte(first, '/') >= 0 {
		return newResponse(first, rest, fields)
	}

	if !isToken(first) {
		return nil, parseErrorf(nil, "invalid method %q", truncate(first))
	}

	req := &Request{Method: first, Envelope: Envelope{Header: fields}}

	i := strings.LastIndexByte(rest, ' ')
	if i < 0 {
		return nil, parseErrorf(req, "invalid Request-Line")
	}

	req.Version = rest[i+1:]
	if !isVersion(req.Version) {
		return nil, parseErrorf(req, "invalid SIP-Version %q", truncate(req.Version))
	}

	u, err := ParseURI(rest[:i])
	if err != nil {
		return nil, parseErrorf(req, "Request-URI: %w", err)
	}

	req.URI = u
	req.line = &requestLine{text: start, method: req.Method, version: req.Version, uri: u.Clone()}

	return req, nil
}

func newResponse(version, rest string, fields Header) (*Response, error) {
	if !isVersion(version) {
		return nil, parseErrorf(nil, "invalid SIP-Version %q", truncate(version))
	}

	code, reason, _ := strings.Cut(rest, " ")
	if len(code) != 3 || !isDigits(code) || code[0] < '1' || code[0] > '6' {
		return nil, parseErrorf(nil, "invalid status code %q", truncate(code))
	}

	r := &Response{
		Version:    version,
		StatusCode: int(code[0]-'0')*100 + int(code[1]-'0')*10 + int(code[2]-'0'),
		Reason:     reason,
		Envelope:   Envelope{Header: fields},
	}
	r.line = &statusLine{text: version + " " + rest, version: r.Version, code: r.StatusCode, reason: reason}

	return r, nil
}

// isVersion checks SIP-Version = "SIP" "/" 1*DIGIT "." 1*DIGIT, where
// "SIP" is case-insensitive.
func isVersion(v string) bool {
	if len(v) < 4 || !strings.EqualFold(v[:4], "SIP/") {
		return false
	}

	major, minor, ok := strings.Cut(v[4:], ".")

	return ok && isDigits(major) && isDigits(minor)
}
