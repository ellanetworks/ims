package amr

import (
	"errors"
	"fmt"
)

// RFC 4867 section 4.3.2
// TS 26.101
// TS 26.201 clause 4.1.1
const (
	FrameSpeechLost = 14
	FrameNoData     = 15
)

// RFC 4867 section 4.3.1
const NoModeRequest = 15

// RFC 4867 section 4.3.3
type Frame struct {
	Type uint8
	Good bool
	Bits []byte
}

// RFC 4867 section 4.3
// RFC 4867 section 4.4
type Payload struct {
	CMR    uint8
	Frames []Frame
}

var (
	ErrFrameType = errors.New("amr: frame type for future use")
	ErrShort     = errors.New("amr: payload shorter than its table of contents")
)

func (c Codec) Bits(ft uint8) (int, bool) {
	switch {
	case int(ft) < len(c.FrameBits):
		return c.FrameBits[ft], true
	case ft == c.SID:
		return c.SIDBits, true
	case ft == FrameSpeechLost && c.SpeechLost, ft == FrameNoData:
		return 0, true
	}

	return 0, false
}

func (c Codec) check(f Frame) (int, error) {
	n, ok := c.Bits(f.Type)
	if !ok {
		return 0, fmt.Errorf("%w: %d", ErrFrameType, f.Type)
	}

	if len(f.Bits) != (n+7)/8 {
		return 0, fmt.Errorf("amr: frame type %d of %d octets, want %d", f.Type, len(f.Bits), (n+7)/8)
	}

	return n, nil
}

// RFC 4867 section 5.3
func (c Codec) ParseStorage(b []byte) (Frame, int, error) {
	if len(b) == 0 {
		return Frame{}, 0, ErrShort
	}

	f := Frame{Type: b[0] >> 3 & 0x0f, Good: b[0]&0x04 != 0}

	n, ok := c.Bits(f.Type)
	if !ok {
		return Frame{}, 0, fmt.Errorf("%w: %d", ErrFrameType, f.Type)
	}

	size := 1 + (n+7)/8
	if len(b) < size {
		return Frame{}, 0, ErrShort
	}

	f.Bits = append([]byte(nil), b[1:size]...)

	return f, size, nil
}

// RFC 4867 section 5.3
func (c Codec) AppendStorage(dst []byte, f Frame) ([]byte, error) {
	if _, err := c.check(f); err != nil {
		return dst, err
	}

	h := f.Type << 3
	if f.Good {
		h |= 0x04
	}

	return append(append(dst, h), f.Bits...), nil
}

// RFC 4867 section 4.3
// RFC 4867 section 4.4
// RFC 4867 section 4.3.2
func (c Codec) ParsePayload(b []byte, octetAligned bool) (Payload, error) {
	r := bitReader{b: b}

	var p Payload

	p.CMR = uint8(r.read(4))

	if octetAligned {
		r.read(4)
	}

	var sizes []int

	entry := 6
	if octetAligned {
		entry = 8
	}

	for more := true; more; {
		if r.left() < entry {
			return Payload{}, ErrShort
		}

		more = r.read(1) == 1
		f := Frame{Type: uint8(r.read(4)), Good: r.read(1) == 1}

		if octetAligned {
			r.read(2)
		}

		n, ok := c.Bits(f.Type)
		if !ok {
			return Payload{}, fmt.Errorf("%w: %d", ErrFrameType, f.Type)
		}

		p.Frames = append(p.Frames, f)
		sizes = append(sizes, n)
	}

	for i, n := range sizes {
		if r.left() < n {
			return Payload{}, ErrShort
		}

		bits := make([]byte, (n+7)/8)
		for j := range n {
			if r.read(1) == 1 {
				bits[j/8] |= 0x80 >> (j % 8)
			}
		}

		p.Frames[i].Bits = bits

		if octetAligned {
			r.align()
		}
	}

	return p, nil
}

// RFC 4867 section 4.3.4
// RFC 4867 section 4.4.4
func (c Codec) AppendPayload(dst []byte, p Payload, octetAligned bool) ([]byte, error) {
	if len(p.Frames) == 0 {
		return dst, errors.New("amr: payload without a frame")
	}

	sizes := make([]int, len(p.Frames))

	for i, f := range p.Frames {
		n, err := c.check(f)
		if err != nil {
			return dst, err
		}

		sizes[i] = n
	}

	w := bitWriter{b: dst}

	w.write(uint32(p.CMR), 4)

	if octetAligned {
		w.write(0, 4)
	}

	for i, f := range p.Frames {
		w.write(boolBit(i < len(p.Frames)-1), 1)
		w.write(uint32(f.Type), 4)
		w.write(boolBit(f.Good), 1)

		if octetAligned {
			w.write(0, 2)
		}
	}

	for i, f := range p.Frames {
		for j := range sizes[i] {
			w.write(uint32(f.Bits[j/8]>>(7-j%8)&1), 1)
		}

		if octetAligned {
			w.align()
		}
	}

	w.align()

	return w.b, nil
}

func boolBit(b bool) uint32 {
	if b {
		return 1
	}

	return 0
}

type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) left() int { return len(r.b)*8 - r.pos }

func (r *bitReader) read(n int) uint32 {
	var v uint32

	for range n {
		v <<= 1

		if r.pos < len(r.b)*8 {
			v |= uint32(r.b[r.pos/8] >> (7 - r.pos%8) & 1)
		}

		r.pos++
	}

	return v
}

func (r *bitReader) align() { r.pos = (r.pos + 7) / 8 * 8 }

type bitWriter struct {
	b []byte
	n int
}

func (w *bitWriter) write(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}

		w.b[len(w.b)-1] |= byte(v>>i&1) << (7 - w.n%8)
		w.n++
	}
}

func (w *bitWriter) align() { w.n = (w.n + 7) / 8 * 8 }
