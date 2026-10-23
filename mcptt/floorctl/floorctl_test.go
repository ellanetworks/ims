package floorctl

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/ellanetworks/ims/mcptt/rtp"
)

func ptr[T any](v T) *T { return &v }

// Each message of §8.2.4–§8.2.13 in scope, from its table, and its fields from §8.2.3: the data as coded, each field
// padded to 32 bits (§8.1.3).
func TestMessages(t *testing.T) {
	for _, c := range []struct {
		name string
		m    Message
		sub  uint8
		data []byte
	}{
		{
			"Floor Request (Table 8.2.4-1)",
			Message{Type: Request, SSRC: 1, Priority: ptr[uint8](5), Indicator: ptr(IndicatorNormal | IndicatorQueueing)},
			0b00000,
			[]byte{0, 2, 5, 0, 13, 2, 0x84, 0x00},
		},
		{
			"Floor Granted (Table 8.2.5-1)",
			Message{
				Type: Granted, AckRequired: true, SSRC: 2, Priority: ptr[uint8](5), Duration: ptr[uint16](30),
				GrantedSSRC: ptr[uint32](0x01020304),
			},
			0b10001,
			[]byte{0, 2, 5, 0, 1, 2, 0, 30, 14, 6, 1, 2, 3, 4, 0, 0},
		},
		{
			"Floor Deny (Table 8.2.6.1-1)",
			Message{Type: Deny, SSRC: 2, RejectCause: &RejectCause{Cause: DenyAnotherHasPermission, Phrase: "busy"}},
			0b00011,
			[]byte{2, 6, 0, 1, 'b', 'u', 's', 'y'},
		},
		{
			"Floor Release (Table 8.2.7-1)",
			Message{Type: Release, AckRequired: true, SSRC: 1},
			0b10100,
			nil,
		},
		{
			"Floor Idle (Table 8.2.8-1)",
			Message{Type: Idle, SSRC: 2, SequenceNumber: ptr[uint16](65535)},
			0b00101,
			[]byte{8, 2, 0xff, 0xff},
		},
		{
			"Floor Taken (Table 8.2.9-1)",
			Message{
				Type: Taken, SSRC: 2, GrantedPartyID: "sip:alice@x", Permission: ptr(true),
				SequenceNumber: ptr[uint16](7), GrantedSSRC: ptr[uint32](9),
			},
			0b00010,
			[]byte{
				4, 11, 's', 'i', 'p', ':', 'a', 'l', 'i', 'c', 'e', '@', 'x', 0, 0, 0, // padded to 2 + 4n (§8.2.3.6)
				5, 2, 0, 1, 8, 2, 0, 7, 14, 6, 0, 0, 0, 9, 0, 0,
			},
		},
		{
			"Floor Revoke (Table 8.2.10.1-1)",
			Message{Type: Revoke, SSRC: 2, RejectCause: &RejectCause{Cause: RevokeMediaBurstTooLong}},
			0b00110,
			[]byte{2, 2, 0, 2},
		},
		{
			"Floor Queue Position Request (Table 8.2.11-1)",
			Message{Type: QueuePositionRequest, SSRC: 1},
			0b01000,
			nil,
		},
		{
			"Floor Queue Position Info (Table 8.2.12-1)",
			Message{Type: QueuePositionInfo, SSRC: 2, QueueInfo: &QueueInfo{Position: 3, Priority: 1}},
			0b01001,
			[]byte{3, 2, 3, 1},
		},
		{
			"Floor Ack (Table 8.2.13-1)",
			Message{Type: Ack, SSRC: 1, Source: ptr(SourceParticipant), MessageType: ptr(Granted)},
			0b01010,
			[]byte{10, 2, 0, 0, 12, 2, 1, 0},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, err := c.m.App()
			if err != nil {
				t.Fatal(err)
			}

			if app.Subtype != c.sub || app.Name != Name || app.SSRC != c.m.SSRC || !bytes.Equal(app.Data, c.data) {
				t.Fatalf("App subtype %05b, data % x", app.Subtype, app.Data)
			}

			m, err := Decode(app)
			if err != nil || !reflect.DeepEqual(m, c.m) {
				t.Errorf("Decode %+v, %v", m, err)
			}
		})
	}
}

// §8.1.1: more than one message in a datagram, each an APP packet outside compound packets.
func TestDatagram(t *testing.T) {
	var b []byte

	for _, m := range []Message{
		{Type: Taken, SSRC: 2, GrantedPartyID: "sip:a@x"},
		{Type: Idle, SSRC: 2, SequenceNumber: ptr[uint16](1)},
	} {
		app, err := m.App()
		if err != nil {
			t.Fatal(err)
		}

		if b, err = rtp.AppendRTCP(b, app); err != nil {
			t.Fatal(err)
		}
	}

	ps, err := rtp.ParseRTCP(b)
	if err != nil || len(ps) != 2 {
		t.Fatalf("%d packets, %v", len(ps), err)
	}

	if m, err := Decode(ps[1].(*rtp.App)); err != nil || m.Type != Idle || *m.SequenceNumber != 1 {
		t.Errorf("%+v, %v", m, err)
	}
}

// §8.1.4: a message of an unknown subtype is ignored whole; unspecified fields, of a later version or that the
// message does not carry, are ignored and kept; a syntactically incorrect field is ignored.
func TestUnknown(t *testing.T) {
	for _, sub := range []uint8{0b01011, 0b01111, 0b10000, 0b10110, 0b11010} {
		if _, err := Decode(&rtp.App{Subtype: sub, Name: Name}); !errors.Is(err, ErrUnknown) {
			t.Errorf("subtype %05b: %v", sub, err)
		}
	}

	if _, err := Decode(&rtp.App{Name: [4]byte{'M', 'C', 'P', 'C'}}); err == nil {
		t.Error("name MCPC decoded")
	}

	app := &rtp.App{Subtype: uint8(Idle), Name: Name, Data: []byte{
		30, 1, 0xaa, 0, // a later field
		200, 0, 3, 1, 2, 3, 0, 0, // a field ID of a two-octet length (§8.1.3)
		0, 2, 9, 0, // Floor Priority, which a Floor Idle does not carry
		8, 3, 0, 1, 2, 0, 0, 0, // a Message Sequence Number of a wrong length
		13, 2, 0x80, 0, // the Floor Indicator
		13, 2, 0x40, 0, // repeated
	}}

	m, err := Decode(app)
	if !errors.Is(err, ErrField) {
		t.Fatalf("%v", err)
	}

	want := Message{
		Type: Idle, Indicator: ptr(IndicatorNormal),
		Other: []Field{{30, []byte{0xaa}}, {200, []byte{1, 2, 3}}, {0, []byte{9, 0}}, {13, []byte{0x40, 0}}},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("Decode %+v", m)
	}

	out, err := m.App()
	if err != nil {
		t.Fatal(err)
	}

	if again, err := Decode(out); err != nil || !reflect.DeepEqual(again, m) {
		t.Errorf("Decode(App) %+v, %v", again, err)
	}

	for _, data := range [][]byte{{8}, {8, 4, 0, 0}, {200, 0}} {
		if m, err := Decode(&rtp.App{Subtype: uint8(Idle), Name: Name, Data: data}); err == nil || errors.Is(err, ErrField) {
			t.Errorf("% x: %+v, %v", data, m, err)
		}
	}
}

// §8.2.3: values outside their coding.
func TestMalformedFields(t *testing.T) {
	for _, c := range []struct {
		t    Type
		data []byte
	}{
		{Taken, []byte{5, 2, 0, 2}},              // Permission to Request the Floor 2
		{Taken, []byte{4, 0, 0, 0}},              // empty Granted Party's Identity
		{Taken, []byte{14, 4, 0, 0, 0, 9, 0, 0}}, // Audio SSRC of Granted Participant without spare
		{Deny, []byte{2, 1, 0, 0}},               // Reject Cause without a cause
	} {
		m, err := Decode(&rtp.App{Subtype: uint8(c.t), Name: Name, Data: c.data})
		if !errors.Is(err, ErrField) || !reflect.DeepEqual(m, Message{Type: c.t}) {
			t.Errorf("% x: %+v, %v", c.data, m, err)
		}
	}

	m, err := Decode(&rtp.App{Subtype: uint8(Ack), Name: Name, Data: []byte{12, 2, 0b10001, 0}})
	if !errors.Is(err, ErrField) || m.MessageType != nil {
		t.Errorf("Message Type with the acknowledgement bit: %+v, %v", m, err)
	}
}

func TestAppErrors(t *testing.T) {
	for _, m := range []Message{
		{Type: Type(0b01011)},
		{Type: Revoke, AckRequired: true},
		{Type: Idle, Duration: ptr[uint16](1)},
		{Type: Taken, GrantedPartyID: string(make([]byte, 256))},
		{Type: Idle, Other: []Field{{200, make([]byte, 1<<16)}}},
	} {
		if app, err := m.App(); err == nil {
			t.Errorf("App(%+v) = %+v", m, app)
		}
	}
}

func FuzzFloor(f *testing.F) {
	f.Add(uint8(0b10001), []byte{0, 2, 5, 0, 1, 2, 0, 30, 14, 6, 1, 2, 3, 4, 0, 0})
	f.Add(uint8(0b00010), []byte{4, 11, 's', 'i', 'p', ':', 'a', 'l', 'i', 'c', 'e', '@', 'x', 0, 0, 0, 5, 2, 0, 1})
	f.Add(uint8(0b00101), []byte{30, 1, 0xaa, 0, 200, 0, 3, 1, 2, 3, 13, 2, 0x80, 0})
	f.Add(uint8(0b01010), []byte{10, 2, 0, 0, 12, 2, 1, 0})

	f.Fuzz(func(t *testing.T, sub uint8, data []byte) {
		if len(data)%4 != 0 {
			return
		}

		m, err := Decode(&rtp.App{Subtype: sub & 0x1f, Name: Name, Data: data})
		if err != nil && !errors.Is(err, ErrField) {
			return
		}

		app, err := m.App()
		if err != nil {
			t.Fatalf("%05b % x decoded to %+v, App: %v", sub, data, m, err)
		}

		again, err := Decode(app)
		if err != nil || !reflect.DeepEqual(again, m) {
			t.Fatalf("%05b % x: App % x decodes to %+v, %v; want %+v", sub, data, app.Data, again, err, m)
		}
	})
}
