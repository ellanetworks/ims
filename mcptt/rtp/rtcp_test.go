package rtp

import (
	"bytes"
	"reflect"
	"testing"
)

// RFC 3550 §6.4.1, §6.5, §6.1: a compound packet of an SR with a report block and an SDES with a CNAME; then an RR,
// a BYE with a reason, an APP, a packet of another type and, last, padding.
func TestParseRTCP(t *testing.T) {
	b := []byte{
		// SR, one report block.
		0x81, 200, 0x00, 0x0c,
		0x00, 0x00, 0x00, 0x01, // SSRC
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, // NTP
		0x00, 0x00, 0x00, 0x64, // RTP time
		0x00, 0x00, 0x00, 0x0a, // packets
		0x00, 0x00, 0x03, 0x20, // octets
		0x00, 0x00, 0x00, 0x02, 0x10, 0xff, 0xff, 0xfe, // SSRC, fraction lost, cumulative lost -2
		0x00, 0x01, 0x00, 0x05, 0x00, 0x00, 0x00, 0x07, 0x11, 0x22, 0x33, 0x44, 0x00, 0x00, 0x00, 0x09,
		// SDES, one chunk: CNAME "ab", then the null octet ending the items, padded to 32 bits.
		0x81, 202, 0x00, 0x03,
		0x00, 0x00, 0x00, 0x01, 0x01, 0x02, 'a', 'b', 0x00, 0x00, 0x00, 0x00,
		// RR without report blocks.
		0x80, 201, 0x00, 0x01, 0x00, 0x00, 0x00, 0x03,
		// BYE of one source, reason "x" padded.
		0x81, 203, 0x00, 0x02, 0x00, 0x00, 0x00, 0x03, 0x01, 'x', 0x00, 0x00,
		// APP, subtype 5, name MCPT, one word of data.
		0x85, 204, 0x00, 0x03, 0x00, 0x00, 0x00, 0x04, 'M', 'C', 'P', 'T', 0x01, 0x02, 0x03, 0x04,
		// RTPFB (RFC 4585), one word, padded by 4 octets.
		0xa1, 205, 0x00, 0x02, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x04,
	}

	ps, err := ParseRTCP(b)
	if err != nil {
		t.Fatal(err)
	}

	want := []RTCPPacket{
		&SenderReport{
			SSRC: 1, NTPTime: 0x0102030405060708, RTPTime: 100, PacketCount: 10, OctetCount: 800,
			Reports: []ReceptionReport{{
				SSRC: 2, FractionLost: 0x10, CumulativeLost: -2, HighestSequence: 0x10005, Jitter: 7,
				LastSR: 0x11223344, DelaySinceLastSR: 9,
			}},
		},
		&SourceDescription{Chunks: []SDESChunk{{Source: 1, Items: []SDESItem{{SDESCNAME, "ab"}}}}},
		&ReceiverReport{SSRC: 3},
		&Goodbye{Sources: []uint32{3}, Reason: "x"},
		&App{Subtype: 5, SSRC: 4, Name: [4]byte{'M', 'C', 'P', 'T'}, Data: []byte{1, 2, 3, 4}},
		&RawRTCP{Type: 205, Count: 1, Body: []byte{0, 0, 0, 5}},
	}
	if !reflect.DeepEqual(ps, want) {
		t.Fatalf("ParseRTCP %+v", ps)
	}

	out, err := AppendRTCP(nil, ps...)
	if err != nil {
		t.Fatal(err)
	}

	// As parsed, but the padding of the last packet.
	unpadded := append(b[:len(b)-12:len(b)-12], 0x81, 205, 0x00, 0x01, 0x00, 0x00, 0x00, 0x05)
	if !bytes.Equal(out, unpadded) {
		t.Errorf("AppendRTCP\n% x\nwant\n% x", out, unpadded)
	}
}

// RFC 3550 §A.2: the lengths add up to the datagram's; the version is 2; padding within the packet.
func TestParseRTCPErrors(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{0x80, 201, 0x00},
		{0x40, 201, 0x00, 0x01, 0, 0, 0, 1}, // version 1
		{0x80, 201, 0x00, 0x02, 0, 0, 0, 1}, // longer than the datagram
		{0x80, 201, 0x00, 0x01, 0, 0, 0, 1, 0x80},           // trailing octet
		{0xa0, 201, 0x00, 0x01, 0, 0, 0, 0},                 // padding of 0
		{0xa0, 201, 0x00, 0x01, 0, 0, 0, 5},                 // padding beyond the packet
		{0x80, 200, 0x00, 0x01, 0, 0, 0, 1},                 // truncated SR
		{0x81, 201, 0x00, 0x01, 0, 0, 0, 1},                 // report block missing
		{0x81, 202, 0x00, 0x02, 0, 0, 0, 1, 1, 2, 'a', 'b'}, // SDES chunk without end
		{0x81, 202, 0x00, 0x02, 0, 0, 0, 1, 1, 9, 'a', 'b'}, // SDES item beyond the chunk
		{0x82, 203, 0x00, 0x01, 0, 0, 0, 1},                 // BYE source missing
		{0x81, 203, 0x00, 0x02, 0, 0, 0, 1, 9, 'x', 0, 0},   // BYE reason beyond the packet
		{0x80, 204, 0x00, 0x01, 0, 0, 0, 1},                 // APP without name
	} {
		if ps, err := ParseRTCP(b); err == nil {
			t.Errorf("ParseRTCP(% x) = %+v", b, ps)
		}
	}

	// TS 24.380 §8.1.1: of several packets in a datagram, those well formed are kept, a malformed body skipped by its
	// length.
	ps, err := ParseRTCP([]byte{
		0x80, 204, 0x00, 0x01, 0, 0, 0, 1, // APP without name
		0x85, 204, 0x00, 0x02, 0, 0, 0, 4, 'M', 'C', 'P', 'T',
		0x80, 201, 0x00, 0x05, 0, 0, 0, 1, // longer than the datagram
	})
	if err == nil || len(ps) != 1 || ps[0].(*App).SSRC != 4 {
		t.Errorf("%+v, %v", ps, err)
	}
}

// RFC 3550 §6.4.1: a body not a multiple of 32 bits is padded.
func TestAppendRTCPPadding(t *testing.T) {
	b, err := AppendRTCP(nil, &RawRTCP{Type: 205, Body: []byte{1, 2, 3, 4, 5}})
	if err != nil || !bytes.Equal(b, []byte{0xa0, 205, 0, 2, 1, 2, 3, 4, 5, 0, 0, 3}) {
		t.Fatalf("% x, %v", b, err)
	}

	if ps, err := ParseRTCP(b); err != nil || !reflect.DeepEqual(ps, []RTCPPacket{&RawRTCP{Type: 205, Body: []byte{1, 2, 3, 4, 5}}}) {
		t.Errorf("%+v, %v", ps, err)
	}
}

func TestAppendRTCPErrors(t *testing.T) {
	for _, p := range []RTCPPacket{
		&ReceiverReport{Reports: make([]ReceptionReport, 32)},
		&ReceiverReport{Reports: []ReceptionReport{{CumulativeLost: 1 << 23}}},
		&SourceDescription{Chunks: []SDESChunk{{Items: []SDESItem{{Type: 0, Text: "x"}}}}},
		&SourceDescription{Chunks: []SDESChunk{{Items: []SDESItem{{Type: SDESNote, Text: string(make([]byte, 256))}}}}},
		&Goodbye{Reason: string(make([]byte, 256))},
		&App{Subtype: 32},
		&App{Data: []byte{1}},
		&RawRTCP{Type: 205, Count: 32},
	} {
		if b, err := AppendRTCP([]byte{9}, p); err == nil || !bytes.Equal(b, []byte{9}) {
			t.Errorf("AppendRTCP(%+v) = % x, %v", p, b, err)
		}
	}
}

func FuzzRTCP(f *testing.F) {
	f.Add([]byte{
		0x81, 200, 0, 12, 0, 0, 0, 1, 1, 2, 3, 4, 5, 6, 7, 8, 0, 0, 0, 100, 0, 0, 0, 10, 0, 0, 3, 32,
		0, 0, 0, 2, 0x10, 0xff, 0xff, 0xfe, 0, 1, 0, 5, 0, 0, 0, 7, 0x11, 0x22, 0x33, 0x44, 0, 0, 0, 9,
		0x81, 202, 0, 3, 0, 0, 0, 1, 1, 2, 'a', 'b', 0, 0, 0, 0,
	})
	f.Add([]byte{0x81, 203, 0, 2, 0, 0, 0, 3, 1, 'x', 0, 0, 0x85, 204, 0, 3, 0, 0, 0, 4, 'M', 'C', 'P', 'T', 1, 2, 3, 4})
	f.Add([]byte{0xa1, 205, 0, 2, 0, 0, 0, 5, 0, 0, 0, 4})

	f.Fuzz(func(t *testing.T, b []byte) {
		ps, err := ParseRTCP(b)
		if err != nil {
			return
		}

		out, err := AppendRTCP(nil, ps...)
		if err != nil {
			t.Fatalf("% x parsed, AppendRTCP: %v", b, err)
		}

		again, err := ParseRTCP(out)
		if err != nil || !reflect.DeepEqual(again, ps) {
			t.Fatalf("% x: AppendRTCP % x parses to %+v, %v; want %+v", b, out, again, err, ps)
		}
	})
}
