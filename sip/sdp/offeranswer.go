package sdp

import (
	"crypto/rand"
	"encoding/binary"
	"strconv"
)

// RFC 3264 section 6
func Refusal(offer []byte) ([]byte, error) {
	s, err := Parse(offer)
	if err != nil {
		return nil, err
	}

	if o, err := s.Origin(); err == nil {
		var b [4]byte

		_, _ = rand.Read(b[:])

		o.Username, o.SessionID, o.SessionVersion = "-", strconv.FormatUint(uint64(binary.BigEndian.Uint32(b[:])>>1), 10), "1"
		s.SetOrigin(o)
	}

	for _, m := range s.Media {
		m.Disable()
	}

	return s.Bytes(), nil
}

// RFC 3264 section 8
func Unchanged(prev, next []byte) bool {
	p, err := Parse(prev)
	if err != nil {
		return false
	}

	n, err := Parse(next)
	if err != nil {
		return false
	}

	po, err := p.Origin()
	if err != nil {
		return false
	}

	no, err := n.Origin()

	return err == nil && po == no
}
