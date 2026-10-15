package sip

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

const (
	DispositionSession     = "session"
	DispositionRender      = "render"
	DispositionIcon        = "icon"
	DispositionAlert       = "alert"
	DispositionInfoPackage = "info-package"
)

const (
	maxBodyParts = 64
	maxBodyDepth = 4
)

// ParseMediaType parses a Content-Type value (RFC 3261 §20.15, RFC 2045 §5.1). The media type is returned lower-cased,
// parameter values as they appear, quotes included.
func ParseMediaType(s string) (string, Params, error) {
	t := trimWSP(s)

	typ, rest, _ := strings.Cut(t, "/")

	n := tokenLen(rest)
	if typ == "" || !isToken(typ) || n == 0 {
		return "", nil, fmt.Errorf("media type %q: want type/subtype", s)
	}

	ps, err := parseHeaderParams(rest[n:])
	if err != nil {
		return "", nil, fmt.Errorf("media type %q: %w", s, err)
	}

	return strings.ToLower(typ + "/" + rest[:n]), ps, nil
}

func isMultipart(mediaType string) bool {
	return strings.HasPrefix(mediaType, "multipart/")
}

// BodyPart is one MIME body: a part of a multipart body, or the whole body of a message. Header holds its Content-*
// header fields.
type BodyPart struct {
	Header Header
	Body   []byte
}

func NewBodyPart(contentType string, body []byte) BodyPart {
	var h Header
	h.Add("Content-Type", contentType)

	return BodyPart{Header: h, Body: body}
}

// MediaType is the lower-cased media type of the part: text/plain without Content-Type (RFC 2045 §5.2), empty if
// Content-Type is malformed.
func (p BodyPart) MediaType() string {
	if !p.Header.Has("Content-Type") {
		return "text/plain"
	}

	mt, _, err := ParseMediaType(p.Header.ContentType())
	if err != nil {
		return ""
	}

	return mt
}

// ContentID is the Content-ID of the part without angle brackets (RFC 2045 §7).
func (p BodyPart) ContentID() string {
	v := trimWSP(p.Header.Get("Content-ID"))
	if len(v) >= 2 && v[0] == '<' && v[len(v)-1] == '>' {
		v = v[1 : len(v)-1]
	}

	return v
}

func (p *BodyPart) SetContentID(id string) {
	p.Header.Set("Content-ID", "<"+id+">")
}

// NewContentID returns a fresh Content-ID for a part, for use in a cid URL (RFC 2392).
func NewContentID(host string) string {
	return rand.Text() + "@" + host
}

// Disposition is the Content-Disposition of the part, or its default: session for SDP, render otherwise
// (RFC 3261 §20.11, RFC 5621 §3.2).
func (p BodyPart) Disposition() (string, Params, error) {
	v := p.Header.Get("Content-Disposition")
	if v == "" {
		if p.MediaType() == "application/sdp" {
			return DispositionSession, nil, nil
		}

		return DispositionRender, nil, nil
	}

	d, ps, err := ParseTokenParams(v)
	if err != nil {
		return "", nil, fmt.Errorf("Content-Disposition %w", err)
	}

	return strings.ToLower(d), ps, nil
}

// Optional reports whether the part has handling=optional: a recipient that does not understand it can ignore it
// rather than reject the message (RFC 3261 §20.11, RFC 5621 §3.2).
func (p BodyPart) Optional() bool {
	_, ps, err := p.Disposition()
	if err != nil {
		return false
	}

	v, _ := ps.Get("handling")

	return strings.EqualFold(v, "optional")
}

// Multipart parses the part as a multipart body.
func (p BodyPart) Multipart() (*Multipart, error) {
	return ParseMultipart(p.Header.ContentType(), p.Body)
}

// Multipart is a multipart body (RFC 2046 §5.1, RFC 5621).
type Multipart struct {
	Subtype  string
	Boundary string
	Parts    []BodyPart
}

// NewMultipart builds a multipart/mixed body with a fresh boundary. SDP bodies come first, because SIP entities that do
// not understand multipart look at the first body only (TS 24.379 §6.5).
func NewMultipart(parts ...BodyPart) *Multipart {
	ordered := make([]BodyPart, 0, len(parts))
	for _, p := range parts {
		if p.MediaType() == "application/sdp" {
			ordered = append(ordered, p)
		}
	}

	for _, p := range parts {
		if p.MediaType() != "application/sdp" {
			ordered = append(ordered, p)
		}
	}

	return &Multipart{Subtype: "mixed", Boundary: newBoundary(ordered), Parts: ordered}
}

func newBoundary(parts []BodyPart) string {
	for {
		b := "ella-" + rand.Text()
		if !slices.ContainsFunc(parts, func(p BodyPart) bool { return bytes.Contains(p.Body, []byte(b)) }) {
			return b
		}
	}
}

// ParseMultipart parses a multipart body (RFC 2046 §5.1.1). The parts alias body, so that each part keeps its exact
// octets, as signatures over them need (TS 24.379 §6.6.3). The preamble and the epilogue are dropped.
func ParseMultipart(contentType string, body []byte) (*Multipart, error) {
	mt, ps, err := ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}

	if !isMultipart(mt) {
		return nil, fmt.Errorf("multipart: media type %s", mt)
	}

	v, ok := ps.Get("boundary")
	if !ok {
		return nil, errors.New("multipart: no boundary")
	}

	boundary := Unquote(v)
	if !validBoundary(boundary) {
		return nil, fmt.Errorf("multipart: invalid boundary %q", boundary)
	}

	m := &Multipart{Subtype: strings.TrimPrefix(mt, "multipart/"), Boundary: boundary}
	delim := []byte("\r\n--" + boundary)

	// pos is the start of a dash-boundary. The first delimiter may start the body, with no CRLF before it.
	pos := bytes.Index(append([]byte("\r\n"), body...), delim)
	if pos < 0 {
		return nil, errors.New("multipart: no delimiter")
	}

	for {
		pos += len(delim) - 2

		rest := body[pos:]
		if bytes.HasPrefix(rest, []byte("--")) {
			break
		}

		start, ok := delimiterEnd(rest)
		if !ok {
			return nil, errors.New("multipart: delimiter not followed by CRLF")
		}

		start += pos

		end := bytes.Index(body[start:], delim)
		if end < 0 {
			return nil, errors.New("multipart: no close delimiter")
		}

		end += start

		if len(m.Parts) == maxBodyParts {
			return nil, fmt.Errorf("multipart: more than %d parts", maxBodyParts)
		}

		p, err := parsePart(body[start:end])
		if err != nil {
			return nil, err
		}

		m.Parts = append(m.Parts, p)
		pos = end + 2
	}

	if len(m.Parts) == 0 {
		return nil, errors.New("multipart: no body part")
	}

	return m, nil
}

// delimiterEnd skips the transport padding and the CRLF after a delimiter.
func delimiterEnd(b []byte) (int, bool) {
	i := 0
	for i < len(b) && isWSP(b[i]) {
		i++
	}

	if !bytes.HasPrefix(b[i:], []byte("\r\n")) {
		return 0, false
	}

	return i + 2, true
}

func parsePart(b []byte) (BodyPart, error) {
	head, body := b, []byte(nil)

	switch i := bytes.Index(b, []byte("\r\n\r\n")); {
	case bytes.HasPrefix(b, []byte("\r\n")):
		head, body = nil, b[2:]
	case i >= 0:
		head, body = b[:i], b[i+4:]
	}

	fields, perr := parseFields(string(head))
	if perr != nil {
		return BodyPart{}, fmt.Errorf("multipart: part header: %w", perr)
	}

	if len(body) == 0 {
		body = nil
	}

	return BodyPart{Header: fields, Body: body}, nil
}

// RFC 2046 §5.1.1
func validBoundary(s string) bool {
	if s == "" || len(s) > 70 || s[len(s)-1] == ' ' {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if !isAlnum(c) && strings.IndexByte("'()+_,-./:=? ", c) < 0 {
			return false
		}
	}

	return true
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (m *Multipart) ContentType() string {
	sub := m.Subtype
	if sub == "" {
		sub = "mixed"
	}

	boundary := m.Boundary
	if !isToken(boundary) {
		boundary = Quote(boundary)
	}

	return "multipart/" + sub + ";boundary=" + boundary
}

func (m *Multipart) AppendTo(b []byte) []byte {
	for _, p := range m.Parts {
		b = append(b, "--"...)
		b = append(b, m.Boundary...)
		b = append(b, "\r\n"...)

		for _, f := range p.Header {
			b = f.appendTo(b)
			b = append(b, "\r\n"...)
		}

		b = append(b, "\r\n"...)
		b = append(b, p.Body...)
		b = append(b, "\r\n"...)
	}

	b = append(b, "--"...)
	b = append(b, m.Boundary...)

	return append(b, "--\r\n"...)
}

func (m *Multipart) Bytes() []byte {
	return m.AppendTo(nil)
}

// Find returns the first part of the media type, looking into nested multipart bodies.
func (m *Multipart) Find(mediaType string) (BodyPart, bool) {
	mediaType = strings.ToLower(mediaType)

	return m.find(func(p BodyPart) bool { return p.MediaType() == mediaType }, 0)
}

// ByContentID returns the part a cid URL (RFC 2392) or a Content-ID refers to, looking into nested multipart bodies.
// A part whose own content is multipart can be the target, as with REFER to a list of bodies (TS 24.379 §17.2.2.2.1).
func (m *Multipart) ByContentID(id string) (BodyPart, bool) {
	id, ok := normalizeContentID(id)
	if !ok {
		return BodyPart{}, false
	}

	return m.find(func(p BodyPart) bool { return p.ContentID() == id }, 0)
}

func (m *Multipart) find(match func(BodyPart) bool, depth int) (BodyPart, bool) {
	for _, p := range m.Parts {
		if match(p) {
			return p, true
		}

		if depth+1 < maxBodyDepth && isMultipart(p.MediaType()) {
			if nested, err := p.Multipart(); err == nil {
				if found, ok := nested.find(match, depth+1); ok {
					return found, true
				}
			}
		}
	}

	return BodyPart{}, false
}

func normalizeContentID(id string) (string, bool) {
	id = trimWSP(id)

	if len(id) > 4 && strings.EqualFold(id[:4], "cid:") {
		u, err := url.PathUnescape(id[4:])
		if err != nil {
			return "", false
		}

		return u, true
	}

	if len(id) >= 2 && id[0] == '<' && id[len(id)-1] == '>' {
		id = id[1 : len(id)-1]
	}

	return id, id != ""
}

func isContentField(name string) bool {
	long := strings.ToLower(LongName(name))
	return strings.HasPrefix(long, "content-") && long != "content-length"
}

// part is the whole body of the message as one part, with the Content-* header fields of the message.
func (e Envelope) part() BodyPart {
	var h Header

	for _, f := range e.Header {
		if isContentField(f.Name) {
			h = append(h, Field{Name: f.Name, Value: f.Value})
		}
	}

	return BodyPart{Header: h, Body: e.Body}
}

// Bodies returns the bodies of the message: the parts of a multipart body, or the body itself. Nested multipart
// parts are returned as they are.
func (e Envelope) Bodies() ([]BodyPart, error) {
	if len(e.Body) == 0 {
		return nil, nil
	}

	p := e.part()
	if !isMultipart(p.MediaType()) {
		return []BodyPart{p}, nil
	}

	m, err := p.Multipart()
	if err != nil {
		return nil, err
	}

	return m.Parts, nil
}

// FindBody returns the first body of the media type, looking into multipart bodies.
func (e Envelope) FindBody(mediaType string) (BodyPart, bool, error) {
	if len(e.Body) == 0 {
		return BodyPart{}, false, nil
	}

	p := e.part()

	mt := p.MediaType()
	if !isMultipart(mt) {
		return p, mt == strings.ToLower(mediaType), nil
	}

	m, err := p.Multipart()
	if err != nil {
		return BodyPart{}, false, err
	}

	found, ok := m.Find(mediaType)

	return found, ok, nil
}

// SetBodies replaces the body of the message and its Content-* header fields. One part is sent as is, several as a
// multipart/mixed body (TS 24.379 §6.5).
func (e *Envelope) SetBodies(parts ...BodyPart) {
	switch len(parts) {
	case 0:
		e.SetBody("", nil)
	case 1:
		e.SetBody(parts[0].Header.ContentType(), parts[0].Body)

		for _, f := range parts[0].Header {
			if isContentField(f.Name) && !strings.EqualFold(LongName(f.Name), "Content-Type") {
				e.Header.Add(f.Name, f.Value)
			}
		}
	default:
		m := NewMultipart(parts...)
		e.SetBody(m.ContentType(), m.Bytes())
	}
}
