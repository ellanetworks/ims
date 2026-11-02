package sip

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// RFC 3840 section 5
// RFC 3840 section 10
var baseFeatureTags = []string{
	"audio", "automata", "class", "duplex", "data", "control", "mobility", "description", "events", "priority",
	"methods", "schemes", "application", "video", "language", "type", "isfocus", "actor", "text", "extensions",
}

// TS 24.229 clause 7.2A.8.2
// TS 24.229 clause 7.2A.9.2
var urnFeatureTags = []string{"g.3gpp.icsi-ref", "g.3gpp.iari-ref"}

type FeatureKind int

const (
	FeatureToken FeatureKind = iota
	FeatureString
	FeatureNumeric
)

// RFC 3840 section 5
// RFC 3840 section 9
type FeatureValue struct {
	Negated bool
	Kind    FeatureKind

	Text string

	Min, Max float64
}

// RFC 3840 section 5
type FeatureTerm struct {
	Tag    string
	Values []FeatureValue
}

// RFC 3840 section 9
// RFC 3841 section 10
// RFC 6809 section 6
type FeatureSet []FeatureTerm

func NewFeature(tag string, tokens ...string) FeatureTerm {
	if len(tokens) == 0 {
		tokens = []string{"TRUE"}
	}

	t := FeatureTerm{Tag: normalizeFeatureTag(tag)}
	for _, v := range tokens {
		t.Values = append(t.Values, FeatureValue{Kind: FeatureToken, Text: v})
	}

	return t
}

func NewStringFeature(tag, s string) FeatureTerm {
	return FeatureTerm{Tag: normalizeFeatureTag(tag), Values: []FeatureValue{{Kind: FeatureString, Text: s}}}
}

func normalizeFeatureTag(tag string) string {
	if strings.HasPrefix(tag, "+") || isBaseFeatureTag(tag) {
		return featureTag(tag)
	}

	return strings.ToLower(tag)
}

func isBaseFeatureTag(name string) bool {
	return slices.ContainsFunc(baseFeatureTags, func(b string) bool { return strings.EqualFold(b, name) })
}

// RFC 3841 section 7.2.1
func IsFeatureParam(name string) bool {
	return strings.HasPrefix(name, "+") || isBaseFeatureTag(name)
}

// RFC 3840 section 5
// RFC 6809 section 6.3
func featureTag(param string) string {
	name := strings.ToLower(param)

	if rest, ok := strings.CutPrefix(name, "+"); ok {
		return strings.NewReplacer("'", "/", "!", ":").Replace(rest)
	}

	if name == "language" || name == "type" {
		return name
	}

	return "sip." + name
}

// RFC 3840 section 5
func featureParam(tag string) string {
	if rest, ok := strings.CutPrefix(tag, "sip."); ok && isBaseFeatureTag(rest) {
		return rest
	}

	if tag == "language" || tag == "type" {
		return tag
	}

	return "+" + strings.NewReplacer("/", "'", ":", "!").Replace(tag)
}

// RFC 3841 section 7.2.3
func ParseFeatureSet(ps Params) (FeatureSet, error) {
	var (
		fs   FeatureSet
		errs []error
	)

	for _, base := range []bool{true, false} {
		for _, p := range ps {
			if strings.HasPrefix(p.Name, "+") == base || !IsFeatureParam(p.Name) {
				continue
			}

			if !base {
				rest := p.Name[1:]
				if !isFtagName(rest) {
					errs = append(errs, fmt.Errorf("invalid feature parameter %q", p.Name))
					continue
				}

				if isBaseFeatureTag(rest) && fs.Has(featureTag(rest)) {
					continue
				}
			}

			tag := featureTag(p.Name)
			if fs.Has(tag) {
				continue
			}

			values, err := parseFeatureValues(tag, p.Value)
			if err != nil {
				errs = append(errs, fmt.Errorf("feature parameter %s: %w", p.Name, err))
				continue
			}

			fs = append(fs, FeatureTerm{Tag: tag, Values: values})
		}
	}

	return fs, errors.Join(errs...)
}

// RFC 3840 section 9
func parseFeatureValues(tag, v string) ([]FeatureValue, error) {
	if v == "" {
		return []FeatureValue{{Kind: FeatureToken, Text: "TRUE"}}, nil
	}

	inner := v
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		inner = v[1 : len(v)-1]
	}

	if strings.HasPrefix(inner, "<") {
		text, err := parseFeatureString(inner)
		if err != nil {
			return nil, err
		}

		return []FeatureValue{{Kind: FeatureString, Text: text}}, nil
	}

	var out []FeatureValue

	for s := range strings.SplitSeq(inner, ",") {
		fv, err := parseFeatureValue(tag, trimWSP(s))
		if err != nil {
			return nil, err
		}

		out = append(out, fv)
	}

	return out, nil
}

// RFC 3840 section 9
func parseFeatureString(raw string) (string, error) {
	if len(raw) < 2 || raw[len(raw)-1] != '>' {
		return "", fmt.Errorf("unterminated string-value %q", raw)
	}

	body := raw[1 : len(raw)-1]

	var b strings.Builder

	for i := 0; i < len(body); i++ {
		c := body[i]

		switch {
		case c == '\\':
			if i+1 == len(body) || body[i+1] == '\r' || body[i+1] == '\n' {
				return "", fmt.Errorf("string-value %q: invalid quoted-pair", raw)
			}

			i++
			b.WriteByte(body[i])
		case c == '<' || c == '>' || c == '"' || isTextCTL(c):
			return "", fmt.Errorf("invalid string-value %q", raw)
		default:
			b.WriteByte(c)
		}
	}

	return b.String(), nil
}

func parseFeatureValue(tag, s string) (FeatureValue, error) {
	var fv FeatureValue

	s, fv.Negated = strings.CutPrefix(s, "!")

	if num, ok := strings.CutPrefix(s, "#"); ok {
		fv.Kind = FeatureNumeric

		var err error
		if fv.Min, fv.Max, err = parseFeatureNumeric(num); err != nil {
			return FeatureValue{}, fmt.Errorf("numeric value %q: %w", s, err)
		}

		return fv, nil
	}

	if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r > 0x7e || strings.ContainsRune(`"<>\`, r) }) {
		return FeatureValue{}, fmt.Errorf("invalid token value %q", s)
	}

	fv.Text = s

	if slices.Contains(urnFeatureTags, tag) {
		if u, err := url.PathUnescape(s); err == nil {
			fv.Text = u
		}
	}

	return fv, nil
}

// RFC 3840 section 9
func isFtagName(s string) bool {
	if s == "" || !isAlpha(s[0]) {
		return false
	}

	for i := 1; i < len(s); i++ {
		if c := s[i]; !isAlnum(c) && strings.IndexByte("!'.-%", c) < 0 {
			return false
		}
	}

	return true
}

// RFC 3840 section 9
func isTokenNoBang(c byte) bool {
	return isAlnum(c) || strings.IndexByte("-.%*_+`'~", c) >= 0
}

// RFC 3840 section 9
func parseFeatureNumeric(s string) (float64, float64, error) {
	switch {
	case strings.HasPrefix(s, ">="):
		f, err := parseFeatureNumber(s[2:])
		return f, math.Inf(1), err
	case strings.HasPrefix(s, "<="):
		f, err := parseFeatureNumber(s[2:])
		return math.Inf(-1), f, err
	case strings.HasPrefix(s, "="):
		f, err := parseFeatureNumber(s[1:])
		return f, f, err
	}

	lo, hi, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, errors.New("no relation")
	}

	a, err := parseFeatureNumber(lo)
	if err != nil {
		return 0, 0, err
	}

	b, err := parseFeatureNumber(hi)
	if err != nil || b < a {
		return 0, 0, errors.New("invalid range")
	}

	return a, b, nil
}

func parseFeatureNumber(s string) (float64, error) {
	digits := strings.TrimLeft(s, "+-")
	whole, frac, _ := strings.Cut(digits, ".")

	if len(s)-len(digits) > 1 || !isDigits(whole) || frac != "" && !isDigits(frac) {
		return 0, errors.New("invalid number")
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) {
		return 0, errors.New("invalid number")
	}

	return f, nil
}

func (fv FeatureValue) appendTo(b []byte, tag string) ([]byte, error) {
	if fv.Negated {
		b = append(b, '!')
	}

	switch fv.Kind {
	case FeatureString:
		b = append(b, '<')

		for i := range len(fv.Text) {
			c := fv.Text[i]

			switch {
			case c == '\r' || c == '\n':
				return nil, fmt.Errorf("string value %q: CR or LF", fv.Text)
			case c == '<' || c == '>' || c == '"' || c == '\\' || isTextCTL(c):
				b = append(b, '\\', c)
			default:
				b = append(b, c)
			}
		}

		return append(b, '>'), nil
	case FeatureNumeric:
		if math.IsNaN(fv.Min) || math.IsNaN(fv.Max) || fv.Min > fv.Max || math.IsInf(fv.Min, 1) || math.IsInf(fv.Max, -1) ||
			math.IsInf(fv.Min, -1) && math.IsInf(fv.Max, 1) {
			return nil, fmt.Errorf("invalid numeric range [%v, %v]", fv.Min, fv.Max)
		}

		f := func(v float64) []byte { return strconv.AppendFloat(nil, v, 'f', -1, 64) }

		b = append(b, '#')

		switch {
		case fv.Min == fv.Max:
			b = append(b, '=')
			b = append(b, f(fv.Min)...)
		case math.IsInf(fv.Max, 1):
			b = append(b, ">="...)
			b = append(b, f(fv.Min)...)
		case math.IsInf(fv.Min, -1):
			b = append(b, "<="...)
			b = append(b, f(fv.Max)...)
		default:
			b = append(b, f(fv.Min)...)
			b = append(b, ':')
			b = append(b, f(fv.Max)...)
		}

		return b, nil
	}

	if fv.Text == "" {
		return nil, errors.New("empty token value")
	}

	const hex = "0123456789ABCDEF"

	urn := slices.Contains(urnFeatureTags, tag)

	for i := range len(fv.Text) {
		c := fv.Text[i]

		switch {
		case isTokenNoBang(c) && (c != '%' || !urn):
			b = append(b, c)
		case urn:
			b = append(b, '%', hex[c>>4], hex[c&0xf])
		default:
			return nil, fmt.Errorf("token value %q: invalid character %q", fv.Text, c)
		}
	}

	return b, nil
}

// RFC 3840 section 9
// RFC 3840 section 5
func (t FeatureTerm) Param() (Param, error) {
	if !isFtagName(strings.TrimPrefix(featureParam(t.Tag), "+")) || len(t.Values) == 0 {
		return Param{}, fmt.Errorf("feature tag %q without a valid name or value", t.Tag)
	}

	p := Param{Name: featureParam(t.Tag)}

	if len(t.Values) == 1 && !t.Values[0].Negated && t.Values[0].Kind == FeatureToken && strings.EqualFold(t.Values[0].Text, "TRUE") {
		return p, nil
	}

	var b []byte

	for i, v := range t.Values {
		if v.Kind == FeatureString && (v.Negated || len(t.Values) > 1) {
			return Param{}, fmt.Errorf("feature tag %s: a string value is alone and not negated", t.Tag)
		}

		if i > 0 {
			b = append(b, ',')
		}

		var err error
		if b, err = v.appendTo(b, t.Tag); err != nil {
			return Param{}, fmt.Errorf("feature tag %s: %w", t.Tag, err)
		}
	}

	p.Value = `"` + string(b) + `"`

	return p, nil
}

func (fs FeatureSet) Params() (Params, error) {
	var ps Params

	for _, t := range fs {
		p, err := t.Param()
		if err != nil {
			return nil, err
		}

		ps = append(ps, p)
	}

	return ps, nil
}

func (fs FeatureSet) Term(tag string) (FeatureTerm, bool) {
	i := slices.IndexFunc(fs, func(t FeatureTerm) bool { return strings.EqualFold(t.Tag, tag) })
	if i < 0 {
		return FeatureTerm{}, false
	}

	return fs[i], true
}

func (fs FeatureSet) Has(tag string) bool {
	_, ok := fs.Term(tag)
	return ok
}

func (fs FeatureSet) Tokens(tag string) []string {
	t, _ := fs.Term(tag)

	var out []string

	for _, v := range t.Values {
		if !v.Negated && v.Kind != FeatureNumeric {
			out = append(out, v.Text)
		}
	}

	return out
}

// RFC 2533
func (fv FeatureValue) intersects(o FeatureValue) bool {
	switch {
	case fv.Negated && o.Negated:
		return true
	case fv.Negated:
		return !o.within(fv)
	case o.Negated:
		return !fv.within(o)
	case fv.Kind != o.Kind:
		return false
	case fv.Kind == FeatureNumeric:
		return math.Max(fv.Min, o.Min) <= math.Min(fv.Max, o.Max)
	case fv.Kind == FeatureString:
		return fv.Text == o.Text
	}

	return strings.EqualFold(fv.Text, o.Text)
}

func (fv FeatureValue) within(o FeatureValue) bool {
	if fv.Kind != o.Kind {
		return false
	}

	switch fv.Kind {
	case FeatureNumeric:
		return o.Min <= fv.Min && fv.Max <= o.Max
	case FeatureString:
		return fv.Text == o.Text
	}

	return strings.EqualFold(fv.Text, o.Text)
}

func (t FeatureTerm) matches(o FeatureTerm) bool {
	return slices.ContainsFunc(t.Values, func(a FeatureValue) bool {
		return slices.ContainsFunc(o.Values, a.intersects)
	})
}

// RFC 3841 section 10
type CallerPreference struct {
	Features FeatureSet

	Require, Explicit bool

	Params Params
}

func (p CallerPreference) Value() (string, error) {
	ps, err := p.Features.Params()
	if err != nil {
		return "", err
	}

	if p.Require {
		ps = append(ps, Param{Name: "require"})
	}

	if p.Explicit {
		ps = append(ps, Param{Name: "explicit"})
	}

	return "*" + append(ps, p.Params...).String(), nil
}

func (fs Header) AcceptContact() ([]CallerPreference, error) {
	return fs.callerPreferences("Accept-Contact")
}

func (fs Header) RejectContact() ([]CallerPreference, error) {
	return fs.callerPreferences("Reject-Contact")
}

func (fs Header) callerPreferences(name string) ([]CallerPreference, error) {
	var (
		out  []CallerPreference
		errs []error
	)

	for _, v := range fs.Elements(name) {
		ps, err := starParams(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}

		features, err := ParseFeatureSet(ps)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}

		p := CallerPreference{Features: features}

		for _, param := range ps {
			switch {
			case strings.EqualFold(param.Name, "require"):
				p.Require = true
			case strings.EqualFold(param.Name, "explicit"):
				p.Explicit = true
			case !IsFeatureParam(param.Name):
				p.Params = append(p.Params, param)
			}
		}

		out = append(out, p)
	}

	return out, errors.Join(errs...)
}

// RFC 6809 section 6
func (fs Header) FeatureCaps() ([]FeatureSet, error) {
	var (
		out  []FeatureSet
		errs []error
	)

	for _, v := range fs.Elements("Feature-Caps") {
		ps, err := starParams(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("Feature-Caps: %w", err))
			continue
		}

		set, err := ParseFeatureSet(slices.DeleteFunc(ps, func(p Param) bool { return !strings.HasPrefix(p.Name, "+") }))
		if err != nil {
			errs = append(errs, fmt.Errorf("Feature-Caps: %w", err))
		}

		out = append(out, set)
	}

	return out, errors.Join(errs...)
}

// RFC 6809 section 6.2
func FeatureCapsValue(fs FeatureSet) (string, error) {
	ps, err := fs.Params()
	if err != nil {
		return "", err
	}

	if slices.ContainsFunc(ps, func(p Param) bool { return !strings.HasPrefix(p.Name, "+") }) {
		return "", errors.New("Feature-Caps: a base tag is not a feature-capability indicator")
	}

	return "*" + ps.String(), nil
}

func starParams(v string) (Params, error) {
	star, ps, err := ParseTokenParams(v)
	if err != nil {
		return nil, err
	}

	if star != "*" {
		return nil, fmt.Errorf("value %q does not start with *", v)
	}

	return ps, nil
}

// RFC 3840 section 9
func (a Address) Features() (FeatureSet, error) {
	return ParseFeatureSet(a.Params)
}

// RFC 3841 section 7.2.4
func (fs FeatureSet) RejectedBy(rejects []CallerPreference) bool {
	if len(fs) == 0 {
		return false
	}

	return slices.ContainsFunc(rejects, func(r CallerPreference) bool {
		return len(r.Features) > 0 && !slices.ContainsFunc(r.Features, func(t FeatureTerm) bool {
			have, ok := fs.Term(t.Tag)
			return !ok || !t.matches(have)
		})
	})
}

// RFC 3841 section 7.2.4
func (fs FeatureSet) Preference(accepts []CallerPreference) (qa float64, ok bool) {
	if len(fs) == 0 || !slices.ContainsFunc(accepts, func(p CallerPreference) bool { return len(p.Features) > 0 }) {
		return 1, true
	}

	var (
		total float64
		m     int
	)

	for _, p := range accepts {
		if len(p.Features) == 0 {
			continue
		}

		present, matched := 0, true

		for _, t := range p.Features {
			if have, ok := fs.Term(t.Tag); ok {
				present++
				matched = matched && t.matches(have)
			}
		}

		if !matched {
			if p.Require {
				return 0, false
			}

			continue
		}

		score := float64(present) / float64(len(p.Features))

		if present < len(p.Features) && p.Explicit {
			if p.Require {
				return 0, false
			}

			score = 0
		}

		total += score
		m++
	}

	if m == 0 {
		return 0, true
	}

	return total / float64(m), true
}
