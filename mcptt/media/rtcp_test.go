package media

import (
	"slices"
	"testing"

	"github.com/ellanetworks/ims/mcptt/rtp"
)

// RFC 3550 §6.4–§6.6, RFC 4585 §6.1: the offsets of the SSRC fields of a compound packet: of the senders, SR, SDES
// chunks, BYE; of the sources, the report blocks and the media source of feedback; none of APP.
func TestRTCPSSRCs(t *testing.T) {
	b, err := rtp.AppendRTCP(nil,
		&rtp.SenderReport{SSRC: 1, Reports: []rtp.ReceptionReport{{SSRC: 2}, {SSRC: 3}}},
		&rtp.SourceDescription{Chunks: []rtp.SDESChunk{
			{Source: 4, Items: []rtp.SDESItem{{Type: rtp.SDESCNAME, Text: "abc"}}},
			{Source: 5, Items: []rtp.SDESItem{{Type: rtp.SDESCNAME, Text: "abcdefgh"}}},
		}},
		&rtp.App{SSRC: 6, Name: [4]byte{'M', 'C', 'P', 'T'}},
		&rtp.RawRTCP{Type: 205, Count: 1, Body: []byte{0, 0, 0, 8, 0, 0, 0, 9}},
		&rtp.Goodbye{Sources: []uint32{10, 11}, Reason: "x"},
	)
	if err != nil {
		t.Fatal(err)
	}

	senders, sources, err := rtcpSSRCs(b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	at := func(offs []int) []uint32 {
		var out []uint32
		for _, o := range offs {
			out = append(out, uint32(b[o])<<24|uint32(b[o+1])<<16|uint32(b[o+2])<<8|uint32(b[o+3]))
		}

		return out
	}

	if s, r := at(senders), at(sources); !slices.Equal(s, []uint32{1, 4, 5, 8, 10, 11}) || !slices.Equal(r, []uint32{2, 3, 9}) {
		t.Errorf("senders %v, sources %v", s, r)
	}

	for _, bad := range [][]byte{nil, b[:len(b)-1], {0x80, 200, 0, 1, 0, 0, 0, 1}, {0x81, 201, 0, 1, 0, 0, 0, 1}, {0x40, 201, 0, 0}} {
		if _, _, err := rtcpSSRCs(bad, nil, nil); err == nil {
			t.Errorf("% x accepted", bad)
		}
	}
}

// rtcpSSRCs on any input: no panic, offsets of whole fields of the input.
func FuzzRTCPSSRCs(f *testing.F) {
	b, _ := rtp.AppendRTCP(nil, &rtp.ReceiverReport{SSRC: 1, Reports: []rtp.ReceptionReport{{SSRC: 2}}},
		&rtp.SourceDescription{Chunks: []rtp.SDESChunk{{Source: 4}}}, &rtp.Goodbye{Sources: []uint32{3}})
	f.Add(b)

	f.Fuzz(func(t *testing.T, b []byte) {
		senders, sources, err := rtcpSSRCs(b, nil, nil)
		if err != nil {
			return
		}

		for _, o := range append(senders, sources...) {
			if o < 0 || o+4 > len(b) {
				t.Fatalf("offset %d of %d octets", o, len(b))
			}
		}
	})
}
