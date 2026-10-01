package corpus

import (
	"bytes"
	"strings"
)

type scannedField struct {
	Name  string
	Value string
}

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

var listHeaders = map[string]bool{
	"Via": true, "Contact": true, "Route": true, "Record-Route": true, "Path": true,
	"Service-Route": true, "P-Associated-URI": true, "P-Asserted-Identity": true,
	"P-Preferred-Identity": true, "Allow": true, "Supported": true, "Require": true,
	"Proxy-Require": true, "Security-Client": true, "Security-Server": true,
	"Security-Verify": true, "Accept": true, "Accept-Contact": true, "Allow-Events": true,
	"Unsupported": true, "Privacy": true,
}

func isList(name string) bool {
	return listHeaders[name]
}

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

func (s scanned) values(name string) []string {
	var out []string

	for _, f := range s.Fields {
		if f.Name == name {
			out = append(out, f.Value)
		}
	}

	return elements(name, out)
}

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
