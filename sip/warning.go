package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

const (
	WarnIncompatibleNetworkProtocol = 300
	WarnIncompatibleAddressFormats  = 301
	WarnIncompatibleTransport       = 302
	WarnIncompatibleBandwidthUnits  = 303
	WarnMediaTypeNotAvailable       = 304
	WarnIncompatibleMediaFormat     = 305
	WarnAttributeNotUnderstood      = 306
	WarnSessionParamNotUnderstood   = 307
	WarnMulticastNotAvailable       = 330
	WarnUnicastNotAvailable         = 331
	WarnInsufficientBandwidth       = 370
	WarnMiscellaneous               = 399
)

var warningTexts = map[int]string{
	WarnIncompatibleNetworkProtocol: "Incompatible network protocol",
	WarnIncompatibleAddressFormats:  "Incompatible network address formats",
	WarnIncompatibleTransport:       "Incompatible transport protocol",
	WarnIncompatibleBandwidthUnits:  "Incompatible bandwidth units",
	WarnMediaTypeNotAvailable:       "Media type not available",
	WarnIncompatibleMediaFormat:     "Incompatible media format",
	WarnAttributeNotUnderstood:      "Attribute not understood",
	WarnSessionParamNotUnderstood:   "Session description parameter not understood",
	WarnMulticastNotAvailable:       "Multicast not available",
	WarnUnicastNotAvailable:         "Unicast not available",
	WarnInsufficientBandwidth:       "Insufficient bandwidth",
	WarnMiscellaneous:               "Miscellaneous warning",
}

func WarningText(code int) string {
	return warningTexts[code]
}

type Warning struct {
	Code  int
	Agent string
	Text  string
}

func NewWarning(code int, agent string) (Warning, error) {
	if code < 0 || code > 999 {
		return Warning{}, fmt.Errorf("warn-code %d is not three digits", code)
	}

	if a, err := netip.ParseAddr(agent); err == nil && a.Is6() && a.Zone() == "" {
		agent = "[" + agent + "]"
	}

	if !isWarnAgent(agent) {
		return Warning{}, fmt.Errorf("warn-agent %q is neither a hostport nor a token", agent)
	}

	return Warning{Code: code, Agent: agent, Text: WarningText(code)}, nil
}

func ParseWarning(s string) (Warning, error) {
	t := trimWSP(s)

	if len(t) < 4 || !isDigits(t[:3]) || !isWSP(t[3]) {
		return Warning{}, fmt.Errorf("warning-value %q: missing warn-code", s)
	}

	code, _ := strconv.Atoi(t[:3])
	t = trimLeftWSP(t[4:])

	i := strings.IndexAny(t, " \t")
	if i <= 0 {
		return Warning{}, fmt.Errorf("warning-value %q: missing warn-agent", s)
	}

	agent, text := t[:i], trimLeftWSP(t[i:])
	if !isWarnAgent(agent) {
		return Warning{}, fmt.Errorf("warning-value %q: invalid warn-agent", s)
	}

	n, err := quotedLen(text)
	if err != nil || n != len(text) || hasCTL(text) {
		return Warning{}, fmt.Errorf("warning-value %q: warn-text is not a quoted string", s)
	}

	return Warning{Code: code, Agent: agent, Text: Unquote(text)}, nil
}

func isWarnAgent(s string) bool {
	if isToken(s) {
		return true
	}

	_, rest, err := parseHost(s)
	if err != nil {
		return false
	}

	if rest == "" {
		return true
	}

	if rest[0] != ':' {
		return false
	}

	_, err = parsePort(rest[1:])

	return err == nil
}

func (w Warning) String() string {
	return fmt.Sprintf("%03d %s %s", w.Code, w.Agent, Quote(w.Text))
}

func (fs Header) Warnings() ([]Warning, error) {
	var (
		out  []Warning
		errs []error
	)

	for _, e := range fs.Elements("Warning") {
		w, err := ParseWarning(e)
		if err != nil {
			errs = append(errs, fmt.Errorf("Warning: %w", err))
			continue
		}

		out = append(out, w)
	}

	return out, errors.Join(errs...)
}
