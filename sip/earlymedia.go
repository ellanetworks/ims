package sip

import (
	"fmt"
	"slices"
	"strings"
)

const (
	EarlyMediaSendRecv  = "sendrecv"
	EarlyMediaSendOnly  = "sendonly"
	EarlyMediaRecvOnly  = "recvonly"
	EarlyMediaInactive  = "inactive"
	EarlyMediaGated     = "gated"
	EarlyMediaSupported = "supported"
)

type EarlyMedia []string

func ParseEarlyMedia(s string) (EarlyMedia, error) {
	var out EarlyMedia

	for _, e := range SplitList(s) {
		if e == "" {
			continue
		}

		if !isToken(e) {
			return nil, fmt.Errorf("P-Early-Media %q: invalid em-param %q", s, e)
		}

		out = append(out, e)
	}

	return out, nil
}

func (em EarlyMedia) Has(param string) bool {
	return slices.ContainsFunc(em, func(v string) bool { return strings.EqualFold(v, param) })
}

func (em EarlyMedia) String() string {
	return strings.Join(em, ", ")
}

func (fs Header) EarlyMedia() (EarlyMedia, bool, error) {
	if !fs.Has("P-Early-Media") {
		return nil, false, nil
	}

	var out EarlyMedia

	for _, v := range fs.Values("P-Early-Media") {
		em, err := ParseEarlyMedia(v)
		if err != nil {
			return nil, true, err
		}

		out = append(out, em...)
	}

	return out, true, nil
}
