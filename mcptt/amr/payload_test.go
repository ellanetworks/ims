package amr

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

// ones is a frame of type ft of c, all its bits set.
func ones(c Codec, ft uint8) Frame {
	n, _ := c.Bits(ft)

	b := make([]byte, (n+7)/8)
	for i := range n {
		b[i/8] |= 0x80 >> (i % 8)
	}

	return Frame{Type: ft, Good: true, Bits: b}
}

func roundTrip(t *testing.T, c Codec, p Payload, octetAligned bool) []byte {
	t.Helper()

	b, err := c.AppendPayload(nil, p, octetAligned)
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.ParsePayload(b, octetAligned)
	if err != nil {
		t.Fatal(err)
	}

	if got.CMR != p.CMR || len(got.Frames) != len(p.Frames) {
		t.Fatalf("parsed %+v, want %+v", got, p)
	}

	for i := range p.Frames {
		if got.Frames[i].Type != p.Frames[i].Type || got.Frames[i].Good != p.Frames[i].Good ||
			!bytes.Equal(got.Frames[i].Bits, p.Frames[i].Bits) {
			t.Fatalf("frame %d parsed %+v, want %+v", i, got.Frames[i], p.Frames[i])
		}
	}

	return b
}

// RFC 4867 §4.3.5.1: a bandwidth-efficient AMR payload of one 7.4 kbit/s frame, CMR 15, two padding bits.
func TestBandwidthEfficientSingleFrame(t *testing.T) {
	b := roundTrip(t, AMR, Payload{CMR: 15, Frames: []Frame{ones(AMR, 4)}}, false)

	if len(b) != 20 || b[0] != 0xf2 || b[1] != 0x7f || b[19] != 0xfc {
		t.Fatalf("payload % x", b)
	}
}

// RFC 4867 §4.3.5.2: a bandwidth-efficient AMR-WB payload of four frames: 6.6 kbit/s, SID, NO_DATA, 8.85 kbit/s;
// CMR 1, seven padding bits.
func TestBandwidthEfficientMultipleFrames(t *testing.T) {
	p := Payload{CMR: 1, Frames: []Frame{ones(WB, 0), ones(WB, 9), {Type: FrameNoData, Good: true}, ones(WB, 1)}}
	b := roundTrip(t, WB, p, false)

	if len(b) != 48 || !slices.Equal(b[:4], []byte{0x18, 0x73, 0xfc, 0x3f}) || b[47] != 0x80 {
		t.Fatalf("payload % x", b)
	}
}

// RFC 4867 §4.4.5.1: an octet-aligned AMR payload of two 7.95 kbit/s frames, CMR 6, each frame padded.
func TestOctetAlignedMultipleFrames(t *testing.T) {
	b := roundTrip(t, AMR, Payload{CMR: 6, Frames: []Frame{ones(AMR, 5), ones(AMR, 5)}}, true)

	if len(b) != 43 || !slices.Equal(b[:3], []byte{0x60, 0xac, 0x2c}) || b[22] != 0xfe || b[42] != 0xfe {
		t.Fatalf("payload % x", b)
	}
}

// RFC 4867 §4.3.2: a frame type for future use, 10 to 13 for AMR-WB, makes the payload discarded; a payload shorter
// than its table of contents is refused.
func TestPayloadRefused(t *testing.T) {
	if _, err := WB.ParsePayload([]byte{0xf5, 0x40}, false); !errors.Is(err, ErrFrameType) {
		t.Errorf("FT 10: %v", err)
	}

	if _, err := WB.ParsePayload([]byte{0xf0, 0xc0}, false); !errors.Is(err, ErrShort) {
		t.Errorf("6.6 kbit/s frame without its bits: %v", err)
	}

	if _, err := AMR.ParsePayload([]byte{0xf4}, false); !errors.Is(err, ErrShort) {
		t.Errorf("truncated ToC: %v", err)
	}

	if _, err := WB.AppendPayload(nil, Payload{CMR: 15, Frames: []Frame{{Type: 2, Good: true, Bits: []byte{1}}}}, false); err == nil {
		t.Error("a frame of the wrong length was packed")
	}
}

// RFC 4867 §5.3: a frame in the storage format is its header octet, then its bits octet-aligned.
func TestStorage(t *testing.T) {
	for _, f := range []Frame{ones(WB, 2), ones(WB, 9), {Type: FrameSpeechLost}, {Type: FrameNoData}} {
		b, err := WB.AppendStorage(nil, f)
		if err != nil {
			t.Fatal(err)
		}

		if b[0] != f.Type<<3|map[bool]byte{true: 0x04}[f.Good] {
			t.Errorf("FT %d: header %02x", f.Type, b[0])
		}

		got, n, err := WB.ParseStorage(append(b, 0xaa))
		if err != nil || n != len(b) || got.Type != f.Type || got.Good != f.Good || !bytes.Equal(got.Bits, f.Bits) {
			t.Errorf("FT %d: parsed %+v, %d, %v", f.Type, got, n, err)
		}
	}

	if _, _, err := AMR.ParseStorage([]byte{FrameSpeechLost << 3}); !errors.Is(err, ErrFrameType) {
		t.Errorf("SPEECH_LOST for AMR: %v", err)
	}
}

// A payload that parses packs again to one that parses alike.
func FuzzParsePayload(f *testing.F) {
	f.Add([]byte{0xf2, 0x7f, 0xff}, false)
	f.Add([]byte{0x60, 0xac, 0x2c, 0xff}, true)

	f.Fuzz(func(t *testing.T, b []byte, octetAligned bool) {
		p, err := WB.ParsePayload(b, octetAligned)
		if err != nil {
			return
		}

		out, err := WB.AppendPayload(nil, p, octetAligned)
		if err != nil {
			t.Fatalf("parsed %+v does not pack: %v", p, err)
		}

		q, err := WB.ParsePayload(out, octetAligned)
		if err != nil || q.CMR != p.CMR || len(q.Frames) != len(p.Frames) {
			t.Fatalf("repacked % x parsed %+v, %v; want %+v", out, q, err, p)
		}
	})
}

// TS 26.114 Tables K.5 and K.7: the payload of one AMR-WB frame, CMR 15, in octets, bandwidth-efficient and
// octet-aligned.
func TestPayloadSizes(t *testing.T) {
	for _, tc := range []struct {
		mode               uint8
		efficient, aligned int
	}{
		{0, 18, 19}, {2, 33, 34}, {8, 61, 62},
	} {
		p := Payload{CMR: NoModeRequest, Frames: []Frame{ones(WB, tc.mode)}}

		if b, _ := WB.AppendPayload(nil, p, false); len(b) != tc.efficient {
			t.Errorf("mode %d bandwidth-efficient: %d octets, want %d", tc.mode, len(b), tc.efficient)
		}

		if b, _ := WB.AppendPayload(nil, p, true); len(b) != tc.aligned {
			t.Errorf("mode %d octet-aligned: %d octets, want %d", tc.mode, len(b), tc.aligned)
		}
	}
}
