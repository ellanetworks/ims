package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
)

const MagicCookie = "z9hG4bK"

type Via struct {
	Protocol string

	Transport Transport

	Host string

	Port   uint16
	Params Params
}

func (v Via) Branch() string {
	b, _ := v.Params.Get("branch")
	return b
}

func (v Via) Received() string {
	r, _ := v.Params.Get("received")
	return r
}

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

func (v Via) SentBy() string {
	if v.Port == 0 {
		return v.Host
	}

	return v.Host + ":" + strconv.Itoa(int(v.Port))
}

func (v Via) Clone() Via {
	v.Params = v.Params.Clone()
	return v
}

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

func ParseVia(s string) (Via, error) {
	v, err := parseVia(trimWSP(s))
	if err != nil {
		return Via{}, fmt.Errorf("Via %q: %w", s, err)
	}

	return v, nil
}

func parseVia(s string) (Via, error) {
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

func (v Via) Addr() (netip.Addr, bool) {
	return HostAddr(v.Host)
}
