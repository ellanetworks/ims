package sdp

import (
	"strings"
)

// FormatParam is a format-specific parameter of an fmtp attribute in its common form: a name, with or without a value
// after "=" (RFC 8866 §6.15, RFC 4855 §3): "mode-set=0,2", "mc_granted".
type FormatParam struct {
	Name     string
	Value    string
	HasValue bool
}

// FormatParams are the parameters of an fmtp attribute, in order.
type FormatParams []FormatParam

// ParseFormatParams parses parameters separated by ";", the whitespace around names and values ignored, and the
// parameters without a name left out. A format whose definition uses other separators maps them to ";" first.
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

// Get is the value of the parameter name, compared case-insensitively (RFC 4855 §3); ok is false without it.
func (ps FormatParams) Get(name string) (value string, ok bool) {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p.Value, true
		}
	}

	return "", false
}

// Has reports whether the parameter name is there, with or without a value.
func (ps FormatParams) Has(name string) bool {
	_, ok := ps.Get(name)
	return ok
}

// String is the parameters as an fmtp attribute carries them after the format: separated by ";", without spaces.
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

// FormatParams are the parameters of the fmtp attribute of a format of the media; ok is false without one.
func (m *Media) FormatParams(format string) (FormatParams, bool) {
	v, ok := m.Fmtp(format)
	if !ok {
		return nil, false
	}

	return ParseFormatParams(v), true
}
