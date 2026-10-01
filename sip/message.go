package sip

import (
	"net/netip"
	"strconv"
	"strings"
)

const Version = "SIP/2.0"

type Transport string

const (
	UDP Transport = "UDP"
	TCP Transport = "TCP"
)

type Flow struct {
	Transport Transport
	Local     netip.AddrPort
	Remote    netip.AddrPort
}

type Message interface {
	StartLine() string

	Env() *Envelope

	Validate() error

	AppendTo(b []byte) []byte

	Bytes() []byte

	String() string

	message()
}

type Envelope struct {
	Header Header

	Body []byte

	Flow Flow
}

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

type Request struct {
	Method string
	URI    URI

	Version string
	Envelope

	line *requestLine
}

type requestLine struct {
	text, method, version string
	uri                   URI
}

func NewRequest(method string, uri URI) *Request {
	r := &Request{Method: method, URI: uri.Clone(), Version: Version}
	r.Header.Add("Content-Length", "0")

	return r
}

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

func (r *Request) Env() *Envelope { return &r.Envelope }

func (r *Request) AppendTo(b []byte) []byte {
	return appendMessage(r.appendStartLine(b), &r.Envelope)
}

func (r *Request) Bytes() []byte { return r.AppendTo(nil) }

func (r *Request) String() string { return string(r.Bytes()) }

func (r *Request) Clone() *Request {
	c := *r
	c.URI = r.URI.Clone()
	c.Envelope = r.clone()

	return &c
}

func (*Request) message() {}

type Response struct {
	Version    string
	StatusCode int
	Reason     string
	Envelope

	line *statusLine
}

type statusLine struct {
	text, version, reason string
	code                  int
}

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

func (r *Response) Env() *Envelope { return &r.Envelope }

func (r *Response) AppendTo(b []byte) []byte {
	return appendMessage(r.appendStartLine(b), &r.Envelope)
}

func (r *Response) Bytes() []byte { return r.AppendTo(nil) }

func (r *Response) String() string { return string(r.Bytes()) }

func (r *Response) Clone() *Response {
	c := *r
	c.Envelope = r.clone()

	return &c
}

func (*Response) message() {}

func (r *Response) IsProvisional() bool { return r.StatusCode < 200 }

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
