package sip

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	RefresherUAC = "uac"
	RefresherUAS = "uas"
)

// SessionExpires is a Session-Expires value (RFC 4028 §4): the session interval, in seconds, and the refresher, when
// chosen.
type SessionExpires struct {
	Seconds   uint32
	Refresher string
	Params    Params
}

func ParseSessionExpires(s string) (SessionExpires, error) {
	n, ps, err := parseDeltaParams(s)
	if err != nil {
		return SessionExpires{}, fmt.Errorf("Session-Expires %w", err)
	}

	se := SessionExpires{Seconds: n}

	for _, p := range ps {
		if !strings.EqualFold(p.Name, "refresher") {
			se.Params = append(se.Params, p)
			continue
		}

		switch r := strings.ToLower(p.Value); r {
		case RefresherUAC, RefresherUAS:
			se.Refresher = r
		default:
			return SessionExpires{}, fmt.Errorf("Session-Expires %q: refresher %q", s, p.Value)
		}
	}

	return se, nil
}

func (se SessionExpires) String() string {
	s := strconv.FormatUint(uint64(se.Seconds), 10)
	if se.Refresher != "" {
		s += ";refresher=" + se.Refresher
	}

	return s + se.Params.String()
}

func (fs Header) SessionExpires() (SessionExpires, bool, error) {
	v, ok, err := fs.single("Session-Expires")
	if !ok || err != nil {
		return SessionExpires{}, ok, err
	}

	se, err := ParseSessionExpires(v)

	return se, true, err
}

// MinSE is the Min-SE value, in seconds (RFC 4028 §5).
func (fs Header) MinSE() (uint32, bool, error) {
	v, ok, err := fs.single("Min-SE")
	if !ok || err != nil {
		return 0, ok, err
	}

	n, _, err := parseDeltaParams(v)
	if err != nil {
		return 0, true, fmt.Errorf("Min-SE %w", err)
	}

	return n, true, nil
}

// delta-seconds *(SEMI param), with delta-seconds of RFC 3261 §25.1. Values above 2**32-1 count as 2**32-1, as RFC
// 3261 §10.2.1.1 allows for expires.
func parseDeltaParams(s string) (uint32, Params, error) {
	v, rest, _ := strings.Cut(trimWSP(s), ";")

	n, err := parseDeltaSeconds(trimWSP(v))
	if err != nil {
		return 0, nil, err
	}

	ps, err := ParseParams(rest)
	if err != nil {
		return 0, nil, err
	}

	return n, ps, nil
}

func parseDeltaSeconds(s string) (uint32, error) {
	n, err := parseUint(s, 1<<32-1)

	switch {
	case errors.Is(err, errRange):
		return 1<<32 - 1, nil
	case err != nil:
		return 0, errors.New("invalid delta-seconds")
	}

	return uint32(n), nil
}
