package sdp

import "strings"

const (
	Uplink   = "uplink"
	Downlink = "downlink"
)

const (
	CodecOffer       = "offer"
	CodecAnswer      = "answer"
	CodecDescription = "description"
)

func (m *Media) CodecData(direction, kind string) []byte {
	b := make([]byte, 0, 512)
	b = append(b, direction...)
	b = append(b, '\n')
	b = append(b, kind...)

	for _, l := range m.Lines {
		if !codecDataLine(l) {
			continue
		}

		b = append(b, '\n')
		b = append(b, l.String()...)
	}

	return b
}

func codecDataLine(l Line) bool {
	switch l.Type {
	case 'm':
		return true
	case 'b':
		t, _, _ := strings.Cut(l.Value, ":")
		return t != BandwidthAS && t != BandwidthRS && t != BandwidthRR
	case 'a':
		name, _ := splitAttr(l.Value)
		return !isDirection(l) && !strings.EqualFold(name, "bw-info")
	}

	return false
}
