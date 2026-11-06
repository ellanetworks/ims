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
	DispositionSession      = "session"
	DispositionEarlySession = "early-session"
	DispositionRender       = "render"
	DispositionIcon         = "icon"
	DispositionAlert        = "alert"
	DispositionInfoPackage  = "info-package"
	DispositionByReference  = "by-reference"
)

const (
	maxBodyParts = 64
	maxBodyDepth = 4
)

// RFC 3261 section 20.15
// RFC 2045 section 5.1
func ParseMediaType(s string) (string, Params, error) {
	typ, rest, _ := strings.Cut(trimWSP(s), "/")
	typ, rest = strings.TrimRight(typ, " \t"), trimLeftWSP(rest)

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

type BodyPart struct {
	Header Header
	Body   []byte
}

func NewBodyPart(contentType string, body []byte) BodyPart {
	var h Header
	h.Add("Content-Type", contentType)

	return BodyPart{Header: h, Body: body}
}

// RFC 2045 section 5.2
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

// RFC 2045 section 7
// RFC 5322 section 3.6.4
func (p BodyPart) ContentID() string {
	v := trimWSP(p.Header.Get("Content-ID"))

	if i := strings.IndexByte(v, '<'); i >= 0 {
		if j := strings.IndexByte(v[i:], '>'); j >= 0 {
			return v[i+1 : i+j]
		}
	}

	return v
}

func (p *BodyPart) SetContentID(id string) {
	p.Header.Set("Content-ID", "<"+id+">")
}

func NewContentID(host string) string {
	return rand.Text() + "@" + host
}

// RFC 3261 section 20.11
// RFC 5621 section 8.2
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

// RFC 3261 section 20.11
// RFC 5621 section 8.1
// RFC 5621 section 8.2
func (p BodyPart) Optional() bool {
	_, ps, err := p.Disposition()
	if err != nil {
		return false
	}

	v, _ := ps.Get("handling")

	return strings.EqualFold(v, "optional")
}

func (p BodyPart) IsSession() bool {
	d, _, err := p.Disposition()
	return err == nil && d == DispositionSession && p.MediaType() == "application/sdp"
}

func (p BodyPart) Multipart() (*Multipart, error) {
	return parseMultipart(p.Header.ContentType(), p.Body)
}

var ErrUnsupportedEncoding = errors.New("sip: unsupported content encoding")

// RFC 3261 section 8.2.3
func (p BodyPart) Encoding() error {
	return p.checkEncoding()
}

// RFC 3261 section 7.4
func (p BodyPart) checkEncoding() error {
	if p.Header.Has("Content-Type") && p.MediaType() == "" {
		return fmt.Errorf("malformed Content-Type %q", p.Header.ContentType())
	}

	for _, v := range p.Header.Values("Content-Encoding") {
		for _, c := range strings.Split(v, ",") {
			if c = trimWSP(c); c != "" && !strings.EqualFold(c, "identity") {
				return fmt.Errorf("%w: Content-Encoding %q", ErrUnsupportedEncoding, c)
			}
		}
	}

	switch cte := strings.ToLower(trimWSP(p.Header.Get("Content-Transfer-Encoding"))); cte {
	case "", "binary", "8bit", "7bit":
		return nil
	default:
		return fmt.Errorf("unsupported Content-Transfer-Encoding %q", cte)
	}
}

// RFC 2046 section 5.1
// RFC 5621
type Multipart struct {
	Subtype string
	Parts   []BodyPart

	boundary string
}

// TS 24.379 clause 6.5
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

	return &Multipart{Subtype: "mixed", Parts: ordered}
}

// RFC 2046 section 5.1.1
func collides(b string, body []byte) bool {
	dash := "--" + b
	return bytes.HasPrefix(body, []byte(dash)) || bytes.Contains(body, []byte("\r\n"+dash))
}

func newBoundary(parts []BodyPart) string {
	for {
		b := "ella-" + rand.Text()
		if !slices.ContainsFunc(parts, func(p BodyPart) bool { return collides(b, p.Body) }) {
			return b
		}
	}
}

// RFC 2046 section 5.1.1
func parseMultipart(contentType string, body []byte) (*Multipart, error) {
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

	m := &Multipart{Subtype: strings.TrimPrefix(mt, "multipart/"), boundary: boundary}
	dash := []byte("--" + boundary)
	delim := []byte("\r\n--" + boundary)

	pos := 0
	if !bytes.HasPrefix(body, dash) {
		if pos = bytes.Index(body, delim); pos < 0 {
			return nil, errors.New("multipart: no delimiter")
		}

		pos += 2
	}

	for {
		pos += len(dash)

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

		p, err := parsePart(body[start:end:end])
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

// RFC 2046 section 5.1.1
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

func (m *Multipart) Encode() (contentType string, body []byte) {
	if !validBoundary(m.boundary) || slices.ContainsFunc(m.Parts, func(p BodyPart) bool { return collides(m.boundary, p.Body) }) {
		m.boundary = newBoundary(m.Parts)
	}

	for _, p := range m.Parts {
		body = append(body, "--"...)
		body = append(body, m.boundary...)
		body = append(body, "\r\n"...)

		for _, f := range p.Header {
			body = f.appendTo(body)
			body = append(body, "\r\n"...)
		}

		body = append(body, "\r\n"...)
		body = append(body, p.Body...)
		body = append(body, "\r\n"...)
	}

	body = append(body, "--"...)
	body = append(body, m.boundary...)
	body = append(body, "--\r\n"...)

	sub := m.Subtype
	if sub == "" {
		sub = "mixed"
	}

	boundary := m.boundary
	if !isToken(boundary) {
		boundary = Quote(boundary)
	}

	return "multipart/" + sub + ";boundary=" + boundary, body
}

// RFC 3261 section 8.2.3
// RFC 5621 section 9.3
func (p BodyPart) Parts() ([]BodyPart, error) {
	parts, err := p.Split()
	if err != nil {
		return nil, err
	}

	var usable []BodyPart

	for _, part := range parts {
		switch err := part.checkEncoding(); {
		case err == nil:
			usable = append(usable, part)
		case !part.Optional():
			return nil, err
		}
	}

	return usable, nil
}

// RFC 2046 section 5.1
func (p BodyPart) Split() ([]BodyPart, error) {
	if len(p.Body) == 0 {
		return nil, nil
	}

	if !isMultipart(p.MediaType()) {
		return []BodyPart{p}, nil
	}

	if err := p.checkEncoding(); err != nil {
		return nil, err
	}

	m, err := p.Multipart()
	if err != nil {
		return nil, err
	}

	return m.Parts, nil
}

// RFC 5621 section 9.3
func (p BodyPart) Find(mediaType string) (BodyPart, bool, error) {
	mediaType = strings.ToLower(mediaType)

	return p.FindFunc(func(q BodyPart) bool { return q.MediaType() == mediaType })
}

// RFC 2046 section 5.1.4
// RFC 5621 section 6
// RFC 5621 section 9.4
func (p BodyPart) FindFunc(match func(BodyPart) bool) (BodyPart, bool, error) {
	if len(p.Body) == 0 {
		return BodyPart{}, false, nil
	}

	return p.find(match, false, 0)
}

// RFC 2392
// TS 24.379 clause 17.2.2.2.1
func (p BodyPart) ByContentID(id string) (BodyPart, bool, error) {
	id, ok := normalizeContentID(id)
	if !ok || len(p.Body) == 0 {
		return BodyPart{}, false, nil
	}

	return p.find(func(q BodyPart) bool { return q.ContentID() == id }, true, 0)
}

func (m *Multipart) find(match func(BodyPart) bool, byReference bool, depth int) (BodyPart, bool, error) {
	parts := m.Parts
	if !byReference && strings.EqualFold(m.Subtype, "alternative") {
		parts = slices.Clone(parts)
		slices.Reverse(parts)
	}

	for _, p := range parts {
		if found, ok, err := p.find(match, byReference, depth); err != nil || ok {
			return found, ok, err
		}
	}

	return BodyPart{}, false, nil
}

// RFC 5621 section 9.3
func (p BodyPart) find(match func(BodyPart) bool, byReference bool, depth int) (BodyPart, bool, error) {
	if !byReference {
		if d, _, err := p.Disposition(); err == nil && d == DispositionByReference {
			return BodyPart{}, false, nil
		}
	}

	if err := p.checkEncoding(); err != nil {
		if p.Optional() {
			return BodyPart{}, false, nil
		}

		return BodyPart{}, false, err
	}

	if match(p) {
		return p, true, nil
	}

	if !isMultipart(p.MediaType()) || depth >= maxBodyDepth {
		return BodyPart{}, false, nil
	}

	m, err := p.Multipart()
	if err != nil {
		return BodyPart{}, false, err
	}

	return m.find(match, byReference, depth+1)
}

func normalizeContentID(id string) (string, bool) {
	id = trimWSP(id)

	if len(id) >= 2 && id[0] == '<' && id[len(id)-1] == '>' {
		id = id[1 : len(id)-1]
	}

	if len(id) >= 4 && strings.EqualFold(id[:4], "cid:") {
		u, err := url.PathUnescape(id[4:])
		if err != nil {
			return "", false
		}

		id = u
	}

	return id, id != ""
}

func isContentField(name string) bool {
	long := strings.ToLower(LongName(name))
	return strings.HasPrefix(long, "content-") && long != "content-length"
}

func (e Envelope) Content() BodyPart {
	var h Header

	for _, f := range e.Header {
		if isContentField(f.Name) {
			h = append(h, Field{Name: f.Name, Value: f.Value})
		}
	}

	return BodyPart{Header: h, Body: e.Body[:len(e.Body):len(e.Body)]}
}

// TS 24.379 clause 6.5
// RFC 5621 section 8.2
func (e *Envelope) SetBodies(parts ...BodyPart) {
	switch len(parts) {
	case 0:
		e.SetBody("", nil)
	case 1:
		ct := parts[0].Header.ContentType()
		if ct == "" {
			ct = "text/plain"
		}

		e.SetBody(ct, parts[0].Body)

		for _, f := range parts[0].Header {
			if isContentField(f.Name) && !strings.EqualFold(LongName(f.Name), "Content-Type") {
				e.Header.Add(f.Name, f.Value)
			}
		}
	default:
		e.SetBody(NewMultipart(parts...).Encode())
	}
}
