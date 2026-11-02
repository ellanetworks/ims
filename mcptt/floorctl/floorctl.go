package floorctl

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ellanetworks/ims/mcptt/rtp"
)

// TS 24.380 clause 8.2.1
var Name = [4]byte{'M', 'C', 'P', 'T'}

// TS 24.380 clause 8.2.2.1
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

// TS 24.380 clause 8.2.2.1
const ackBit = 0b10000

// TS 24.380 clause 8.2.3.1
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
	fieldIDs = 26
	// TS 24.380 clause 8.1.3
	longFieldID FieldID = 192
)

type Message struct {
	Type        Type
	AckRequired bool
	SSRC        uint32

	Priority       *uint8
	Duration       *uint16
	RejectCause    *RejectCause
	QueueInfo      *QueueInfo
	GrantedPartyID string
	Permission     *bool
	UserID         string
	QueueSize      *uint16
	SequenceNumber *uint16
	QueuedUserID   string
	Source         *Source
	MessageType    *Type
	Indicator      *Indicator
	GrantedSSRC    *uint32

	Other []Field
}

// TS 24.380 clause 8.1.3
type Field struct {
	ID    FieldID
	Value []byte
}

// TS 24.380 clause 8.2.3.4
type RejectCause struct {
	Cause  Cause
	Phrase string
}

// TS 24.380 clause 8.2.6.2
// TS 24.380 clause 8.2.10.2
type Cause uint16

// TS 24.380 clause 8.2.6.2
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

// TS 24.380 clause 8.2.10.2
const (
	RevokeOnlyOneClient     Cause = 1
	RevokeMediaBurstTooLong Cause = 2
	RevokeNoPermission      Cause = 3
	RevokePreempted         Cause = 4
	RevokeNoResources       Cause = 6
	RevokeByAnotherClient   Cause = 7
	RevokeOther             Cause = 255
)

// TS 24.380 clause 8.2.3.5
type QueueInfo struct {
	Position uint8
	Priority uint8
}

const (
	QueueNotQueued uint8 = 254
	QueueUnknown   uint8 = 255
)

// TS 24.380 clause 8.2.3.12
type Source uint16

const (
	SourceParticipant    Source = 0
	SourceParticipating  Source = 1
	SourceControlling    Source = 2
	SourceNonControlling Source = 3
)

// TS 24.380 clause 8.2.3.15
type Indicator uint16

const (
	IndicatorNormal Indicator = 1 << (15 - iota)
	IndicatorBroadcast
	IndicatorSystem
	IndicatorEmergency
	IndicatorImminentPeril
	IndicatorQueueing
	IndicatorDualFloor
	IndicatorTemporaryGroup
	IndicatorMultiTalker
)

// TS 24.380 clause 8.2.2.1
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

// TS 24.380 clause 8.1.4
var ErrUnknown = errors.New("floorctl: unknown message")

// TS 24.380 clause 8.1.4
var ErrField = errors.New("floorctl: malformed field")

// TS 24.380 clause 8.1.4
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

// TS 24.380 clause 8.1.3
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

// TS 24.380 clause 8.1.3
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
