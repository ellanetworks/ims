package sip

import (
	"errors"
	"fmt"
	"strconv"
)

// MagicCookie starts every RFC 3261 branch parameter (§8.1.1.7).
const MagicCookie = "z9hG4bK"

// Via is one via-parm (RFC 3261 §20.42).
type Via struct {
	// Protocol is the protocol name and version, normally "SIP/2.0".
	Protocol string
	// Transport is kept as written, normally "UDP" or "TCP".
	Transport Transport
	// Host is a host name, an IPv4 address or an IPv6 reference in
	// brackets.
	Host string
	// Port is 0 when sent-by has none. A port of 0 as written is kept
	// only while the field is unchanged.
	Port   uint16
	Params Params
}

// Branch returns the branch parameter.
func (v Via) Branch() string {
	b, _ := v.Params.Get("branch")
	return b
}

// Received returns the received parameter.
func (v Via) Received() string {
	r, _ := v.Params.Get("received")
	return r
}

// RPort returns the rport parameter (RFC 3581). present is false when
// there is none; port is 0 when it carries no value. A malformed value
// also gives 0: Request.Validate rejects it in the top Via.
func (v Via) RPort() (port uint16, present bool) {
	r, ok := v.Params.Get("rport")
	if !ok {
		return 0, false
	}

	p, err := parseUint(r, 65535)
	if err != nil {
		return 0, true
	}

	return uint16(p), true
}

// SentBy returns host[:port].
func (v Via) SentBy() string {
	if v.Port == 0 {
		return v.Host
	}

	return v.Host + ":" + strconv.Itoa(int(v.Port))
}

// Clone returns a copy that shares no storage with v.
func (v Via) Clone() Via {
	v.Params = v.Params.Clone()
	return v
}

// String returns the via-parm in wire form.
func (v Via) String() string {
	b := make([]byte, 0, 64)
	b = append(b, v.Protocol...)
	b = append(b, '/')
	b = append(b, v.Transport...)
	b = append(b, ' ')
	b = append(b, v.SentBy()...)
	b = v.Params.appendTo(b)

	return string(b)
}

// ParseVia parses one via-parm. Use SplitList for a Via field holding
// several.
func ParseVia(s string) (Via, error) {
	v, err := parseVia(trimWSP(s))
	if err != nil {
		return Via{}, fmt.Errorf("Via %q: %w", s, err)
	}

	return v, nil
}

func parseVia(s string) (Via, error) {
	// sent-protocol = name SLASH version SLASH transport, where SLASH may
	// be surrounded by whitespace.
	var parts [3]string

	for i := range parts {
		if i > 0 {
			s = trimLeftWSP(s)
			if s == "" || s[0] != '/' {
				return Via{}, errors.New("invalid sent-protocol")
			}

			s = trimLeftWSP(s[1:])
		}

		n := tokenLen(s)
		if n == 0 {
			return Via{}, errors.New("invalid sent-protocol")
		}

		parts[i], s = s[:n], s[n:]
	}

	if s == "" || !isWSP(s[0]) {
		return Via{}, errors.New("missing sent-by")
	}

	v := Via{Protocol: parts[0] + "/" + parts[1], Transport: Transport(parts[2])}

	host, s, err := parseHost(trimLeftWSP(s))
	if err != nil {
		return Via{}, err
	}

	v.Host = host

	if s = trimLeftWSP(s); s != "" && s[0] == ':' {
		s = trimLeftWSP(s[1:])

		n := 0
		for n < len(s) && '0' <= s[n] && s[n] <= '9' {
			n++
		}

		if v.Port, err = parsePort(s[:n]); err != nil {
			return Via{}, err
		}

		s = s[n:]
	}

	if s != "" && !isWSP(s[0]) && s[0] != ';' {
		return Via{}, fmt.Errorf("unexpected %q after sent-by", s)
	}

	if v.Params, err = parseHeaderParams(s); err != nil {
		return Via{}, err
	}

	return v, nil
}
