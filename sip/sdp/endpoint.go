package sdp

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type Endpoint struct {
	Addr netip.Addr
	Port uint16
}

func (e Endpoint) String() string {
	return netip.AddrPortFrom(e.Addr, e.Port).String()
}

func (s *Session) RTPEndpoint(i int) (Endpoint, error) {
	m := s.Media[i]

	port := m.Port()
	if port == 0 {
		return Endpoint{}, fmt.Errorf("media %d: disabled", i+1)
	}

	a, err := s.mediaAddr(i)
	if err != nil {
		return Endpoint{}, err
	}

	return Endpoint{Addr: a, Port: port}, nil
}

func (s *Session) RTCPEndpoint(i int) (Endpoint, error) {
	rtp, err := s.RTPEndpoint(i)
	if err != nil {
		return Endpoint{}, err
	}

	r, ok, err := s.Media[i].RTCP()
	if err != nil {
		return Endpoint{}, fmt.Errorf("media %d: %w", i+1, err)
	}

	if !ok {
		if rtp.Port == 65535 {
			return Endpoint{}, fmt.Errorf("media %d: no port above %d for RTCP", i+1, rtp.Port)
		}

		return Endpoint{Addr: rtp.Addr, Port: rtp.Port + 1}, nil
	}

	if r.Connection == nil {
		return Endpoint{Addr: rtp.Addr, Port: r.Port}, nil
	}

	a, ok := r.Connection.Addr()
	if !ok {
		return Endpoint{}, fmt.Errorf("media %d: rtcp address %q is not an address literal", i+1, r.Connection.Address)
	}

	return Endpoint{Addr: a, Port: r.Port}, nil
}

func (s *Session) mediaAddr(i int) (netip.Addr, error) {
	c, err := s.MediaConnection(i)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("media %d: %w", i+1, err)
	}

	a, ok := c.Addr()
	if !ok {
		return netip.Addr{}, fmt.Errorf("media %d: c=%s is not an %s address literal", i+1, c, c.AddrType)
	}

	return a, nil
}

func RTCPMuxed(offer, answer *Session, i int) bool {
	if offer == nil || answer == nil || i < 0 || i >= len(offer.Media) || i >= len(answer.Media) {
		return false
	}

	return offer.Media[i].RTCPMux() && answer.Media[i].RTCPMux()
}

var errNotRTP = errors.New("not an RTP media")

type staticPayload struct {
	encoding string
	rate     uint32
	params   string
}

var staticPayloads = map[uint8]staticPayload{
	0:  {"PCMU", 8000, "1"},
	3:  {"GSM", 8000, "1"},
	4:  {"G723", 8000, "1"},
	5:  {"DVI4", 8000, "1"},
	6:  {"DVI4", 16000, "1"},
	7:  {"LPC", 8000, "1"},
	8:  {"PCMA", 8000, "1"},
	9:  {"G722", 8000, "1"},
	10: {"L16", 44100, "2"},
	11: {"L16", 44100, "1"},
	12: {"QCELP", 8000, "1"},
	13: {"CN", 8000, "1"},
	14: {"MPA", 90000, ""},
	15: {"G728", 8000, "1"},
	16: {"DVI4", 11025, "1"},
	17: {"DVI4", 22050, "1"},
	18: {"G729", 8000, "1"},
	25: {"CelB", 90000, ""},
	26: {"JPEG", 90000, ""},
	28: {"nv", 90000, ""},
	31: {"H261", 90000, ""},
	32: {"MPV", 90000, ""},
	33: {"MP2T", 90000, ""},
	34: {"H263", 90000, ""},
}

func (m *Media) Codecs() ([]RTPMap, error) {
	d, err := m.Desc()
	if err != nil {
		return nil, err
	}

	if !isRTP(d.Proto) {
		return nil, fmt.Errorf("m=%s: %w", d, errNotRTP)
	}

	maps, err := m.RTPMaps()

	var out []RTPMap

	for _, f := range d.Formats {
		n, err := strconv.ParseUint(f, 10, 8)
		if err != nil || !isDigits(f) || n > 127 {
			continue
		}

		pt := uint8(n)

		if i := indexPayload(maps, pt); i >= 0 {
			out = append(out, maps[i])
		} else if sp, ok := staticPayloads[pt]; ok {
			out = append(out, RTPMap{Payload: pt, Encoding: sp.encoding, ClockRate: sp.rate, Params: sp.params})
		}
	}

	return out, err
}

func indexPayload(maps []RTPMap, pt uint8) int {
	for i, r := range maps {
		if r.Payload == pt {
			return i
		}
	}

	return -1
}

func (m *Media) PayloadTypes(encoding string) []uint8 {
	codecs, _ := m.Codecs()

	var out []uint8

	for _, c := range codecs {
		if strings.EqualFold(c.Encoding, encoding) {
			out = append(out, c.Payload)
		}
	}

	return out
}

func isRTP(proto string) bool {
	for _, p := range []string{"RTP/", "UDP/TLS/RTP/", "TCP/RTP/", "TCP/TLS/RTP/"} {
		if len(proto) > len(p) && strings.EqualFold(proto[:len(p)], p) {
			return true
		}
	}

	return false
}
