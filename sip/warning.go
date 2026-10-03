package sip

import (
	"fmt"
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

func NewWarning(code int, agent string) Warning {
	return Warning{Code: code, Agent: agent, Text: WarningText(code)}
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
	if err != nil || n != len(text) || strings.ContainsAny(text, "\r\n") {
		return Warning{}, fmt.Errorf("warning-value %q: warn-text is not a quoted string", s)
	}

	return Warning{Code: code, Agent: agent, Text: Unquote(text)}, nil
}

func isWarnAgent(s string) bool {
	for i := range len(s) {
		if c := s[i]; c <= ' ' || c >= 0x7f || c == '"' || c == ',' {
			return false
		}
	}

	return true
}

func (w Warning) String() string {
	return fmt.Sprintf("%03d %s %s", w.Code, w.Agent, Quote(w.Text))
}

func (fs Header) Warnings() ([]Warning, error) {
	var out []Warning

	for _, e := range fs.Elements("Warning") {
		w, err := ParseWarning(e)
		if err != nil {
			return nil, fmt.Errorf("Warning: %w", err)
		}

		out = append(out, w)
	}

	return out, nil
}
