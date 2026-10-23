package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// RTCP packet types (RFC 3550 §12.1).
const (
	TypeSR   = 200
	TypeRR   = 201
	TypeSDES = 202
	TypeBYE  = 203
	TypeAPP  = 204
)

// RTCPPacket is one packet of an RTCP datagram: *SenderReport, *ReceiverReport, *SourceDescription, *Goodbye, *App or
// *RawRTCP.
type RTCPPacket interface {
	// body appends the packet's body to dst and returns its count field.
	body(dst []byte) ([]byte, uint8, error)
	packetType() uint8
}

// ReceptionReport is a report block of an SR or RR (RFC 3550 §6.4.1).
type ReceptionReport struct {
	SSRC         uint32
	FractionLost uint8
	// CumulativeLost is the signed 24-bit cumulative number of packets lost.
	CumulativeLost   int32
	HighestSequence  uint32
	Jitter           uint32
	LastSR           uint32
	DelaySinceLastSR uint32
}

const reportSize = 24

// SenderReport is an SR (RFC 3550 §6.4.1).
type SenderReport struct {
	SSRC        uint32
	NTPTime     uint64
	RTPTime     uint32
	PacketCount uint32
	OctetCount  uint32
	Reports     []ReceptionReport
	// Extension is the profile-specific extension, a multiple of 4 octets.
	Extension []byte
}

// ReceiverReport is an RR (RFC 3550 §6.4.2).
type ReceiverReport struct {
	SSRC      uint32
	Reports   []ReceptionReport
	Extension []byte
}

// SDESType is the type of an SDES item (RFC 3550 §6.5).
type SDESType uint8

const (
	SDESCNAME SDESType = 1
	SDESName  SDESType = 2
	SDESEmail SDESType = 3
	SDESPhone SDESType = 4
	SDESLoc   SDESType = 5
	SDESTool  SDESType = 6
	SDESNote  SDESType = 7
	// SDESPriv is a PRIV item: its Text holds the prefix length, the prefix and the value (§6.5.8).
	SDESPriv SDESType = 8
)

// SourceDescription is an SDES (RFC 3550 §6.5).
type SourceDescription struct {
	Chunks []SDESChunk
}

// SDESChunk is the items of one source.
type SDESChunk struct {
	Source uint32
	Items  []SDESItem
}

// SDESItem is an item of a chunk; its Text is at most 255 octets.
type SDESItem struct {
	Type SDESType
	Text string
}

// Goodbye is a BYE (RFC 3550 §6.6).
type Goodbye struct {
	Sources []uint32
	// Reason is at most 255 octets.
	Reason string
}

// App is an APP (RFC 3550 §6.7).
type App struct {
	// Subtype is 5 bits.
	Subtype uint8
	SSRC    uint32
	Name    [4]byte
	// Data is a multiple of 4 octets.
	Data []byte
}

// RawRTCP is a packet of a type the package does not decode.
type RawRTCP struct {
	Type  uint8
	Count uint8
	Body  []byte
}

// ParseRTCP parses the RTCP packets of b: a compound packet (RFC 3550 §6.1), or the packets of the media plane control
// protocols of TS 24.380 §8.1.1, outside compound packets. The packets' lengths add up to b's (§A.2). A packet's
// padding is stripped. On an error, the packets before it are returned: after a packet of a malformed body, those
// after it too, its length framing them.
func ParseRTCP(b []byte) ([]RTCPPacket, error) {
	var (
		ps   []RTCPPacket
		errs []error
	)

	for len(b) > 0 {
		if len(b) < 4 {
			return ps, errors.Join(append(errs, fmt.Errorf("rtp: RTCP packet of %d octets", len(b)))...)
		}

		if b[0]>>6 != version {
			return ps, errors.Join(append(errs, fmt.Errorf("rtp: RTCP version %d", b[0]>>6))...)
		}

		n := 4 + 4*int(binary.BigEndian.Uint16(b[2:]))
		if n > len(b) {
			return ps, errors.Join(append(errs, fmt.Errorf("rtp: RTCP packet of %d octets, %d left", n, len(b)))...)
		}

		body := b[4:n:n]
		head := b[:2]
		b = b[n:]

		if head[0]&0x20 != 0 {
			pad := len(body)
			if pad > 0 {
				pad = int(body[len(body)-1])
			}

			if pad == 0 || pad > len(body) {
				errs = append(errs, fmt.Errorf("rtp: RTCP padding of %d octets", pad))
				continue
			}

			body = body[: len(body)-pad : len(body)-pad]
		}

		p, err := parseRTCP(head[1], head[0]&0x1f, body)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		ps = append(ps, p)
	}

	if len(ps) == 0 && len(errs) == 0 {
		return nil, errors.New("rtp: no RTCP packet")
	}

	return ps, errors.Join(errs...)
}

func parseRTCP(typ, count uint8, b []byte) (RTCPPacket, error) {
	switch typ {
	case TypeSR:
		if len(b) < 24 {
			return nil, errors.New("rtp: truncated SR")
		}

		sr := &SenderReport{
			SSRC:        binary.BigEndian.Uint32(b),
			NTPTime:     binary.BigEndian.Uint64(b[4:]),
			RTPTime:     binary.BigEndian.Uint32(b[12:]),
			PacketCount: binary.BigEndian.Uint32(b[16:]),
			OctetCount:  binary.BigEndian.Uint32(b[20:]),
		}

		var err error
		if sr.Reports, sr.Extension, err = parseReports(count, b[24:]); err != nil {
			return nil, err
		}

		return sr, nil
	case TypeRR:
		if len(b) < 4 {
			return nil, errors.New("rtp: truncated RR")
		}

		rr := &ReceiverReport{SSRC: binary.BigEndian.Uint32(b)}

		var err error
		if rr.Reports, rr.Extension, err = parseReports(count, b[4:]); err != nil {
			return nil, err
		}

		return rr, nil
	case TypeSDES:
		return parseSDES(count, b)
	case TypeBYE:
		return parseBYE(count, b)
	case TypeAPP:
		if len(b) < 8 || len(b)%4 != 0 {
			return nil, fmt.Errorf("rtp: APP of %d octets", len(b))
		}

		app := &App{Subtype: count, SSRC: binary.BigEndian.Uint32(b), Data: b[8:]}
		copy(app.Name[:], b[4:8])

		return app, nil
	}

	return &RawRTCP{Type: typ, Count: count, Body: b}, nil
}

func parseReports(count uint8, b []byte) ([]ReceptionReport, []byte, error) {
	if int(count)*reportSize > len(b) {
		return nil, nil, fmt.Errorf("rtp: %d report blocks in %d octets", count, len(b))
	}

	var rs []ReceptionReport

	for i := range int(count) {
		r := b[i*reportSize:]
		lost := int32(binary.BigEndian.Uint32(r[4:])<<8) >> 8
		rs = append(rs, ReceptionReport{
			SSRC:             binary.BigEndian.Uint32(r),
			FractionLost:     r[4],
			CumulativeLost:   lost,
			HighestSequence:  binary.BigEndian.Uint32(r[8:]),
			Jitter:           binary.BigEndian.Uint32(r[12:]),
			LastSR:           binary.BigEndian.Uint32(r[16:]),
			DelaySinceLastSR: binary.BigEndian.Uint32(r[20:]),
		})
	}

	ext := b[int(count)*reportSize:]
	if len(ext) == 0 {
		ext = nil
	}

	return rs, ext, nil
}

// parseSDES parses the chunks of an SDES: each a source and items to a null octet, padded to 32 bits (§6.5).
func parseSDES(count uint8, b []byte) (*SourceDescription, error) {
	sd := &SourceDescription{}

	for range int(count) {
		if len(b) < 4 {
			return nil, errors.New("rtp: truncated SDES chunk")
		}

		c := SDESChunk{Source: binary.BigEndian.Uint32(b)}
		off := 4

		for {
			if off >= len(b) {
				return nil, errors.New("rtp: SDES chunk without end")
			}

			if b[off] == 0 {
				break
			}

			if off+2 > len(b) || off+2+int(b[off+1]) > len(b) {
				return nil, errors.New("rtp: truncated SDES item")
			}

			c.Items = append(c.Items, SDESItem{Type: SDESType(b[off]), Text: string(b[off+2 : off+2+int(b[off+1])])})
			off += 2 + int(b[off+1])
		}

		off = (off + 4) &^ 3
		if off > len(b) {
			return nil, errors.New("rtp: truncated SDES chunk")
		}

		sd.Chunks = append(sd.Chunks, c)
		b = b[off:]
	}

	return sd, nil
}

func parseBYE(count uint8, b []byte) (*Goodbye, error) {
	if int(count)*4 > len(b) {
		return nil, fmt.Errorf("rtp: BYE of %d sources in %d octets", count, len(b))
	}

	bye := &Goodbye{}
	for i := range int(count) {
		bye.Sources = append(bye.Sources, binary.BigEndian.Uint32(b[4*i:]))
	}

	if rest := b[4*int(count):]; len(rest) > 0 {
		n := int(rest[0])
		if 1+n > len(rest) {
			return nil, errors.New("rtp: truncated BYE reason")
		}

		bye.Reason = string(rest[1 : 1+n])
	}

	return bye, nil
}

// AppendRTCP appends the packets ps to dst, padding a packet whose body is not a multiple of 32 bits (§6.4.1).
func AppendRTCP(dst []byte, ps ...RTCPPacket) ([]byte, error) {
	for _, p := range ps {
		start := len(dst)
		dst = append(dst, 0, 0, 0, 0)

		var (
			count uint8
			err   error
		)

		if dst, count, err = p.body(dst); err != nil {
			return dst[:start], err
		}

		b0 := byte(version<<6) | count

		if pad := (4 - (len(dst)-start)%4) % 4; pad != 0 {
			dst = append(dst, make([]byte, pad-1)...)
			dst = append(dst, byte(pad))
			b0 |= 0x20
		}

		n := len(dst) - start - 4
		if count > 31 || n/4 > 0xffff {
			return dst[:start], fmt.Errorf("rtp: RTCP packet %d: count %d, body of %d octets", p.packetType(), count, n)
		}

		dst[start] = b0
		dst[start+1] = p.packetType()
		binary.BigEndian.PutUint16(dst[start+2:], uint16(n/4))
	}

	return dst, nil
}

func (sr *SenderReport) packetType() uint8 { return TypeSR }

func (sr *SenderReport) body(dst []byte) ([]byte, uint8, error) {
	dst = binary.BigEndian.AppendUint32(dst, sr.SSRC)
	dst = binary.BigEndian.AppendUint64(dst, sr.NTPTime)
	dst = binary.BigEndian.AppendUint32(dst, sr.RTPTime)
	dst = binary.BigEndian.AppendUint32(dst, sr.PacketCount)
	dst = binary.BigEndian.AppendUint32(dst, sr.OctetCount)

	return appendReports(dst, sr.Reports, sr.Extension)
}

func (rr *ReceiverReport) packetType() uint8 { return TypeRR }

func (rr *ReceiverReport) body(dst []byte) ([]byte, uint8, error) {
	return appendReports(binary.BigEndian.AppendUint32(dst, rr.SSRC), rr.Reports, rr.Extension)
}

func appendReports(dst []byte, rs []ReceptionReport, ext []byte) ([]byte, uint8, error) {
	if len(rs) > 31 {
		return dst, 0, fmt.Errorf("rtp: %d report blocks", len(rs))
	}

	for _, r := range rs {
		if r.CumulativeLost < -1<<23 || r.CumulativeLost >= 1<<23 {
			return dst, 0, fmt.Errorf("rtp: cumulative lost %d", r.CumulativeLost)
		}

		dst = binary.BigEndian.AppendUint32(dst, r.SSRC)
		dst = binary.BigEndian.AppendUint32(dst, uint32(r.FractionLost)<<24|uint32(r.CumulativeLost)&0xffffff)
		dst = binary.BigEndian.AppendUint32(dst, r.HighestSequence)
		dst = binary.BigEndian.AppendUint32(dst, r.Jitter)
		dst = binary.BigEndian.AppendUint32(dst, r.LastSR)
		dst = binary.BigEndian.AppendUint32(dst, r.DelaySinceLastSR)
	}

	return append(dst, ext...), uint8(len(rs)), nil
}

func (sd *SourceDescription) packetType() uint8 { return TypeSDES }

func (sd *SourceDescription) body(dst []byte) ([]byte, uint8, error) {
	if len(sd.Chunks) > 31 {
		return dst, 0, fmt.Errorf("rtp: %d SDES chunks", len(sd.Chunks))
	}

	for _, c := range sd.Chunks {
		start := len(dst)
		dst = binary.BigEndian.AppendUint32(dst, c.Source)

		for _, it := range c.Items {
			if it.Type == 0 || len(it.Text) > 0xff {
				return dst, 0, fmt.Errorf("rtp: SDES item %d of %d octets", it.Type, len(it.Text))
			}

			dst = append(dst, byte(it.Type), byte(len(it.Text)))
			dst = append(dst, it.Text...)
		}

		dst = append(dst, 0)
		for (len(dst)-start)%4 != 0 {
			dst = append(dst, 0)
		}
	}

	return dst, uint8(len(sd.Chunks)), nil
}

func (bye *Goodbye) packetType() uint8 { return TypeBYE }

func (bye *Goodbye) body(dst []byte) ([]byte, uint8, error) {
	if len(bye.Sources) > 31 || len(bye.Reason) > 0xff {
		return dst, 0, fmt.Errorf("rtp: BYE of %d sources, reason of %d octets", len(bye.Sources), len(bye.Reason))
	}

	for _, s := range bye.Sources {
		dst = binary.BigEndian.AppendUint32(dst, s)
	}

	if bye.Reason != "" {
		dst = append(dst, byte(len(bye.Reason)))
		dst = append(dst, bye.Reason...)
		dst = append(dst, make([]byte, (4-(1+len(bye.Reason))%4)%4)...)
	}

	return dst, uint8(len(bye.Sources)), nil
}

func (app *App) packetType() uint8 { return TypeAPP }

func (app *App) body(dst []byte) ([]byte, uint8, error) {
	if app.Subtype > 31 || len(app.Data)%4 != 0 {
		return dst, 0, fmt.Errorf("rtp: APP subtype %d, data of %d octets", app.Subtype, len(app.Data))
	}

	dst = binary.BigEndian.AppendUint32(dst, app.SSRC)
	dst = append(dst, app.Name[:]...)

	return append(dst, app.Data...), app.Subtype, nil
}

func (r *RawRTCP) packetType() uint8 { return r.Type }

func (r *RawRTCP) body(dst []byte) ([]byte, uint8, error) {
	return append(dst, r.Body...), r.Count, nil
}
