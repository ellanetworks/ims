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

// baseFeatureTags are the feature tags whose feature parameter has neither a "+" nor a "sip." prefix (RFC 3840 §5,
// §10). All but language and type are in the SIP tree.
var baseFeatureTags = []string{
	"audio", "automata", "class", "duplex", "data", "control", "mobility", "description", "events", "priority",
	"methods", "schemes", "application", "video", "language", "type", "isfocus", "actor", "text", "extensions",
}

type FeatureKind int

const (
	FeatureToken FeatureKind = iota
	FeatureString
	FeatureNumeric
)

// FeatureValue is a simple filter of a feature predicate term (RFC 3840 §5, §9): an equality over a token or boolean
// (case-insensitive), an equality over a string (case-sensitive), or a numeric comparison, which may be negated.
type FeatureValue struct {
	Negated bool
	Kind    FeatureKind

	// Text is the token, unescaped, or the string without its angle brackets.
	Text string

	// Min and Max bound a numeric value, inclusive: equal for =, infinite for >= and <=.
	Min, Max float64
}

// FeatureTerm constrains one feature tag: the disjunction of its values (RFC 3840 §5).
type FeatureTerm struct {
	// Tag is the lower-cased feature tag: sip.audio, language, g.3gpp.icsi-ref.
	Tag    string
	Values []FeatureValue
}

// FeatureSet is a feature predicate: the conjunction of terms on distinct feature tags, as the feature parameters of
// a Contact, an Accept-Contact or Reject-Contact value, or a Feature-Caps value express it (RFC 3840 §9, RFC 3841
// §10, RFC 6809 §6).
type FeatureSet []FeatureTerm

// NewFeature is a term with token values: TRUE when there is none.
func NewFeature(tag string, tokens ...string) FeatureTerm {
	if len(tokens) == 0 {
		tokens = []string{"TRUE"}
	}

	t := FeatureTerm{Tag: strings.ToLower(tag)}
	for _, v := range tokens {
		t.Values = append(t.Values, FeatureValue{Kind: FeatureToken, Text: v})
	}

	return t
}

func isBaseFeatureTag(name string) bool {
	return slices.ContainsFunc(baseFeatureTags, func(b string) bool { return strings.EqualFold(b, name) })
}

// IsFeatureParam reports whether a header field parameter is a feature parameter: a base tag, or a name with a
// leading "+" (RFC 3841 §7.2.1).
func IsFeatureParam(name string) bool {
	return strings.HasPrefix(name, "+") || isBaseFeatureTag(name)
}

// featureTag is the feature tag of a feature parameter (RFC 3840 §5).
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

// featureParam is the feature parameter of a feature tag (RFC 3840 §5).
func featureParam(tag string) string {
	if rest, ok := strings.CutPrefix(tag, "sip."); ok && isBaseFeatureTag(rest) {
		return rest
	}

	if tag == "language" || tag == "type" {
		return tag
	}

	return "+" + strings.NewReplacer("/", "'", ":", "!").Replace(tag)
}

// ParseFeatureSet extracts the feature parameters of header field parameters (RFC 3841 §7.2.3): the base tags, and
// the "+" parameters whose tag is not already there.
func ParseFeatureSet(ps Params) (FeatureSet, error) {
	var fs FeatureSet

	for _, base := range []bool{true, false} {
		for _, p := range ps {
			if strings.HasPrefix(p.Name, "+") == base || !IsFeatureParam(p.Name) {
				continue
			}

			if !base && !isFtagName(p.Name[1:]) {
				return nil, fmt.Errorf("invalid feature parameter %q", p.Name)
			}

			tag := featureTag(p.Name)
			if fs.Has(tag) {
				continue
			}

			values, err := parseFeatureValues(p.Value)
			if err != nil {
				return nil, fmt.Errorf("feature parameter %s: %w", p.Name, err)
			}

			fs = append(fs, FeatureTerm{Tag: tag, Values: values})
		}
	}

	return fs, nil
}

// RFC 3840 §9: no value is TRUE; a quoted string-value is one string, any other value a list of tag-values.
// Tokens are percent-decoded, as TS 24.229 §7.9.2 encodes URNs in them.
func parseFeatureValues(v string) ([]FeatureValue, error) {
	if v == "" {
		return []FeatureValue{{Kind: FeatureToken, Text: "TRUE"}}, nil
	}

	inner := Unquote(v)

	if strings.HasPrefix(inner, "<") {
		text := strings.TrimSuffix(inner[1:], ">")

		// RFC 3840 §9: qdtext-no-abkt.
		if len(text) == len(inner)-1 || strings.ContainsFunc(text, func(r rune) bool { return r == '<' || r == '>' || r == '"' || r < 0x20 && r != '\t' || r == 0x7f }) {
			return nil, fmt.Errorf("invalid string-value %q", v)
		}

		return []FeatureValue{{Kind: FeatureString, Text: text}}, nil
	}

	var out []FeatureValue

	for s := range strings.SplitSeq(inner, ",") {
		fv, err := parseFeatureValue(trimWSP(s))
		if err != nil {
			return nil, err
		}

		out = append(out, fv)
	}

	return out, nil
}

func parseFeatureValue(s string) (FeatureValue, error) {
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

	if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r > 0x7e || !isTokenNoBang(byte(r)) }) {
		return FeatureValue{}, fmt.Errorf("invalid token value %q", s)
	}

	text, err := url.PathUnescape(s)
	if err != nil {
		return FeatureValue{}, fmt.Errorf("token value %q: %w", s, err)
	}

	fv.Text = text

	return fv, nil
}

// RFC 3840 §9: ftag-name = ALPHA *( ALPHA / DIGIT / "!" / "'" / "." / "-" / "%" ).
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

// RFC 3840 §9: token-nobang.
func isTokenNoBang(c byte) bool {
	return isAlnum(c) || strings.IndexByte("-.%*_+`'~", c) >= 0
}

// RFC 3840 §9: numeric = "#" (">=" / "<=" / "=" / number ":") number.
func parseFeatureNumeric(s string) (float64, float64, error) {
	num := func(s string) (float64, error) {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || strings.ContainsAny(s, "eExXpPnN_") {
			return 0, errors.New("invalid number")
		}

		return f, nil
	}

	switch {
	case strings.HasPrefix(s, ">="):
		f, err := num(s[2:])
		return f, math.Inf(1), err
	case strings.HasPrefix(s, "<="):
		f, err := num(s[2:])
		return math.Inf(-1), f, err
	case strings.HasPrefix(s, "="):
		f, err := num(s[1:])
		return f, f, err
	}

	lo, hi, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, errors.New("no relation")
	}

	a, err := num(lo)
	if err != nil {
		return 0, 0, err
	}

	b, err := num(hi)
	if err != nil || b < a {
		return 0, 0, errors.New("invalid range")
	}

	return a, b, nil
}

func (fv FeatureValue) appendTo(b []byte) []byte {
	if fv.Negated {
		b = append(b, '!')
	}

	switch fv.Kind {
	case FeatureString:
		b = append(b, '<')
		b = append(b, fv.Text...)

		return append(b, '>')
	case FeatureNumeric:
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

		return b
	}

	const hex = "0123456789ABCDEF"

	for i := range len(fv.Text) {
		c := fv.Text[i]
		if isTokenNoBang(c) && c != '%' {
			b = append(b, c)
			continue
		}

		b = append(b, '%', hex[c>>4], hex[c&0xf])
	}

	return b
}

// Param is the feature parameter of the term (RFC 3840 §9): bare for TRUE, a quoted list otherwise.
func (t FeatureTerm) Param() Param {
	p := Param{Name: featureParam(t.Tag)}

	if len(t.Values) == 1 && !t.Values[0].Negated && t.Values[0].Kind == FeatureToken && strings.EqualFold(t.Values[0].Text, "TRUE") {
		return p
	}

	var b []byte

	for i, v := range t.Values {
		if i > 0 {
			b = append(b, ',')
		}

		b = v.appendTo(b)
	}

	p.Value = Quote(string(b))

	return p
}

// Params are the feature parameters of the set.
func (fs FeatureSet) Params() Params {
	var ps Params
	for _, t := range fs {
		ps = append(ps, t.Param())
	}

	return ps
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

// Tokens are the token and string values of a tag that are not negated, such as the ICSIs of g.3gpp.icsi-ref.
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

// intersects reports whether a value satisfies both simple filters, as RFC 2533 matching asks.
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

// within reports whether every value fv admits is one o, taken as not negated, admits.
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

// matches reports whether a value satisfies both terms, on the same tag.
func (t FeatureTerm) matches(o FeatureTerm) bool {
	return slices.ContainsFunc(t.Values, func(a FeatureValue) bool {
		return slices.ContainsFunc(o.Values, a.intersects)
	})
}

// CallerPreference is an Accept-Contact or Reject-Contact value (RFC 3841 §10).
type CallerPreference struct {
	Features FeatureSet

	// Require and Explicit only apply to Accept-Contact.
	Require, Explicit bool
}

func (fs Header) AcceptContact() ([]CallerPreference, error) {
	return fs.callerPreferences("Accept-Contact")
}

func (fs Header) RejectContact() ([]CallerPreference, error) {
	return fs.callerPreferences("Reject-Contact")
}

func (fs Header) callerPreferences(name string) ([]CallerPreference, error) {
	var out []CallerPreference

	for _, v := range fs.Elements(name) {
		ps, err := starParams(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		p := CallerPreference{Require: ps.Has("require"), Explicit: ps.Has("explicit")}
		if p.Features, err = ParseFeatureSet(ps); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		out = append(out, p)
	}

	return out, nil
}

// FeatureCaps are the feature-capability indicators of the Feature-Caps values (RFC 6809 §6): one set for each
// entity that inserted one.
func (fs Header) FeatureCaps() ([]FeatureSet, error) {
	var out []FeatureSet

	for _, v := range fs.Elements("Feature-Caps") {
		ps, err := starParams(v)
		if err != nil {
			return nil, fmt.Errorf("Feature-Caps: %w", err)
		}

		set, err := ParseFeatureSet(slices.DeleteFunc(ps, func(p Param) bool { return !strings.HasPrefix(p.Name, "+") }))
		if err != nil {
			return nil, fmt.Errorf("Feature-Caps: %w", err)
		}

		out = append(out, set)
	}

	return out, nil
}

// "*" *(SEMI param), as in Accept-Contact, Reject-Contact and Feature-Caps.
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

// Features is the feature set of a Contact (RFC 3840 §9).
func (a Address) Features() (FeatureSet, error) {
	return ParseFeatureSet(a.Params)
}

// RejectedBy reports whether a Reject-Contact predicate discards a contact with these features: one whose tags all
// appear in the contact's features, and match them (RFC 3841 §7.2.4). A contact without features is immune.
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

// Preference is the caller preference Qa of a contact with these features against Accept-Contact predicates
// (RFC 3841 §7.2.4). ok is false when a predicate with the require flag drops the contact. A contact without features
// is immune, with a Qa of 1.
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

		// 1/N for each term the contact has, counted rather than summed so that a full match is exactly 1.
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

	// No predicate is left to match: the contact comes after those that match one.
	if m == 0 {
		return 0, true
	}

	return total / float64(m), true
}
