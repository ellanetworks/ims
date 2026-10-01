package sip

import (
	"fmt"
	"net/netip"
	"strings"
)

func NewVia(tr Transport, sentBy netip.AddrPort) Via {
	v := Via{Protocol: "SIP/2.0", Transport: tr, Host: FormatHost(sentBy.Addr()), Port: sentBy.Port()}
	v.Params.Set("branch", NewBranch())

	return v
}

func (u URI) IsLooseRouter() bool {
	return u.Params.Has("lr")
}

func (fs Header) TopRoute() (Address, bool, error) {
	v, err := fs.first("Route")
	if err != nil {
		return Address{}, false, nil
	}

	top, _ := firstListElement(v)

	a, err := ParseAddress(top)
	if err != nil {
		return Address{}, true, err
	}

	return a, true, nil
}

func NextHop(r *Request) (Transport, netip.AddrPort, error) {
	u := r.URI

	route, ok, err := r.Header.TopRoute()
	if err != nil {
		return "", netip.AddrPort{}, err
	}

	if ok {
		u = route.URI
	}

	if !u.IsSIP() || u.IsSIPS() {
		return "", netip.AddrPort{}, fmt.Errorf("sip: no next hop in %s", u)
	}

	a, ok := u.Addr()
	if !ok {
		return "", netip.AddrPort{}, fmt.Errorf("sip: next hop %s is not an IP address", u.Host)
	}

	port := u.Port
	if port == 0 {
		port = 5060
	}

	tr := UDP

	if t, ok := u.Params.Get("transport"); ok {
		switch tr = Transport(strings.ToUpper(t)); tr {
		case UDP, TCP:
		default:
			return "", netip.AddrPort{}, fmt.Errorf("sip: next hop transport %s", t)
		}
	}

	return tr, netip.AddrPortFrom(a.Unmap(), port), nil
}

type RAck struct {
	RSeq   uint32
	CSeq   uint32
	Method string
}

func (r RAck) String() string {
	return fmt.Sprintf("%d %d %s", r.RSeq, r.CSeq, r.Method)
}

func ParseRAck(s string) (RAck, error) {
	f := strings.Fields(s)
	if len(f) != 3 || !isToken(f[2]) {
		return RAck{}, fmt.Errorf("RAck %q: want response-num CSeq-num method", s)
	}

	rseq, err := parseUint(f[0], 1<<32-1)
	if err != nil || rseq == 0 {
		return RAck{}, fmt.Errorf("RAck %q: invalid response number", s)
	}

	cseq, err := parseUint(f[1], 1<<32-1)
	if err != nil {
		return RAck{}, fmt.Errorf("RAck %q: invalid CSeq number", s)
	}

	return RAck{RSeq: uint32(rseq), CSeq: uint32(cseq), Method: f[2]}, nil
}

func (fs Header) RAck() (RAck, error) {
	v, err := fs.first("RAck")
	if err != nil {
		return RAck{}, err
	}

	return ParseRAck(v)
}

func (fs Header) RSeq() (uint32, error) {
	v, err := fs.first("RSeq")
	if err != nil {
		return 0, err
	}

	n, err := parseUint(trimWSP(v), 1<<32-1)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("RSeq %q: invalid response number", v)
	}

	return uint32(n), nil
}
