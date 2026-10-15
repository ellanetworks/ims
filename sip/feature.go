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

// urnFeatureTags carry URNs, percent-encoded where RFC 3840 does not allow their characters (TS 24.229 §7.2A.8.2,
// §7.2A.9.2).
var urnFeatureTags = []string{"g.3gpp.icsi-ref", "g.3gpp.iari-ref"}

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

	// Text is the token, or the string without its angle brackets and quoting. The URN of a URN tag is decoded.
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

// NewFeature is a term with token values, TRUE when there is none. tag is a feature tag, or a feature parameter name.
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

// NewStringFeature is a term with a string value, compared case-sensitively.
func NewStringFeature(tag, s string) FeatureTerm {
	return FeatureTerm{Tag: normalizeFeatureTag(tag), Values: []FeatureValue{{Kind: FeatureString, Text: s}}}
}

func normalizeFeatureTag(tag string) string {
	if strings.HasPrefix(tag, "+") {
		return featureTag(tag)
	}

	return strings.ToLower(tag)
}

func isBaseFeatureTag(name string) bool {
	return slices.ContainsFunc(baseFeatureTags, func(b string) bool { return strings.EqualFold(b, name) })
}

// IsFeatureParam reports whether a header field parameter is a feature parameter: a base tag, or a name with a
// leading "+" (RFC 3841 §7.2.1).
func IsFeatureParam(name string) bool {
	return strings.HasPrefix(name, "+") || isBaseFeatureTag(name)
}

// featureTag is the feature tag of a feature parameter (RFC 3840 §5). Feature-capability indicators (RFC 6809 §6.3)
// are mapped the same way: their names are media feature tag names with a mandatory "+".
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
// the "+" parameters whose tag is not already there. A malformed feature parameter is left out and reported in the
// error, next to the others.
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

				// RFC 3841 §7.2.3 step 3: "+video" next to "video" is dropped.
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

// RFC 3840 §9: no value is TRUE; a string-value is one string; otherwise a list of tag-values. Unquoted values are
// accepted too.
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

// RFC 3840 §9: string-value = "<" *(qdtext-no-abkt / quoted-pair ) ">".
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

	// Lenient: any visible character but the quoting and list ones, so that URNs sent unescaped still match.
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

// RFC 3840 §9: numeric = "#" (">=" / "<=" / "=" / number ":") number, number = [ "+" / "-" ] 1*DIGIT ["." 0*DIGIT].
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

// Param is the feature parameter of the term (RFC 3840 §9): bare for TRUE, a quoted list otherwise. A string value
// is alone and not negated (RFC 3840 §5).
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

// Params are the feature parameters of the set.
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

	// Params are the other parameters.
	Params Params
}

// Value is the header field value: "*" and the parameters.
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

// callerPreferences parses the predicates of a header field. A predicate with a malformed term is left out whole,
// and reported in the error: without the term, it would match contacts the caller did not mean.
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

// FeatureCaps are the feature-capability indicators of the Feature-Caps values (RFC 6809 §6): one set for each
// entity that inserted one. Malformed indicators are left out and reported in the error.
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

// FeatureCapsValue is a Feature-Caps value of feature-capability indicators (RFC 6809 §6.2).
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

// Features is the feature set of a Contact (RFC 3840 §9), without its malformed feature parameters, which the error
// reports.
func (a Address) Features() (FeatureSet, error) {
	return ParseFeatureSet(a.Params)
}

// RejectedBy reports whether a Reject-Contact predicate discards a contact with these features: one whose tags all
// appear in the contact's features, and match them (RFC 3841 §7.2.4). A contact without features is immune. A
// predicate without feature parameters constrains nothing and is ignored.
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
// is immune, with a Qa of 1. A predicate without feature parameters is ignored.
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
