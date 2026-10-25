// Package rtp is the coding of RTP and RTCP packets (RFC 3550) and their demultiplexing on one port (RFC 5761).
//
// Parsing aliases the buffer parsed: payloads, extensions and application data are slices of it.
package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// version is the version of RTP and RTCP (RFC 3550 §5.1).
const version = 2

// headerSize is the size of the fixed RTP header (RFC 3550 §5.1).
const headerSize = 12

// Packet is an RTP data packet (RFC 3550 §5.1). Padding is not kept: Parse strips it and Append adds none.
type Packet struct {
	Marker      bool
	PayloadType uint8
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
	CSRC        []uint32
	// Extension is the header extension of RFC 3550 §5.3.1, if HasExtension: its profile-defined 16 bits and its
	// data, a multiple of 4 octets.
	HasExtension     bool
	ExtensionProfile uint16
	Extension        []byte
	Payload          []byte
}

// Parse parses the RTP packet b.
func Parse(b []byte) (Packet, error) {
	if len(b) < headerSize {
		return Packet{}, fmt.Errorf("rtp: packet of %d octets", len(b))
	}

	if b[0]>>6 != version {
		return Packet{}, fmt.Errorf("rtp: version %d", b[0]>>6)
	}

	p := Packet{
		Marker:      b[1]&0x80 != 0,
		PayloadType: b[1] & 0x7f,
		Sequence:    binary.BigEndian.Uint16(b[2:]),
		Timestamp:   binary.BigEndian.Uint32(b[4:]),
		SSRC:        binary.BigEndian.Uint32(b[8:]),
	}

	end := len(b)

	if b[0]&0x20 != 0 {
		n := int(b[end-1])
		if n == 0 || n > end-headerSize {
			return Packet{}, fmt.Errorf("rtp: padding of %d octets", n)
		}

		end -= n
	}

	off := headerSize

	if cc := int(b[0] & 0x0f); cc > 0 {
		if off+4*cc > end {
			return Packet{}, errors.New("rtp: truncated CSRC list")
		}

		p.CSRC = make([]uint32, cc)
		for i := range p.CSRC {
			p.CSRC[i] = binary.BigEndian.Uint32(b[off+4*i:])
		}

		off += 4 * cc
	}

	if b[0]&0x10 != 0 {
		if off+4 > end {
			return Packet{}, errors.New("rtp: truncated header extension")
		}

		p.HasExtension = true
		p.ExtensionProfile = binary.BigEndian.Uint16(b[off:])
		n := 4 * int(binary.BigEndian.Uint16(b[off+2:]))
		off += 4

		if off+n > end {
			return Packet{}, errors.New("rtp: truncated header extension")
		}

		p.Extension = b[off : off+n : off+n]
		off += n
	}

	p.Payload = b[off:end:end]

	return p, nil
}

// Append appends the packet p to dst.
func (p *Packet) Append(dst []byte) ([]byte, error) {
	if len(p.CSRC) > 15 {
		return dst, fmt.Errorf("rtp: %d CSRCs", len(p.CSRC))
	}

	if p.PayloadType > 0x7f {
		return dst, fmt.Errorf("rtp: payload type %d", p.PayloadType)
	}

	if p.HasExtension && (len(p.Extension)%4 != 0 || len(p.Extension) > 4*0xffff) {
		return dst, fmt.Errorf("rtp: header extension of %d octets", len(p.Extension))
	}

	b0 := byte(version<<6) | byte(len(p.CSRC))
	if p.HasExtension {
		b0 |= 0x10
	}

	b1 := p.PayloadType
	if p.Marker {
		b1 |= 0x80
	}

	dst = append(dst, b0, b1)
	dst = binary.BigEndian.AppendUint16(dst, p.Sequence)
	dst = binary.BigEndian.AppendUint32(dst, p.Timestamp)
	dst = binary.BigEndian.AppendUint32(dst, p.SSRC)

	for _, c := range p.CSRC {
		dst = binary.BigEndian.AppendUint32(dst, c)
	}

	if p.HasExtension {
		dst = binary.BigEndian.AppendUint16(dst, p.ExtensionProfile)
		dst = binary.BigEndian.AppendUint16(dst, uint16(len(p.Extension)/4))
		dst = append(dst, p.Extension...)
	}

	return append(dst, p.Payload...), nil
}

// IsRTCP reports whether b, received on a port RTP and RTCP share, is RTCP: its second octet, the RTCP packet type
// or the RTP marker and payload type, is in 192–223 (RFC 5761 §4), as payload types of RTP are chosen to keep it.
func IsRTCP(b []byte) bool {
	return len(b) >= 2 && b[1] >= 192 && b[1] <= 223
}
