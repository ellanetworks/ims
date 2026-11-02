package sdp

import (
	"strings"
)

// RFC 8866 section 6.15
// RFC 4855 section 3
type FormatParam struct {
	Name     string
	Value    string
	HasValue bool
}

type FormatParams []FormatParam

func ParseFormatParams(s string) FormatParams {
	var out FormatParams

	for p := range strings.SplitSeq(s, ";") {
		name, value, hasValue := strings.Cut(p, "=")

		name = trimWSP(name)
		if name == "" {
			continue
		}

		out = append(out, FormatParam{Name: name, Value: trimWSP(value), HasValue: hasValue})
	}

	return out
}

// RFC 4855 section 3
func (ps FormatParams) Get(name string) (value string, ok bool) {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p.Value, true
		}
	}

	return "", false
}

func (ps FormatParams) Has(name string) bool {
	_, ok := ps.Get(name)
	return ok
}

func (ps FormatParams) String() string {
	var b strings.Builder

	for i, p := range ps {
		if i > 0 {
			b.WriteByte(';')
		}

		b.WriteString(p.Name)

		if p.HasValue {
			b.WriteByte('=')
			b.WriteString(p.Value)
		}
	}

	return b.String()
}

func (m *Media) FormatParams(format string) (FormatParams, bool) {
	v, ok := m.Fmtp(format)
	if !ok {
		return nil, false
	}

	return ParseFormatParams(v), true
}
