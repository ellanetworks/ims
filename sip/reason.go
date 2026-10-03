package sip

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	ReasonSIP          = "SIP"
	ReasonQ850         = "Q.850"
	ReasonReleaseCause = "RELEASE_CAUSE"
	ReasonFailureCause = "FAILURE_CAUSE"
)

const (
	ReleaseUserEndsCall       = 1
	ReleaseRTPTimeout         = 2
	ReleaseMediaBearerLoss    = 3
	ReleaseNoACK              = 4
	ReleaseResponseTimeout    = 5
	ReleaseCallSetupTimeout   = 6
	ReleaseRedirectionFailure = 7
)

const (
	FailureMediaBearerLost          = 1
	FailureSignallingBearerReleased = 2
)

var releaseCauseTexts = map[int]string{
	ReleaseUserEndsCall:       "User ends call",
	ReleaseRTPTimeout:         "RTP/RTCP time-out",
	ReleaseMediaBearerLoss:    "Media bearer loss",
	ReleaseNoACK:              "SIP timeout - no ACK",
	ReleaseResponseTimeout:    "SIP response time-out",
	ReleaseCallSetupTimeout:   "Call-setup time-out",
	ReleaseRedirectionFailure: "Redirection failure",
}

var failureCauseTexts = map[int]string{
	FailureMediaBearerLost:          "Media bearer or QoS lost",
	FailureSignallingBearerReleased: "Release of signalling bearer",
}

func ReasonText(protocol string, cause int) string {
	switch protocol {
	case ReasonSIP:
		if t, ok := reasonPhrases[cause]; ok {
			return t
		}
	case ReasonReleaseCause:
		return releaseCauseTexts[cause]
	case ReasonFailureCause:
		return failureCauseTexts[cause]
	}

	return ""
}

type Reason struct {
	Protocol string
	Params   Params
}

func NewReason(protocol string, cause int, text string) Reason {
	r := Reason{Protocol: protocol, Params: Params{{Name: "cause", Value: strconv.Itoa(cause)}}}
	if text != "" {
		r.Params = append(r.Params, Param{Name: "text", Value: Quote(text)})
	}

	return r
}

func ParseReason(s string) (Reason, error) {
	protocol, ps, err := ParseTokenParams(s)
	if err != nil {
		return Reason{}, fmt.Errorf("reason-value %w", err)
	}

	r := Reason{Protocol: protocol, Params: ps}

	if v, ok := ps.Get("cause"); ok {
		if _, err := parseUint(v, math.MaxInt32); err != nil {
			return Reason{}, fmt.Errorf("reason-value %q: invalid cause", s)
		}
	}

	if v, ok := ps.Get("text"); ok && (len(v) < 2 || v[0] != '"' || strings.ContainsAny(v, "\r\n")) {
		return Reason{}, fmt.Errorf("reason-value %q: text is not a quoted string", s)
	}

	return r, nil
}

func (r Reason) String() string {
	return r.Protocol + r.Params.String()
}

func (r Reason) Cause() (int, bool) {
	v, ok := r.Params.Get("cause")
	if !ok {
		return 0, false
	}

	n, err := parseUint(v, math.MaxInt32)

	return int(n), err == nil
}

func (r Reason) Text() string {
	v, _ := r.Params.Get("text")
	return Unquote(v)
}

func (fs Header) Reasons() ([]Reason, error) {
	var out []Reason

	for _, e := range fs.Elements("Reason") {
		r, err := ParseReason(e)
		if err != nil {
			return nil, fmt.Errorf("Reason: %w", err)
		}

		out = append(out, r)
	}

	return out, nil
}
