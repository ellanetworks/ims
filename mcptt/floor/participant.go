package floor

import (
	"errors"
	"fmt"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// ParticipantTimers are the timers and counters of the on-network floor participant (§11.1.1, §11.2.1). The
// timers are those of the client's configuration (TS 24.483 §8.2.11–§8.2.13, of none by default): T100 and T101
// are required, T103, optional, unused when zero. A zero counter is its default of Table 11.2.1-1.
type ParticipantTimers struct {
	// T100 (Floor Release), T101 (Floor Request), T103 (End of RTP media).
	T100, T101, T103 time.Duration
	// C100 (Floor Release), C101 (Floor Request): the number of times a message is sent.
	C100, C101 int
}

func (t ParticipantTimers) withDefaults() (ParticipantTimers, error) {
	if t.T100 <= 0 || t.T101 <= 0 {
		return t, errors.New("floor: T100 (Floor Release) or T101 (Floor Request) not configured")
	}

	if t.C100 <= 0 {
		t.C100 = 3
	}

	if t.C101 <= 0 {
		t.C101 = 3
	}

	// Table 11.1.1-1 NOTE 1: the Floor Release messages "shall" be sent within 6 seconds; NOTE 2 says "should" of
	// the Floor Requests.
	if time.Duration(t.C100)*t.T100 >= maxRetransmission {
		return t, fmt.Errorf("floor: %d Floor Release messages every %s not within %s", t.C100, t.T100, maxRetransmission)
	}

	return t, nil
}

// Notice is what the floor participant tells its user (§6.2.4).
type Notice struct {
	Kind NoticeKind
	// Cause is the <Reject Cause> of a Floor Deny or Floor Revoke.
	Cause floorctl.Cause
	// Party is the Granted Party's Identity of a Floor Taken.
	Party string
	// Indicator is the Floor Indicator of the message, the type of the call.
	Indicator floorctl.Indicator
	// Duration is the Duration of a Floor Granted, the time the talker may transmit (§8.2.3.3); zero without one.
	Duration time.Duration
}

// NoticeKind is a notice of the floor participant.
type NoticeKind int

const (
	// NoticeGranted: the user may talk (§6.2.4.4.2).
	NoticeGranted NoticeKind = iota + 1
	// NoticeDenied: the request is denied (§6.2.4.4.4).
	NoticeDenied
	// NoticeTaken: another user talks (§6.2.4.3.3).
	NoticeTaken
	// NoticeIdle: nobody talks (§6.2.4.3.2).
	NoticeIdle
	// NoticeRevoked: the permission to talk is revoked (§6.2.4.5.4).
	NoticeRevoked
	// NoticeRequestTimeout: the server did not answer the request (§6.2.4.4.6).
	NoticeRequestTimeout
	// NoticeMediaEnded: the media heard ended, by T103 (§6.2.4.3.6).
	NoticeMediaEnded
)

// ParticipantOut is what the floor participant does outside its machine.
type ParticipantOut interface {
	// Send sends msg to the floor control server.
	Send(msg floorctl.Message)
	// Transmit has the client send the user's voice, with ssrc, the Audio SSRC of Granted Participant
	// (§6.2.4.4.2 7), or no longer, discarding what is buffered (§6.2.4.5.4).
	Transmit(on bool, ssrc uint32)
	// Notify tells the user.
	Notify(n Notice)
}

// ParticipantConfig is the configuration of a floor participant.
type ParticipantConfig struct {
	Clock  Clock
	Out    ParticipantOut
	Timers ParticipantTimers
	// SSRC is the RTCP SSRC of the participant's messages: the one the server assigned it in mc_floor_ssrc, or its
	// own (§8.1.2).
	SSRC uint32
	// Priority is the floor priority the participant requests, no higher than negotiated (§14.3.3); 0, the
	// default.
	Priority uint8
	// Indicator are the Floor Indicator bits of the call's type, for a broadcast, system, emergency or imminent peril
	// call (§6.2.4.3.5 1b).
	Indicator floorctl.Indicator
}

type participantState int

const (
	pStartStop participantState = iota
	pNoPermission
	pPendingRequest
	pHasPermission
	pPendingRelease
	pReleasing
)

// Participant is the floor participant of a client in a call (§6.2.4).
type Participant struct {
	cfg   ParticipantConfig
	state participantState

	// early are the messages received before the 200 of an originating call (§6.2.4.2.2 2).
	early []floorctl.Message
	// ssrc is the Audio SSRC of Granted Participant last received.
	ssrc uint32

	t100, t101, t103 timer
	c100, c101       int
}

// maxRetransmission bounds the time a floor participant sends a message again (Table 11.1.1-1 NOTE 1).
const maxRetransmission = 6 * time.Second

// NewParticipant is the floor participant of a call, in 'Start-stop' (§6.2.4.2.1).
func NewParticipant(cfg ParticipantConfig) (*Participant, error) {
	t, err := cfg.Timers.withDefaults()
	if err != nil {
		return nil, err
	}

	cfg.Timers = t

	return &Participant{cfg: cfg}, nil
}

// Origination is the 200 of a call the user originated (§6.2.4.2.2).
type Origination struct {
	// Implicit is the INVITE's implicit floor request (§14.2.5).
	Implicit bool
	// Granted is the 200's mc_granted (§14.3.4), a floor granted indication (§6.2.4.4.2), and AudioSSRC its
	// mc_ssrc, the SSRC the participant's media then has (§14.3.6, §14.4).
	Granted   bool
	AudioSSRC uint32
}

// Originate is the 200 of the call the user originated (§6.2.4.2.2): the floor messages received before it taken
// after it, before its floor granted indication.
func (p *Participant) Originate(o Origination) {
	if p.state != pStartStop {
		return
	}

	if !o.Implicit {
		p.Join()
		return
	}

	early := p.early
	p.early = nil
	p.state = pPendingRequest
	p.c101 = 1
	p.t101.start(p.cfg.Clock, p.cfg.Timers.T101, p.expireT101)

	for _, msg := range early {
		p.receive(msg)
	}

	if o.Granted {
		p.receive(floorctl.Message{Type: floorctl.Granted, GrantedSSRC: &o.AudioSSRC})
	}
}

// Join is the 200 of a call the user was invited to (§6.2.4.2.3), or rejoined, or originated without an implicit
// floor request (§6.2.4.2.2): the floor messages received before it taken after it.
func (p *Participant) Join() {
	if p.state != pStartStop {
		return
	}

	early := p.early
	p.early = nil
	p.state = pNoPermission

	for _, msg := range early {
		p.receive(msg)
	}
}

// Press is the user's request to talk, the PTT button pressed (§6.2.4.3.5).
func (p *Participant) Press() {
	if p.state != pNoPermission {
		return
	}

	p.send(p.request())
	p.c101 = 1
	p.t101.start(p.cfg.Clock, p.cfg.Timers.T101, p.expireT101)
	p.state = pPendingRequest
}

// Unpress is the user's end of talk, the PTT button released (§6.2.4.4.8, §6.2.4.5.3).
func (p *Participant) Unpress() {
	switch p.state {
	case pPendingRequest:
		p.t101.stop()
	case pHasPermission:
		p.cfg.Out.Transmit(false, p.ssrc)
	default:
		return
	}

	p.release(floorctl.Message{Type: floorctl.Release})
}

// release sends the Floor Release msg, and waits for its answer.
func (p *Participant) release(msg floorctl.Message) {
	p.send(msg)
	p.c100 = 1
	p.t100.start(p.cfg.Clock, p.cfg.Timers.T100, p.expireT100)
	p.state = pPendingRelease
}

// Media is RTP media received from the server (§6.2.4.3.4, §6.2.4.4.7, §6.2.4.6.6): rendered, and T103 restarted.
// The media layer may report it at a coarser rate than per packet, well below T103.
func (p *Participant) Media() {
	switch p.state {
	case pNoPermission, pPendingRequest, pPendingRelease:
		p.startT103()
	}
}

// Receive is a floor control message from the server.
func (p *Participant) Receive(msg floorctl.Message) {
	switch p.state {
	case pStartStop:
		p.early = append(p.early, msg)
	case pReleasing:
	default:
		p.receive(msg)
	}
}

func (p *Participant) receive(msg floorctl.Message) {
	switch msg.Type {
	case floorctl.Granted:
		p.receiveGranted(msg)
	case floorctl.Deny:
		if p.state != pPendingRequest {
			return
		}

		// §6.2.4.4.4.
		p.ack(msg)
		p.t101.stop()
		p.state = pNoPermission
		p.notify(NoticeDenied, msg)
	case floorctl.Taken:
		p.receiveTaken(msg)
	case floorctl.Idle:
		switch p.state {
		case pNoPermission:
			// §6.2.4.3.2.
			p.ack(msg)
			p.t103.stop()
			p.notify(NoticeIdle, msg)
		case pPendingRelease:
			// §6.2.4.6.4.
			p.ack(msg)
			p.t100.stop()
			p.state = pNoPermission
			p.notify(NoticeIdle, msg)
		}
		// 'U: has permission': only of dual floor (§6.2.4.5.7).
	case floorctl.Revoke:
		switch p.state {
		case pHasPermission:
			// §6.2.4.5.4.
			p.cfg.Out.Transmit(false, p.ssrc)
			p.notify(NoticeRevoked, msg)
			// §6.2.4.5.4 5a i: with the Floor Indicator, its G-bit 0, without dual floor.
			p.release(floorctl.Message{Type: floorctl.Release, Indicator: ptr(p.cfg.Indicator &^ floorctl.IndicatorDualFloor)})
		case pPendingRelease:
			// §6.2.4.6.7.
			p.notify(NoticeRevoked, msg)
		}
	}
}

func (p *Participant) receiveGranted(msg floorctl.Message) {
	switch p.state {
	case pPendingRequest:
		// §6.2.4.4.2.
		p.ack(msg)
		p.t103.stop()
		p.t101.stop()

		if msg.GrantedSSRC != nil {
			p.ssrc = *msg.GrantedSSRC
		}

		p.state = pHasPermission
		p.notify(NoticeGranted, msg)
		p.cfg.Out.Transmit(true, p.ssrc)
	case pHasPermission:
		// §6.2.4.5.5: the Audio SSRC it gives is the one to use (§6.2.4.4.2 7).
		p.ack(msg)

		if msg.GrantedSSRC != nil && *msg.GrantedSSRC != p.ssrc {
			p.ssrc = *msg.GrantedSSRC
			p.cfg.Out.Transmit(true, p.ssrc)
		}

		// The Floor Granted of a floor granted in the 200 (mc_granted, §6.3.4.4.2 1a): its Duration told.
		if msg.Duration != nil {
			p.notify(NoticeGranted, msg)
		}
	case pPendingRelease:
		// §6.2.4.6.8.
		p.ack(msg)
	}
}

func (p *Participant) receiveTaken(msg floorctl.Message) {
	switch p.state {
	case pNoPermission, pPendingRequest, pPendingRelease:
		// §6.2.4.3.3, §6.2.4.4.11 (no pre-emptive request), §6.2.4.6.5.
		p.ack(msg)

		if msg.GrantedSSRC != nil {
			p.ssrc = *msg.GrantedSSRC
		}

		p.t101.stop()
		p.t100.stop()
		p.startT103()
		p.state = pNoPermission
		p.notify(NoticeTaken, msg)
	}
	// 'U: has permission': only of dual floor and multi-talker (§6.2.4.5.8).
}

// Release is the release of the call, both steps (§6.2.2, §6.2.4.7.2, §6.2.4.8.2): no more messages or media.
func (p *Participant) Release() {
	if p.state == pHasPermission {
		p.cfg.Out.Transmit(false, p.ssrc)
	}

	p.state = pReleasing
	p.early = nil

	for _, t := range []*timer{&p.t100, &p.t101, &p.t103} {
		t.stop()
	}
}

func (p *Participant) expireT100() {
	p.t100.fired()

	if p.state != pPendingRelease {
		return
	}

	// §6.2.4.6.2, §6.2.4.6.3.
	if p.c100 >= p.cfg.Timers.C100 {
		p.state = pNoPermission
		return
	}

	p.c100++
	p.send(floorctl.Message{Type: floorctl.Release})
	p.t100.start(p.cfg.Clock, p.cfg.Timers.T100, p.expireT100)
}

func (p *Participant) expireT101() {
	p.t101.fired()

	if p.state != pPendingRequest {
		return
	}

	// §6.2.4.4.5, §6.2.4.4.6.
	if p.c101 >= p.cfg.Timers.C101 {
		p.state = pNoPermission
		p.cfg.Out.Notify(Notice{Kind: NoticeRequestTimeout})

		return
	}

	p.c101++
	p.send(p.request())
	p.t101.start(p.cfg.Clock, p.cfg.Timers.T101, p.expireT101)
}

// startT103 (re)starts the optional T103, if configured.
func (p *Participant) startT103() {
	if p.cfg.Timers.T103 > 0 {
		p.t103.start(p.cfg.Clock, p.cfg.Timers.T103, p.expireT103)
	}
}

func (p *Participant) expireT103() {
	p.t103.fired()

	// §6.2.4.3.6: in 'U: has no permission' only.
	if p.state == pNoPermission {
		p.cfg.Out.Notify(Notice{Kind: NoticeMediaEnded})
	}
}

// request is a Floor Request (§6.2.4.3.5 1): the location not provided, which the user profile decides (TS 24.484).
func (p *Participant) request() floorctl.Message {
	msg := floorctl.Message{
		Type:  floorctl.Request,
		Other: []floorctl.Field{{ID: floorctl.FieldLocation, Value: []byte{locationNotProvided}}},
	}

	if p.cfg.Priority != 0 {
		msg.Priority = ptr(p.cfg.Priority)
	}

	if p.cfg.Indicator != 0 {
		msg.Indicator = ptr(p.cfg.Indicator)
	}

	return msg
}

// locationNotProvided is the Location Type "Not provided" (Table 8.2.3.21-3).
const locationNotProvided = 0

// ack sends the Floor Ack msg asks for (§6.2.4.3.2 1).
func (p *Participant) ack(msg floorctl.Message) {
	if a, ok := ack(msg, floorctl.SourceParticipant, p.cfg.SSRC); ok {
		p.cfg.Out.Send(a)
	}
}

func (p *Participant) send(msg floorctl.Message) {
	msg.SSRC = p.cfg.SSRC
	p.cfg.Out.Send(msg)
}

func (p *Participant) notify(kind NoticeKind, msg floorctl.Message) {
	n := Notice{Kind: kind, Party: msg.GrantedPartyID}
	if msg.RejectCause != nil {
		n.Cause = msg.RejectCause.Cause
	}

	if msg.Indicator != nil {
		n.Indicator = *msg.Indicator
	}

	if msg.Duration != nil {
		n.Duration = time.Duration(*msg.Duration) * time.Second
	}

	p.cfg.Out.Notify(n)
}
