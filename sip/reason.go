package sip

import (
	"errors"
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
	ReasonEMM          = "EMM"
	ReasonESM          = "ESM"
	ReasonS1APRNL      = "S1AP-RNL"
	ReasonS1APTL       = "S1AP-TL"
	ReasonS1APNAS      = "S1AP-NAS"
	ReasonS1APMisc     = "S1AP-MISC"
	ReasonS1APProt     = "S1AP-PROT"
	ReasonDiameter     = "DIAMETER"
	ReasonIKEv2        = "IKEV2"
	Reason5GMM         = "5GMM"
	Reason5GSM         = "5GSM"
	ReasonNGAPRNL      = "NGAP-RNL"
	ReasonNGAPTL       = "NGAP-TL"
	ReasonNGAPNAS      = "NGAP-NAS"
	ReasonNGAPMisc     = "NGAP-MISC"
	ReasonNGAPProt     = "NGAP-PROT"
)

const callCompletedElsewhere = "Call completed elsewhere"

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
	FailureResourcesAllocation      = 3
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
	FailureResourcesAllocation:      "Indication of failed resources allocation",
}

func ReasonText(protocol string, cause int) string {
	switch strings.ToUpper(protocol) {
	case ReasonSIP:
		if cause == 200 {
			return callCompletedElsewhere
		}

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

func NewReason(protocol string, cause int, text string) (Reason, error) {
	if !isToken(protocol) {
		return Reason{}, fmt.Errorf("reason protocol %q is not a token", protocol)
	}

	if cause < 0 || cause > math.MaxInt32 {
		return Reason{}, fmt.Errorf("reason cause %d out of range", cause)
	}

	r := Reason{Protocol: protocol, Params: Params{{Name: "cause", Value: strconv.Itoa(cause)}}}
	if text != "" {
		r.Params = append(r.Params, Param{Name: "text", Value: Quote(text)})
	}

	return r, nil
}

func ParseReason(s string) (Reason, error) {
	protocol, ps, err := ParseTokenParams(s)
	if err != nil {
		return Reason{}, fmt.Errorf("reason-value %w", err)
	}

	r := Reason{Protocol: protocol, Params: ps}

	if err := checkReasonParams(ps); err != nil {
		return Reason{}, fmt.Errorf("reason-value %q: %w", s, err)
	}

	return r, nil
}

func checkReasonParams(ps Params) error {
	var cause, text bool

	for _, p := range ps {
		if hasCTL(p.Value) {
			return fmt.Errorf("control character in %s", p.Name)
		}

		switch {
		case strings.EqualFold(p.Name, "cause"):
			if cause {
				return errors.New("duplicate cause")
			}

			if _, err := parseUint(p.Value, math.MaxInt32); err != nil {
				return errors.New("invalid cause")
			}

			cause = true
		case strings.EqualFold(p.Name, "text"):
			if text {
				return errors.New("duplicate text")
			}

			if len(p.Value) < 2 || p.Value[0] != '"' {
				return errors.New("text is not a quoted string")
			}

			text = true
		}
	}

	return nil
}

func (r Reason) Is(protocol string) bool {
	return strings.EqualFold(r.Protocol, protocol)
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
	var (
		out  []Reason
		errs []error
	)

	for _, e := range fs.Elements("Reason") {
		r, err := ParseReason(e)
		if err != nil {
			errs = append(errs, fmt.Errorf("Reason: %w", err))
			continue
		}

		out = append(out, r)
	}

	return out, errors.Join(errs...)
}
