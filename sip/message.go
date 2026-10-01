package sip

import (
	"net/netip"
	"strconv"
	"strings"
)

// Version is the only SIP version this package speaks.
const Version = "SIP/2.0"

// Transport is a SIP transport protocol.
type Transport string

// The transports a Flow can use (IR.92 §2.2.1: phones use UDP and TCP).
const (
	UDP Transport = "UDP"
	TCP Transport = "TCP"
)

// Flow is a transport association between one local socket and a peer.
// Over TCP it names exactly one connection.
type Flow struct {
	Transport Transport
	Local     netip.AddrPort
	Remote    netip.AddrPort
}

// Message is a *Request or a *Response. It cannot be implemented outside
// this package, so that methods can be added to it.
type Message interface {
	// StartLine returns the Request-Line or Status-Line, without CRLF.
	StartLine() string
	// Env returns the header, body and flow of the message.
	Env() *Envelope
	// Validate checks a received message. See Request.Validate and
	// Response.Validate.
	Validate() error
	// AppendTo appends the message in wire form to b.
	AppendTo(b []byte) []byte
	// Bytes returns the message in wire form.
	Bytes() []byte
	// String returns the message in wire form.
	String() string

	message()
}

// Envelope is what requests and responses share.
type Envelope struct {
	Header Header
	// Body is the message body. Serialization writes it as it is: use
	// SetBody to keep Content-Type and Content-Length in step.
	Body []byte
	// Flow is the flow the message was received on, or is to be sent on.
	Flow Flow
}

// SetBody sets the body together with its Content-Type and
// Content-Length (RFC 3261 §20.14, §20.15). An empty contentType removes
// Content-Type.
func (e *Envelope) SetBody(contentType string, body []byte) {
	if contentType == "" {
		e.Header.Del("Content-Type")
	} else {
		e.Header.Set("Content-Type", contentType)
	}

	e.Header.Set("Content-Length", strconv.Itoa(len(body)))

	if len(body) == 0 {
		body = nil
	}

	e.Body = body
}

func (e Envelope) clone() Envelope {
	e.Header = e.Header.Clone()
	e.Body = cloneBytes(e.Body)

	return e
}

// Request is a SIP request.
type Request struct {
	Method string
	URI    URI
	// Version is the SIP-Version as received. An empty Version is sent as
	// SIP/2.0.
	Version string
	Envelope

	// line is the Request-Line as received, written back while Method,
	// URI and Version are unchanged.
	line *requestLine
}

type requestLine struct {
	text, method, version string
	uri                   URI
}

// NewRequest returns a request whose only header field is
// Content-Length: 0. Use SetBody to add a body.
func NewRequest(method string, uri URI) *Request {
	r := &Request{Method: method, URI: uri.Clone(), Version: Version}
	r.Header.Add("Content-Length", "0")

	return r
}

// StartLine returns the Request-Line, without CRLF.
func (r *Request) StartLine() string {
	return string(r.appendStartLine(nil))
}

func (r *Request) appendStartLine(b []byte) []byte {
	if l := r.line; l != nil && l.method == r.Method && l.version == r.Version && l.uri.equal(r.URI) {
		return append(b, l.text...)
	}

	b = appendText(b, r.Method)
	b = append(b, ' ')
	b = r.URI.appendTo(b)
	b = append(b, ' ')

	return appendVersion(b, r.Version)
}

// Env returns the header, body and flow of the request.
func (r *Request) Env() *Envelope { return &r.Envelope }

// AppendTo appends the request in wire form to b.
func (r *Request) AppendTo(b []byte) []byte {
	return appendMessage(r.appendStartLine(b), &r.Envelope)
}

// Bytes returns the request in wire form.
func (r *Request) Bytes() []byte { return r.AppendTo(nil) }

// String returns the request in wire form.
func (r *Request) String() string { return string(r.Bytes()) }

// Clone returns a deep copy of the request.
func (r *Request) Clone() *Request {
	c := *r
	c.URI = r.URI.Clone()
	c.Envelope = r.clone()

	return &c
}

func (*Request) message() {}

// Response is a SIP response.
type Response struct {
	// Version is the SIP-Version as received. An empty Version is sent as
	// SIP/2.0.
	Version    string
	StatusCode int
	Reason     string
	Envelope

	// line is the Status-Line as received, written back while Version,
	// StatusCode and Reason are unchanged.
	line *statusLine
}

type statusLine struct {
	text, version, reason string
	code                  int
}

// StartLine returns the Status-Line, without CRLF.
func (r *Response) StartLine() string {
	return string(r.appendStartLine(nil))
}

func (r *Response) appendStartLine(b []byte) []byte {
	if l := r.line; l != nil && l.version == r.Version && l.code == r.StatusCode && l.reason == r.Reason {
		return append(b, l.text...)
	}

	b = appendVersion(b, r.Version)
	b = append(b, ' ')
	b = strconv.AppendInt(b, int64(r.StatusCode), 10)
	b = append(b, ' ')

	return appendText(b, r.Reason)
}

// Env returns the header, body and flow of the response.
func (r *Response) Env() *Envelope { return &r.Envelope }

// AppendTo appends the response in wire form to b.
func (r *Response) AppendTo(b []byte) []byte {
	return appendMessage(r.appendStartLine(b), &r.Envelope)
}

// Bytes returns the response in wire form.
func (r *Response) Bytes() []byte { return r.AppendTo(nil) }

// String returns the response in wire form.
func (r *Response) String() string { return string(r.Bytes()) }

// Clone returns a deep copy of the response.
func (r *Response) Clone() *Response {
	c := *r
	c.Envelope = r.clone()

	return &c
}

func (*Response) message() {}

// IsProvisional reports whether r is a 1xx response.
func (r *Response) IsProvisional() bool { return r.StatusCode < 200 }

// IsSuccess reports whether r is a 2xx response.
func (r *Response) IsSuccess() bool { return r.StatusCode >= 200 && r.StatusCode < 300 }

func appendVersion(b []byte, v string) []byte {
	if v == "" {
		v = Version
	}

	return append(b, v...)
}

func appendMessage(b []byte, e *Envelope) []byte {
	b = append(b, "\r\n"...)
	b = e.Header.appendTo(b)
	b = append(b, "\r\n"...)

	return append(b, e.Body...)
}

// appendText appends s with CR and LF replaced by spaces, so that a value
// set by the caller cannot break a line (as net/http does for headers).
func appendText(b []byte, s string) []byte {
	if !strings.ContainsAny(s, "\r\n") {
		return append(b, s...)
	}

	for i := range len(s) {
		if c := s[i]; c == '\r' || c == '\n' {
			b = append(b, ' ')
		} else {
			b = append(b, c)
		}
	}

	return b
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}

	return append([]byte(nil), b...)
}
