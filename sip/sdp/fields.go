package sdp

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

const (
	NetTypeIN = "IN"
	IP4       = "IP4"
	IP6       = "IP6"
)

type Origin struct {
	Username       string
	SessionID      string
	SessionVersion string
	NetType        string
	AddrType       string
	Address        string
}

func ParseOrigin(s string) (Origin, error) {
	f := strings.Fields(s)
	if len(f) != 6 {
		return Origin{}, fmt.Errorf("o=%q: want 6 fields, got %d", s, len(f))
	}

	if !isDigits(f[1]) || !isDigits(f[2]) {
		return Origin{}, fmt.Errorf("o=%q: session id and version must be numeric", s)
	}

	return Origin{
		Username:       f[0],
		SessionID:      f[1],
		SessionVersion: f[2],
		NetType:        f[3],
		AddrType:       f[4],
		Address:        f[5],
	}, nil
}

func (o Origin) String() string {
	return strings.Join([]string{o.Username, o.SessionID, o.SessionVersion, o.NetType, o.AddrType, o.Address}, " ")
}

type Connection struct {
	NetType  string
	AddrType string
	Address  string
}

func ParseConnection(s string) (Connection, error) {
	f := strings.Fields(s)
	if len(f) != 3 {
		return Connection{}, fmt.Errorf("c=%q: want 3 fields, got %d", s, len(f))
	}

	return Connection{NetType: f[0], AddrType: f[1], Address: f[2]}, nil
}

func (c Connection) String() string {
	return c.NetType + " " + c.AddrType + " " + c.Address
}

func (c Connection) Addr() (netip.Addr, bool) {
	host, _, _ := strings.Cut(c.Address, "/")

	a, err := netip.ParseAddr(host)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}

	switch c.AddrType {
	case IP4:
		return a, a.Is4()
	case IP6:
		return a, a.Is6() && !a.Is4In6()
	}

	return netip.Addr{}, false
}

func NewConnection(a netip.Addr) Connection {
	if a.Is4() || a.Is4In6() {
		return Connection{NetType: NetTypeIN, AddrType: IP4, Address: a.Unmap().String()}
	}

	return Connection{NetType: NetTypeIN, AddrType: IP6, Address: a.String()}
}

func (ls Lines) Connection() (Connection, error) {
	v, ok := ls.Value('c')
	if !ok {
		return Connection{}, fmt.Errorf("%w: c=", ErrMissing)
	}

	return ParseConnection(v)
}

func (ls *Lines) SetConnection(c Connection) {
	ls.Set('c', c.String())
}

const (
	BandwidthAS   = "AS"
	BandwidthCT   = "CT"
	BandwidthTIAS = "TIAS"
	BandwidthRS   = "RS"
	BandwidthRR   = "RR"
)

type Bandwidth struct {
	Type  string
	Value uint64
}

func ParseBandwidth(s string) (Bandwidth, error) {
	t, v, ok := strings.Cut(s, ":")
	if !ok || !isToken(t) {
		return Bandwidth{}, fmt.Errorf("b=%q: want <bwtype>:<bandwidth>", s)
	}

	if !isDigits(v) {
		return Bandwidth{}, fmt.Errorf("b=%q: bandwidth is not a number", s)
	}

	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return Bandwidth{}, fmt.Errorf("b=%q: %w", s, errors.Unwrap(err))
	}

	return Bandwidth{Type: t, Value: n}, nil
}

func (b Bandwidth) String() string {
	return b.Type + ":" + strconv.FormatUint(b.Value, 10)
}

func (ls Lines) Bandwidths() ([]Bandwidth, error) {
	var (
		out  []Bandwidth
		errs []error
	)

	for _, v := range ls.Values('b') {
		b, err := ParseBandwidth(v)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		out = append(out, b)
	}

	return out, errors.Join(errs...)
}

func (ls Lines) Bandwidth(typ string) (uint64, bool) {
	for _, v := range ls.Values('b') {
		if b, err := ParseBandwidth(v); err == nil && b.Type == typ {
			return b.Value, true
		}
	}

	return 0, false
}

func (ls *Lines) SetBandwidth(typ string, value uint64) {
	b := Bandwidth{Type: typ, Value: value}.String()

	for i, l := range *ls {
		if l.Type == 'b' && strings.HasPrefix(l.Value, typ+":") {
			(*ls)[i].Value = b
			return
		}
	}

	ls.Add('b', b)
}

type Timing struct {
	Start uint64
	Stop  uint64
}

func ParseTiming(s string) (Timing, error) {
	f := strings.Fields(s)
	if len(f) != 2 || !isDigits(f[0]) || !isDigits(f[1]) {
		return Timing{}, fmt.Errorf("t=%q: want <start-time> <stop-time>", s)
	}

	start, err1 := strconv.ParseUint(f[0], 10, 64)
	stop, err2 := strconv.ParseUint(f[1], 10, 64)

	if err := errors.Join(err1, err2); err != nil {
		return Timing{}, fmt.Errorf("t=%q: time out of range", s)
	}

	return Timing{Start: start, Stop: stop}, nil
}

func (t Timing) String() string {
	return strconv.FormatUint(t.Start, 10) + " " + strconv.FormatUint(t.Stop, 10)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

func isToken(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if !isTokenByte(s[i]) {
			return false
		}
	}

	return true
}

func isTokenByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}

	return strings.IndexByte("!#$%&'*+-.^_`{|}~", c) >= 0
}
