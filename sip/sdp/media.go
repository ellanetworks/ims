package sdp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	Audio = "audio"
	Video = "video"
	Text  = "text"
)

type MediaDesc struct {
	Type     string
	Port     uint16
	NumPorts uint16
	Proto    string
	Formats  []string
}

func ParseMediaDesc(s string) (MediaDesc, error) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return MediaDesc{}, fmt.Errorf("m=%q: want <media> <port> <proto> <fmt list>", s)
	}

	if !isToken(f[0]) {
		return MediaDesc{}, fmt.Errorf("m=%q: invalid media type", s)
	}

	d := MediaDesc{Type: f[0], Proto: f[2], Formats: f[3:]}

	port, num, hasNum := strings.Cut(f[1], "/")

	p, err := parsePort(port)
	if err != nil {
		return MediaDesc{}, fmt.Errorf("m=%q: invalid port", s)
	}

	d.Port = p

	if hasNum {
		n, err := parsePort(num)
		if err != nil || n == 0 {
			return MediaDesc{}, fmt.Errorf("m=%q: invalid number of ports", s)
		}

		d.NumPorts = n
	}

	if len(d.Formats) == 0 {
		d.Formats = nil
	}

	return d, nil
}

func (d MediaDesc) String() string {
	b := make([]byte, 0, 64)
	b = append(b, d.Type...)
	b = append(b, ' ')
	b = strconv.AppendUint(b, uint64(d.Port), 10)

	if d.NumPorts != 0 {
		b = append(b, '/')
		b = strconv.AppendUint(b, uint64(d.NumPorts), 10)
	}

	b = append(b, ' ')
	b = append(b, d.Proto...)

	for _, f := range d.Formats {
		b = append(b, ' ')
		b = append(b, f...)
	}

	return string(b)
}

func parsePort(s string) (uint16, error) {
	if !isDigits(s) {
		return 0, strconv.ErrSyntax
	}

	n, err := strconv.ParseUint(s, 10, 16)

	return uint16(n), err
}

type Media struct {
	Lines
}

func (m *Media) Clone() *Media {
	return &Media{Lines: slices.Clone(m.Lines)}
}

func (m *Media) Desc() (MediaDesc, error) {
	if len(m.Lines) == 0 || m.Lines[0].Type != 'm' {
		return MediaDesc{}, fmt.Errorf("%w: m=", ErrMissing)
	}

	return ParseMediaDesc(m.Lines[0].Value)
}

func (m *Media) SetDesc(d MediaDesc) {
	if len(m.Lines) == 0 || m.Lines[0].Type != 'm' {
		m.Lines = slices.Insert(m.Lines, 0, Line{Type: 'm'})
	}

	m.Lines[0].Value = d.String()
}

func (m *Media) Type() string {
	d, _ := m.Desc()
	return d.Type
}

func (m *Media) Port() uint16 {
	d, _ := m.Desc()
	return d.Port
}

func (m *Media) Disable() {
	d, err := m.Desc()
	if err != nil {
		return
	}

	d.Port, d.NumPorts = 0, 0
	m.SetDesc(d)
}

func (m *Media) RTPMaps() ([]RTPMap, error) {
	var out []RTPMap

	for _, v := range m.Attrs("rtpmap") {
		r, err := ParseRTPMap(v)
		if err != nil {
			return nil, err
		}

		out = append(out, r)
	}

	return out, nil
}

func (m *Media) Fmtp(format string) (string, bool) {
	for _, v := range m.Attrs("fmtp") {
		if f, params, _ := strings.Cut(v, " "); f == format {
			return strings.TrimLeft(params, " "), true
		}
	}

	return "", false
}

func (m *Media) Ptime() (time.Duration, bool) {
	return m.duration("ptime")
}

func (m *Media) MaxPtime() (time.Duration, bool) {
	return m.duration("maxptime")
}

func (m *Media) duration(name string) (time.Duration, bool) {
	v, ok := m.Attr(name)
	if !ok {
		return 0, false
	}

	whole, frac, hasFrac := strings.Cut(v, ".")
	if !isDigits(whole) || hasFrac && !isDigits(frac) {
		return 0, false
	}

	ms, err := strconv.ParseFloat(v, 64)
	if err != nil || ms <= 0 || ms > 1e6 {
		return 0, false
	}

	return time.Duration(ms * float64(time.Millisecond)), true
}

func (m *Media) RTCP() (RTCP, bool, error) {
	v, ok := m.Attr("rtcp")
	if !ok {
		return RTCP{}, false, nil
	}

	r, err := ParseRTCP(v)

	return r, err == nil, err
}

func (m *Media) RTCPMux() bool {
	return m.HasAttr("rtcp-mux")
}

type RTPMap struct {
	Payload   uint8
	Encoding  string
	ClockRate uint32
	Params    string
}

func ParseRTPMap(s string) (RTPMap, error) {
	pt, enc, ok := strings.Cut(s, " ")
	if !ok {
		return RTPMap{}, fmt.Errorf("rtpmap %q: want <payload type> <encoding>/<clock rate>", s)
	}

	n, err := strconv.ParseUint(pt, 10, 8)
	if err != nil || !isDigits(pt) || n > 127 {
		return RTPMap{}, fmt.Errorf("rtpmap %q: invalid payload type", s)
	}

	parts := strings.SplitN(strings.TrimLeft(enc, " "), "/", 3)
	if len(parts) < 2 || !isToken(parts[0]) || !isDigits(parts[1]) {
		return RTPMap{}, fmt.Errorf("rtpmap %q: want <encoding>/<clock rate>", s)
	}

	rate, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return RTPMap{}, fmt.Errorf("rtpmap %q: invalid clock rate", s)
	}

	r := RTPMap{Payload: uint8(n), Encoding: parts[0], ClockRate: uint32(rate)}

	if len(parts) == 3 {
		if !isToken(parts[2]) {
			return RTPMap{}, fmt.Errorf("rtpmap %q: invalid encoding parameters", s)
		}

		r.Params = parts[2]
	}

	return r, nil
}

func (r RTPMap) String() string {
	s := strconv.Itoa(int(r.Payload)) + " " + r.Encoding + "/" + strconv.FormatUint(uint64(r.ClockRate), 10)
	if r.Params != "" {
		s += "/" + r.Params
	}

	return s
}

type RTCP struct {
	Port       uint16
	Connection *Connection
}

func ParseRTCP(s string) (RTCP, error) {
	f := strings.Fields(s)
	if len(f) != 1 && len(f) != 4 {
		return RTCP{}, fmt.Errorf("rtcp %q: want <port> [<nettype> <addrtype> <address>]", s)
	}

	p, err := parsePort(f[0])
	if err != nil {
		return RTCP{}, fmt.Errorf("rtcp %q: invalid port", s)
	}

	r := RTCP{Port: p}
	if len(f) == 4 {
		r.Connection = &Connection{NetType: f[1], AddrType: f[2], Address: f[3]}
	}

	return r, nil
}

func (r RTCP) String() string {
	s := strconv.FormatUint(uint64(r.Port), 10)
	if r.Connection != nil {
		s += " " + r.Connection.String()
	}

	return s
}

type Direction string

const (
	SendRecv Direction = "sendrecv"
	SendOnly Direction = "sendonly"
	RecvOnly Direction = "recvonly"
	Inactive Direction = "inactive"
)

var directions = []Direction{SendRecv, SendOnly, RecvOnly, Inactive}

func (d Direction) Reverse() Direction {
	switch d {
	case SendOnly:
		return RecvOnly
	case RecvOnly:
		return SendOnly
	}

	return d
}

func (d Direction) Sends() bool {
	return d == SendRecv || d == SendOnly
}

func (d Direction) Receives() bool {
	return d == SendRecv || d == RecvOnly
}

func Effective(offer, answer Direction) Direction {
	if offer == Inactive {
		return Inactive
	}

	return answer
}

func (ls Lines) Direction() (Direction, bool) {
	for _, l := range ls {
		if l.Type == 'a' && slices.Contains(directions, Direction(l.Value)) {
			return Direction(l.Value), true
		}
	}

	return "", false
}

func (ls *Lines) SetDirection(d Direction) {
	i := slices.IndexFunc(*ls, isDirection)
	if i < 0 {
		ls.AddAttr(string(d), "")
		return
	}

	(*ls)[i].Value = string(d)
	tail := (*ls)[i+1:]
	*ls = append((*ls)[:i+1], slices.DeleteFunc(tail, isDirection)...)
}

func isDirection(l Line) bool {
	return l.Type == 'a' && slices.Contains(directions, Direction(l.Value))
}
