package corpus

import (
	"bytes"
	"strings"
)

// scannedField is one header field as it appears on the wire, after unfolding.
type scannedField struct {
	Name  string // canonical long form, e.g. "Via" for "v"
	Value string // value with surrounding whitespace removed
}

// scanned is a message split into its parts without interpreting any
// header. It is the reference the corpus tests compare implementations
// against, so it stays deliberately simple and independent of any parser.
type scanned struct {
	StartLine string
	Fields    []scannedField
	Body      []byte
}

var compactForms = map[string]string{
	"a": "Accept-Contact",
	"b": "Referred-By",
	"c": "Content-Type",
	"d": "Request-Disposition",
	"e": "Content-Encoding",
	"f": "From",
	"i": "Call-ID",
	"j": "Reject-Contact",
	"k": "Supported",
	"l": "Content-Length",
	"m": "Contact",
	"o": "Event",
	"r": "Refer-To",
	"s": "Subject",
	"t": "To",
	"u": "Allow-Events",
	"v": "Via",
	"x": "Session-Expires",
	"y": "Identity",
}

// canonicalName maps a header name to the long form used in fixtures and
// comparisons: compact forms are expanded and case is normalised to the
// usual spelling for the headers the IMS cares about.
func canonicalName(name string) string {
	name = strings.TrimSpace(name)
	if long, ok := compactForms[strings.ToLower(name)]; ok {
		return long
	}

	if c, ok := canonicalSpelling[strings.ToLower(name)]; ok {
		return c
	}

	return name
}

var canonicalSpelling = func() map[string]string {
	m := make(map[string]string)
	for _, n := range []string{
		"Via", "From", "To", "Call-ID", "CSeq", "Contact", "Max-Forwards", "Route", "Record-Route",
		"Content-Length", "Content-Type", "Expires", "Allow", "Supported", "Require", "Proxy-Require",
		"Authorization", "WWW-Authenticate", "Path", "Service-Route", "P-Asserted-Identity",
		"P-Preferred-Identity", "P-Associated-URI", "P-Access-Network-Info", "P-Charging-Vector",
		"P-Visited-Network-ID", "Security-Client", "Security-Server", "Security-Verify", "RSeq", "RAck",
		"Session-Expires", "Event", "Subscription-State", "Accept", "Accept-Contact", "Privacy",
		"P-Preferred-Service", "P-Early-Media", "Reason", "User-Agent", "Server",
	} {
		m[strings.ToLower(n)] = n
	}

	return m
}()

// listHeaders are header fields whose values are comma-separated lists
// (RFC 3261 §7.3.1) and may be split or merged across lines by a proxy.
var listHeaders = map[string]bool{
	"Via": true, "Contact": true, "Route": true, "Record-Route": true, "Path": true,
	"Service-Route": true, "P-Associated-URI": true, "P-Asserted-Identity": true,
	"P-Preferred-Identity": true, "Allow": true, "Supported": true, "Require": true,
	"Proxy-Require": true, "Security-Client": true, "Security-Server": true,
	"Security-Verify": true, "Accept": true, "Accept-Contact": true, "Allow-Events": true,
	"Unsupported": true, "Privacy": true,
}

// isList reports whether the canonical header name holds a comma list.
func isList(name string) bool {
	return listHeaders[name]
}

// scan splits a message into start line, unfolded header fields and body.
// The body is everything after the empty line, cut to Content-Length when
// one is present (RFC 3261 §18.3: trailing octets in a datagram are
// ignored).
func scan(raw []byte) (scanned, bool) {
	head, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		return scanned{}, false
	}

	lines := strings.Split(string(head), "\r\n")

	s := scanned{StartLine: lines[0]}

	for _, line := range lines[1:] {
		if line != "" && (line[0] == ' ' || line[0] == '\t') && len(s.Fields) > 0 {
			last := &s.Fields[len(s.Fields)-1]
			last.Value = strings.TrimSpace(last.Value + " " + strings.TrimSpace(line))

			continue
		}

		name, value, found := strings.Cut(line, ":")
		if !found {
			return scanned{}, false
		}

		s.Fields = append(s.Fields, scannedField{Name: canonicalName(name), Value: strings.TrimSpace(value)})
	}

	s.Body = body

	if cl, ok := s.ContentLength(); ok && cl <= len(body) {
		s.Body = body[:cl]
	}

	return s, true
}

// ContentLength returns the Content-Length value, if present and numeric.
func (s scanned) ContentLength() (int, bool) {
	for _, f := range s.Fields {
		if f.Name != "Content-Length" {
			continue
		}

		n := 0

		for _, c := range f.Value {
			if c < '0' || c > '9' {
				return 0, false
			}

			n = n*10 + int(c-'0')
		}

		return n, true
	}

	return 0, false
}

// values returns every value of the named header in order. For list
// headers, comma-separated values are split into elements.
func (s scanned) values(name string) []string {
	var out []string

	for _, f := range s.Fields {
		if f.Name == name {
			out = append(out, f.Value)
		}
	}

	return elements(name, out)
}

// Names returns the distinct canonical header names in order of first
// appearance.
func (s scanned) Names() []string {
	var names []string

	seen := map[string]bool{}

	for _, f := range s.Fields {
		if !seen[f.Name] {
			seen[f.Name] = true
			names = append(names, f.Name)
		}
	}

	return names
}

// elements normalises the values of one header for comparison: list
// headers are split on top-level commas, and whitespace around each
// element is removed. Other headers are returned as they are.
func elements(name string, values []string) []string {
	if !isList(name) {
		out := make([]string, 0, len(values))
		for _, v := range values {
			out = append(out, strings.TrimSpace(v))
		}

		return out
	}

	var out []string

	for _, v := range values {
		for _, e := range splitTopLevel(v) {
			if e = strings.TrimSpace(e); e != "" {
				out = append(out, e)
			}
		}
	}

	return out
}

// splitTopLevel splits on commas that are not inside a quoted string or
// angle brackets.
func splitTopLevel(v string) []string {
	var (
		out     []string
		start   int
		quoted  bool
		escaped bool
		angle   int
	)

	for i := 0; i < len(v); i++ {
		c := v[i]

		switch {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '<':
			angle++
		case c == '>' && angle > 0:
			angle--
		case c == ',' && angle == 0:
			out = append(out, v[start:i])
			start = i + 1
		}
	}

	return append(out, v[start:])
}
