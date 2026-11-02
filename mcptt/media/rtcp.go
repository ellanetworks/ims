package media

import (
	"encoding/binary"
	"errors"
)

// RFC 3550 section 12.1
// RFC 4585 section 6.1
const (
	rtcpSR    = 200
	rtcpRR    = 201
	rtcpSDES  = 202
	rtcpBYE   = 203
	rtcpRTPFB = 205
	rtcpPSFB  = 206
)

var errRTCP = errors.New("media: malformed RTCP")

// RFC 3550 section 6.4
// RFC 3550 section 6.4.1
// RFC 3550 section 6.5
// RFC 3550 section 6.6
// RFC 3550 section A.2
// RFC 4585 section 6.1
func rtcpSSRCs(b []byte, senders, sources []int) ([]int, []int, error) {
	if len(b) == 0 {
		return senders, sources, errRTCP
	}

	for off := 0; off < len(b); {
		if len(b)-off < 4 || b[off]>>6 != 2 {
			return senders, sources, errRTCP
		}

		n := 4 + 4*int(binary.BigEndian.Uint16(b[off+2:]))
		if n > len(b)-off {
			return senders, sources, errRTCP
		}

		count, typ := int(b[off]&0x1f), b[off+1]
		start, end := off+4, off+n

		if b[off]&0x20 != 0 {
			pad := 0
			if end > start {
				pad = int(b[end-1])
			}

			if pad == 0 || pad > end-start {
				return senders, sources, errRTCP
			}

			end -= pad
		}

		size := end - start

		switch typ {
		case rtcpSR, rtcpRR:
			info := 4
			if typ == rtcpSR {
				info = 24
			}

			if info+24*count > size {
				return senders, sources, errRTCP
			}

			senders = append(senders, start)
			for i := range count {
				sources = append(sources, start+info+24*i)
			}
		case rtcpSDES:
			p := start

			for range count {
				if end-p < 4 {
					return senders, sources, errRTCP
				}

				senders = append(senders, p)

				q := p + 4
				for {
					if q >= end {
						return senders, sources, errRTCP
					}

					if b[q] == 0 {
						break
					}

					if q+2 > end || q+2+int(b[q+1]) > end {
						return senders, sources, errRTCP
					}

					q += 2 + int(b[q+1])
				}

				p = start + (q-start+4)&^3
				if p > end {
					return senders, sources, errRTCP
				}
			}
		case rtcpBYE:
			if 4*count > size {
				return senders, sources, errRTCP
			}

			for i := range count {
				senders = append(senders, start+4*i)
			}
		case rtcpRTPFB, rtcpPSFB:
			if size < 8 {
				return senders, sources, errRTCP
			}

			senders, sources = append(senders, start), append(sources, start+4)
		}

		off += n
	}

	return senders, sources, nil
}
