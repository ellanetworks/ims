// Package floorctl is the coding of the floor control messages of MCPTT (TS 24.380 §8.2): RTCP APP packets of name
// "MCPT", each message a subtype and the fields of §8.2.3.
//
// The fields of on-network floor control with a controlling function are typed; those of the non-controlling
// function, functional aliases, locations, multi-talker and queued floor request cancellation are kept as Other, as
// any field of a later version (§8.1.4).
package floorctl

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ellanetworks/ims/mcptt/rtp"
)

// Name is the name of the floor control APP packets (§8.2.1).
var Name = [4]byte{'M', 'C', 'P', 'T'}

// Type is a floor control message, its subtype without the acknowledgement bit (Table 8.2.2.1-1). The messages of
// multi-talker (§8.2.14), queued floor request cancellation (§8.2.15), unicast media flow control (§8.2.16) and
// floor revoke request (§8.2.17) are out of scope: Decode takes them as unknown.
type Type uint8

const (
	Request              Type = 0b00000
	Granted              Type = 0b00001
	Taken                Type = 0b00010
	Deny                 Type = 0b00011
	Release              Type = 0b00100
	Idle                 Type = 0b00101
	Revoke               Type = 0b00110
	QueuePositionRequest Type = 0b01000
	QueuePositionInfo    Type = 0b01001
	Ack                  Type = 0b01010
)

// ackBit is the first bit of the subtype, set when the sender asks for a Floor Ack (§8.2.2.1).
const ackBit = 0b10000

// FieldID is the ID of a field (Table 8.2.3.1-2).
type FieldID uint8

const (
	FieldPriority              FieldID = 0
	FieldDuration              FieldID = 1
	FieldRejectCause           FieldID = 2
	FieldQueueInfo             FieldID = 3
	FieldGrantedPartyID        FieldID = 4
	FieldPermission            FieldID = 5
	FieldUserID                FieldID = 6
	FieldQueueSize             FieldID = 7
	FieldSequenceNumber        FieldID = 8
	FieldQueuedUserID          FieldID = 9
	FieldSource                FieldID = 10
	FieldTrackInfo             FieldID = 11
	FieldMessageType           FieldID = 12
	FieldIndicator             FieldID = 13
	FieldGrantedSSRC           FieldID = 14
	FieldGrantedUsers          FieldID = 15
	FieldSSRCs                 FieldID = 16
	FieldFunctionalAlias       FieldID = 17
	FieldFunctionalAliases     FieldID = 18
	FieldLocation              FieldID = 19
	FieldLocations             FieldID = 20
	FieldQueuedRequestsPurpose FieldID = 21
	FieldQueuedUsers           FieldID = 22
	FieldQueuedRequestsResult  FieldID = 23
	FieldMediaFlowControl      FieldID = 24
	FieldRevokeRequestUserID   FieldID = 25
)

const (
	// fieldIDs is the number of field IDs of the present version.
	fieldIDs = 26
	// longFieldID is the first field ID with a two-octet length (§8.1.3).
	longFieldID FieldID = 192
)

// Message is a floor control message. A nil or empty field is absent.
type Message struct {
	Type Type
	// AckRequired is the acknowledgement bit, of the messages that have one (§8.2.2.1).
	AckRequired bool
	// SSRC is the RTCP SSRC of the sender (§8.1.2).
	SSRC uint32

	// Priority is the Floor Priority (§8.2.3.2): requested, granted.
	Priority *uint8
	// Duration is the time in seconds the granted party may send (§8.2.3.3).
	Duration *uint16
	// RejectCause is the Reject Cause of a Floor Deny or Floor Revoke (§8.2.3.4).
	RejectCause *RejectCause
	// QueueInfo is the Queue Info (§8.2.3.5).
	QueueInfo *QueueInfo
	// GrantedPartyID is the Granted Party's Identity, an MCPTT ID (§8.2.3.6).
	GrantedPartyID string
	// Permission is the Permission to Request the Floor (§8.2.3.7).
	Permission *bool
	// UserID is an MCPTT ID (§8.2.3.8).
	UserID string
	// QueueSize is the Queue Size (§8.2.3.9).
	QueueSize *uint16
	// SequenceNumber is the Message Sequence Number (§8.2.3.10).
	SequenceNumber *uint16
	// QueuedUserID is the Queued User ID, an MCPTT ID (§8.2.3.11).
	QueuedUserID string
	// Source is the Source of a Floor Ack (§8.2.3.12).
	Source *Source
	// MessageType is the message a Floor Ack acknowledges (§8.2.3.14).
	MessageType *Type
	// Indicator is the Floor Indicator (§8.2.3.15).
	Indicator *Indicator
	// GrantedSSRC is the Audio SSRC of Granted Participant (§8.2.3.16).
	GrantedSSRC *uint32

	// Other are the fields not typed above, in order: of a later version, or of the features out of scope.
	Other []Field
}

// Field is a field as coded (§8.1.3): its ID and value, without padding.
type Field struct {
	ID    FieldID
	Value []byte
}

// RejectCause is the Reject Cause field (§8.2.3.4).
type RejectCause struct {
	Cause Cause
	// Phrase is text, as an SDES CNAME (RFC 3550 §6.5.1).
	Phrase string
}

// Cause is a <Reject Cause> value: of a Floor Deny (§8.2.6.2) or of a Floor Revoke (§8.2.10.2).
type Cause uint16

// Causes of a Floor Deny (§8.2.6.2).
const (
	DenyAnotherHasPermission Cause = 1
	DenyInternalError        Cause = 2
	DenyOnlyOneParticipant   Cause = 3
	DenyRetryAfter           Cause = 4
	DenyReceiveOnly          Cause = 5
	DenyNoResources          Cause = 6
	DenyQueueFull            Cause = 7
	DenyOther                Cause = 255
)

// Causes of a Floor Revoke (§8.2.10.2).
const (
	RevokeOnlyOneClient     Cause = 1
	RevokeMediaBurstTooLong Cause = 2
	RevokeNoPermission      Cause = 3
	RevokePreempted         Cause = 4
	RevokeNoResources       Cause = 6
	RevokeByAnotherClient   Cause = 7
	RevokeOther             Cause = 255
)

// QueueInfo is the Queue Info field (§8.2.3.5).
type QueueInfo struct {
	// Position is the position in the queue, QueueNotQueued or QueueUnknown.
	Position uint8
	Priority uint8
}

const (
	QueueNotQueued uint8 = 254
	QueueUnknown   uint8 = 255
)

// Source is the Source field (§8.2.3.12).
type Source uint16

const (
	SourceParticipant    Source = 0
	SourceParticipating  Source = 1
	SourceControlling    Source = 2
	SourceNonControlling Source = 3
)

// Indicator is the Floor Indicator bit map (§8.2.3.15), bit A the most significant.
type Indicator uint16

const (
	IndicatorNormal         Indicator = 1 << (15 - iota)
	IndicatorBroadcast                // B
	IndicatorSystem                   // C
	IndicatorEmergency                // D
	IndicatorImminentPeril            // E
	IndicatorQueueing                 // F
	IndicatorDualFloor                // G
	IndicatorTemporaryGroup           // H
	IndicatorMultiTalker              // I
)

// messages are the fields each message carries (Tables 8.2.4-1 to 8.2.17-1), and whether it has the acknowledgement
// bit (Table 8.2.2.1-1).
var messages = map[Type]struct {
	ack    bool
	fields []FieldID
}{
	Request: {false, []FieldID{FieldPriority, FieldUserID, FieldTrackInfo, FieldIndicator, FieldFunctionalAlias, FieldLocation}},
	Granted: {true, []FieldID{
		FieldDuration, FieldGrantedSSRC, FieldPriority, FieldUserID, FieldQueueSize, FieldQueuedUserID, FieldQueueInfo,
		FieldTrackInfo, FieldIndicator,
	}},
	Deny:    {true, []FieldID{FieldRejectCause, FieldUserID, FieldTrackInfo, FieldIndicator}},
	Release: {true, []FieldID{FieldUserID, FieldTrackInfo, FieldIndicator}},
	Idle:    {true, []FieldID{FieldSequenceNumber, FieldTrackInfo, FieldIndicator}},
	Taken: {true, []FieldID{
		FieldGrantedPartyID, FieldPermission, FieldUserID, FieldSequenceNumber, FieldTrackInfo, FieldIndicator,
		FieldGrantedSSRC, FieldFunctionalAlias, FieldGrantedUsers, FieldSSRCs, FieldFunctionalAliases, FieldLocation,
		FieldLocations,
	}},
	Revoke:               {false, []FieldID{FieldRejectCause, FieldTrackInfo, FieldIndicator}},
	QueuePositionRequest: {false, []FieldID{FieldUserID, FieldTrackInfo}},
	QueuePositionInfo: {true, []FieldID{
		FieldUserID, FieldGrantedSSRC, FieldQueuedUserID, FieldQueueInfo, FieldTrackInfo, FieldIndicator,
	}},
	Ack: {false, []FieldID{FieldSource, FieldMessageType, FieldTrackInfo, FieldLocation}},
}

// ErrUnknown is the error of a message whose subtype is unknown, to be ignored whole (§8.1.4).
var ErrUnknown = errors.New("floorctl: unknown message")

// ErrField is wrapped by the errors of the fields Decode leaves out, malformed (§8.1.4): the message it returns is
// usable without them.
var ErrField = errors.New("floorctl: malformed field")

// Decode decodes the floor control message app. A malformed field is left out, with an error wrapping ErrField; of a
// typed field given twice, the first well-formed counts. Fields the message does not carry and the repeated ones are kept as
// Other, to be ignored (§8.1.4).
func Decode(app *rtp.App) (Message, error) {
	if app.Name != Name {
		return Message{}, fmt.Errorf("floorctl: APP name %q", app.Name[:])
	}

	t := Type(app.Subtype &^ ackBit)

	spec, ok := messages[t]
	if !ok || app.Subtype&ackBit != 0 && !spec.ack {
		return Message{}, fmt.Errorf("%w: subtype %05b", ErrUnknown, app.Subtype)
	}

	m := Message{Type: t, AckRequired: app.Subtype&ackBit != 0, SSRC: app.SSRC}

	var (
		errs []error
		seen [fieldIDs]bool
	)

	for b := app.Data; len(b) > 0; {
		f, n, err := nextField(b)
		if err != nil {
			return Message{}, err
		}

		b = b[n:]

		if f.ID >= fieldIDs || !typed(f.ID) || seen[f.ID] || !carries(spec.fields, f.ID) {
			m.Other = append(m.Other, f)
			continue
		}

		if err := m.set(f); err != nil {
			errs = append(errs, fmt.Errorf("%w %d: %w", ErrField, f.ID, err))
			continue
		}

		seen[f.ID] = true
	}

	return m, errors.Join(errs...)
}

// nextField reads the field at the start of b, and the octets it takes with its padding (§8.1.3).
func nextField(b []byte) (Field, int, error) {
	id := FieldID(b[0])

	hdr := 2
	if id >= longFieldID {
		hdr = 3
	}

	if len(b) < hdr {
		return Field{}, 0, errors.New("floorctl: truncated field")
	}

	n := int(b[1])
	if hdr == 3 {
		n = int(binary.BigEndian.Uint16(b[1:]))
	}

	end := hdr + n
	if end > len(b) {
		return Field{}, 0, fmt.Errorf("floorctl: field %d of %d octets, %d left", id, n, len(b)-hdr)
	}

	return Field{ID: id, Value: b[hdr:end:end]}, min((end+3)&^3, len(b)), nil
}

func typed(id FieldID) bool {
	switch id {
	case FieldTrackInfo, FieldGrantedUsers, FieldSSRCs, FieldFunctionalAlias, FieldFunctionalAliases, FieldLocation,
		FieldLocations, FieldQueuedRequestsPurpose, FieldQueuedUsers, FieldQueuedRequestsResult, FieldMediaFlowControl,
		FieldRevokeRequestUserID:
		return false
	}

	return true
}

// set decodes the typed field f into m.
func (m *Message) set(f Field) error {
	v := f.Value

	switch f.ID {
	case FieldRejectCause:
		if len(v) < 2 {
			return fmt.Errorf("length %d", len(v))
		}

		m.RejectCause = &RejectCause{Cause: Cause(binary.BigEndian.Uint16(v)), Phrase: string(v[2:])}

		return nil
	case FieldGrantedPartyID, FieldUserID, FieldQueuedUserID:
		if len(v) == 0 {
			return errors.New("empty MCPTT ID")
		}

		*m.id(f.ID) = string(v)

		return nil
	case FieldGrantedSSRC:
		// The value and 16 spare bits (§8.2.3.16).
		if len(v) != 6 {
			return fmt.Errorf("length %d", len(v))
		}

		ssrc := binary.BigEndian.Uint32(v)
		m.GrantedSSRC = &ssrc

		return nil
	}

	if len(v) != 2 {
		return fmt.Errorf("length %d", len(v))
	}

	u := binary.BigEndian.Uint16(v)

	switch f.ID {
	case FieldPriority:
		p := v[0]
		m.Priority = &p
	case FieldDuration:
		m.Duration = &u
	case FieldQueueInfo:
		m.QueueInfo = &QueueInfo{Position: v[0], Priority: v[1]}
	case FieldPermission:
		if u > 1 {
			return fmt.Errorf("permission %d", u)
		}

		p := u == 1
		m.Permission = &p
	case FieldQueueSize:
		m.QueueSize = &u
	case FieldSequenceNumber:
		m.SequenceNumber = &u
	case FieldSource:
		s := Source(u)
		m.Source = &s
	case FieldMessageType:
		// The subtype of the message acknowledged, its first bit zero, preceded by three zero bits (§8.2.3.14).
		if v[0] > 0b01111 {
			return fmt.Errorf("message type %08b", v[0])
		}

		t := Type(v[0])
		m.MessageType = &t
	case FieldIndicator:
		i := Indicator(u)
		m.Indicator = &i
	}

	return nil
}

func (m *Message) id(id FieldID) *string {
	switch id {
	case FieldGrantedPartyID:
		return &m.GrantedPartyID
	case FieldUserID:
		return &m.UserID
	}

	return &m.QueuedUserID
}

// App is the APP packet of m: its typed fields in the order of the field IDs, then Other. A field the message does
// not carry, or the acknowledgement bit on a message without one, is an error.
func (m *Message) App() (*rtp.App, error) {
	spec, ok := messages[m.Type]
	if !ok {
		return nil, fmt.Errorf("%w: type %d", ErrUnknown, m.Type)
	}

	if m.AckRequired && !spec.ack {
		return nil, fmt.Errorf("floorctl: acknowledgement asked of message %d", m.Type)
	}

	app := &rtp.App{Subtype: uint8(m.Type), SSRC: m.SSRC, Name: Name}
	if m.AckRequired {
		app.Subtype |= ackBit
	}

	var err error

	for _, f := range m.fields() {
		if !carries(spec.fields, f.ID) {
			return nil, fmt.Errorf("floorctl: field %d in message %d", f.ID, m.Type)
		}

		if app.Data, err = appendField(app.Data, f); err != nil {
			return nil, err
		}
	}

	for _, f := range m.Other {
		if app.Data, err = appendField(app.Data, f); err != nil {
			return nil, err
		}
	}

	return app, nil
}

func carries(fields []FieldID, id FieldID) bool {
	for _, f := range fields {
		if f == id {
			return true
		}
	}

	return false
}

// fields are the typed fields of m that are present, coded.
func (m *Message) fields() []Field {
	var fs []Field

	u16 := func(id FieldID, v uint16) { fs = append(fs, Field{id, binary.BigEndian.AppendUint16(nil, v)}) }
	str := func(id FieldID, s string) {
		if s != "" {
			fs = append(fs, Field{id, []byte(s)})
		}
	}

	if m.Priority != nil {
		fs = append(fs, Field{FieldPriority, []byte{*m.Priority, 0}})
	}

	if m.Duration != nil {
		u16(FieldDuration, *m.Duration)
	}

	if r := m.RejectCause; r != nil {
		fs = append(fs, Field{FieldRejectCause, append(binary.BigEndian.AppendUint16(nil, uint16(r.Cause)), r.Phrase...)})
	}

	if q := m.QueueInfo; q != nil {
		fs = append(fs, Field{FieldQueueInfo, []byte{q.Position, q.Priority}})
	}

	str(FieldGrantedPartyID, m.GrantedPartyID)

	if m.Permission != nil {
		var p uint16
		if *m.Permission {
			p = 1
		}

		u16(FieldPermission, p)
	}

	str(FieldUserID, m.UserID)

	if m.QueueSize != nil {
		u16(FieldQueueSize, *m.QueueSize)
	}

	if m.SequenceNumber != nil {
		u16(FieldSequenceNumber, *m.SequenceNumber)
	}

	str(FieldQueuedUserID, m.QueuedUserID)

	if m.Source != nil {
		u16(FieldSource, uint16(*m.Source))
	}

	if m.MessageType != nil {
		fs = append(fs, Field{FieldMessageType, []byte{uint8(*m.MessageType), 0}})
	}

	if m.Indicator != nil {
		u16(FieldIndicator, uint16(*m.Indicator))
	}

	if m.GrantedSSRC != nil {
		fs = append(fs, Field{FieldGrantedSSRC, append(binary.BigEndian.AppendUint32(nil, *m.GrantedSSRC), 0, 0)})
	}

	return fs
}

// appendField appends f with its padding (§8.1.3).
func appendField(dst []byte, f Field) ([]byte, error) {
	start := len(dst)

	if f.ID >= longFieldID {
		if len(f.Value) > 0xffff {
			return dst, fmt.Errorf("floorctl: field %d of %d octets", f.ID, len(f.Value))
		}

		dst = binary.BigEndian.AppendUint16(append(dst, byte(f.ID)), uint16(len(f.Value)))
	} else {
		if len(f.Value) > 0xff {
			return dst, fmt.Errorf("floorctl: field %d of %d octets", f.ID, len(f.Value))
		}

		dst = append(dst, byte(f.ID), byte(len(f.Value)))
	}

	dst = append(dst, f.Value...)

	for (len(dst)-start)%4 != 0 {
		dst = append(dst, 0)
	}

	return dst, nil
}
