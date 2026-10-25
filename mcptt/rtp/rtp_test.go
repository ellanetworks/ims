package rtp

import (
	"bytes"
	"reflect"
	"testing"
)

// RFC 3550 §5.1, §5.3.1: the fixed header, two CSRCs, a header extension, padding.
func TestParse(t *testing.T) {
	b := []byte{
		0xb2, 0xe0, 0x12, 0x34, // V=2 P X CC=2; M PT=96; sequence
		0x00, 0x00, 0x03, 0xe8, // timestamp
		0xde, 0xad, 0xbe, 0xef, // SSRC
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02, // CSRCs
		0xbe, 0xde, 0x00, 0x01, 0x10, 0xaa, 0x00, 0x00, // extension: profile, 1 word
		0x01, 0x02, 0x03, // payload
		0x00, 0x00, 0x03, // padding
	}

	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}

	want := Packet{
		Marker: true, PayloadType: 96, Sequence: 0x1234, Timestamp: 1000, SSRC: 0xdeadbeef, CSRC: []uint32{1, 2},
		HasExtension: true, ExtensionProfile: 0xbede, Extension: []byte{0x10, 0xaa, 0, 0}, Payload: []byte{1, 2, 3},
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("Parse %+v", p)
	}

	out, err := p.Append(nil)
	if err != nil {
		t.Fatal(err)
	}

	unpadded := append([]byte{0x92}, b[1:len(b)-3]...)
	if !bytes.Equal(out, unpadded) {
		t.Errorf("Append % x", out)
	}

	for _, b := range [][]byte{
		b[:11],
		append([]byte{0x40}, b[1:12]...),
		{0xa0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // padding of 0
		{0xa0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 9}, // padding beyond the payload
		{0x81, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},             // truncated CSRC list
		{0x90, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, // truncated extension
	} {
		if p, err := Parse(b); err == nil {
			t.Errorf("Parse(% x) = %+v", b, p)
		}
	}

	for _, p := range []Packet{
		{CSRC: make([]uint32, 16)},
		{PayloadType: 128},
		{HasExtension: true, Extension: []byte{1}},
	} {
		if _, err := p.Append(nil); err == nil {
			t.Errorf("Append(%+v)", p)
		}
	}
}

// RFC 5761 §4: RTCP packet types 192–223; RTP with or without the marker outside.
func TestIsRTCP(t *testing.T) {
	for b1, want := range map[byte]bool{191: false, 192: true, 200: true, 204: true, 223: true, 224: false, 0x80 | 8: false, 0: false} {
		if IsRTCP([]byte{0x80, b1}) != want {
			t.Errorf("IsRTCP(%d) != %v", b1, want)
		}
	}

	if IsRTCP([]byte{0x80}) {
		t.Error("IsRTCP of one octet")
	}
}

func FuzzRTP(f *testing.F) {
	f.Add([]byte{0x92, 0xe0, 0x12, 0x34, 0, 0, 3, 0xe8, 0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 1, 0xbe, 0xde, 0, 0, 1})
	f.Add([]byte{0xa0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5, 0, 2})

	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Parse(b)
		if err != nil {
			return
		}

		out, err := p.Append(nil)
		if err != nil {
			t.Fatalf("% x parsed, Append: %v", b, err)
		}

		again, err := Parse(out)
		if err != nil || !reflect.DeepEqual(again, p) {
			t.Fatalf("% x: Append % x parses to %+v, %v; want %+v", b, out, again, err, p)
		}
	})
}
