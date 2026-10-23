package client

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/jitter"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/ua"
)

// reportInterval is the least time between two reports of the media heard to the floor participant: well below
// T103 (TS 24.380 §11.1.1), as the server's (media.Config).
const reportInterval = 200 * time.Millisecond

// sessionExpires is the session interval the client asks for, and the most it accepts: RFC 4028's minimum (§4), so
// that a call the network released without telling the client (TS 24.229 §5.2.8.1.2, the P-CSCF's BYE toward the
// server alone) ends within it, its refresh refused or never coming (RFC 4028 §10). A UE learns of the release of the
// call's bearer from its modem (TS 24.229 §6.1.1); an Android app cannot.
const sessionExpires = 90

// Call is a call of the client: a prearranged group call it placed (Client.Call) or it is invited to
// (Client.OnInvite). Its methods may be called from any goroutine: they act on the strand of the call, in order.
type Call struct {
	c        *Client
	st       *ua.Strand
	incoming bool
	manual   bool
	// group is the group called, or the calling group of an invitation; caller the calling user of an invitation.
	group, caller sip.URI

	// audio, rtcp and control are the sockets of the call; ssrc the client's own SSRC in it; speech the de-jitter
	// buffer of the speech heard.
	audio, rtcp, control media.Conn
	ssrc                 uint32
	sdpID                uint64
	speech               *jitter.Buffer

	// The state of the strand.
	out *ua.Outgoing
	// req is the INVITE of an invitation, until answered.
	req  *ua.Request
	sess *ua.Session
	// local are the fmtp:MCPTT parameters of the client's offer; answer the SDP answer of an invitation.
	local  controlchannel.Params
	answer []byte
	n      negotiated
	// taken is the answer to the offer taken, bad its error.
	taken bool
	bad   error
	part  *floor.Participant
	// early are the floor control messages before the participant of a call placed, the answer that configures it.
	early []floorctl.Message
	src   speaker
	// established, leaving, ended, failed: the call established, left before, released, its INVITE failed;
	// announced, reported: EventIncoming and EventEnded sent.
	established, leaving, ended, failed, announced, reported bool

	sent atomic.Int64
	// placed is when the call was placed, pressed when the PTT was last pressed: for the access times (TS 22.179
	// §6.15.3.1, KPI 1 and 2).
	placed, pressed time.Time
	// released tells the PTT released before the call was established: the floor of its implicit request released
	// once the participant has it (TS 24.380 §6.2.4.4.8).
	released bool

	// mu guards what the readers of the sockets share: rx is the AMR-WB payload type of the call, once negotiated;
	// cmr the last codec mode request heard.
	mu      sync.Mutex
	closed  bool
	rx      *audioFormat
	cmr     uint8
	heard   map[uint32]*Heard
	order   []uint32
	parties map[uint32]string
	// dropped are the reasons of the audio dropped so far, each logged once a call.
	dropped map[string]bool
	// lastReport is when media was last reported.
	lastReport time.Time
}

// Heard is what the sink heard of a talker in a call.
type Heard struct {
	// SSRC is the RTP SSRC of its media; Party the MCPTT ID of the Floor Taken that announced it (TS 24.380
	// §8.2.3.6, §8.2.3.16), "" without one.
	SSRC    uint32
	Party   string
	Packets int
	// Bursts are the talk bursts heard; the packets lost, by where: at the start of a burst (its first, marked,
	// RFC 4867 §4.1, missing), at the end of the one before (missing before a marked first), within one (TS 22.179
	// R-6.15.3.2-016, -017).
	Bursts, InitialLost, TrailingLost, MidLost int

	seen    bool
	lastSeq uint16
	lastTS  uint32
}

// track counts the packet p, of frames 20 ms frames, in the bursts and the losses: a burst starts after a silence,
// its timestamp further ahead than its sequence number (RFC 3550 §5.1).
func (h *Heard) track(p rtp.Packet, frames int) {
	if !h.seen {
		h.seen, h.Bursts = true, 1
		h.lastSeq, h.lastTS = p.Sequence, p.Timestamp

		return
	}

	gap := int(p.Sequence - h.lastSeq - 1)
	if gap > 1<<15 {
		// Reordered or duplicated: not a loss.
		return
	}

	// A frame of 20 ms (RFC 4867 §4.1).
	step := uint32(max(frames, 1)) * (amr.WB.ClockRate / 50)
	silence := p.Timestamp-h.lastTS > uint32(gap+1)*step

	switch {
	case silence:
		h.Bursts++

		if p.Marker {
			h.TrailingLost += gap
		} else {
			h.InitialLost += gap
		}
	default:
		h.MidLost += gap
	}

	h.lastSeq, h.lastTS = p.Sequence, p.Timestamp
}

func (c *Client) newCall(st *ua.Strand, incoming bool) (*Call, error) {
	call := &Call{
		c: c, st: st, incoming: incoming, ssrc: c.ssrc(), sdpID: rand.Uint64() >> 1,
		speech: jitter.New(jitter.Config{Codec: amr.WB}), heard: map[uint32]*Heard{}, parties: map[uint32]string{},
		cmr: amr.NoModeRequest,
	}

	// The audio, its RTCP (RFC 3550 §11), the control channel.
	cs, err := c.listen(call.receiveAudio, func([]byte, netip.AddrPort) {}, call.receiveControl)
	if err != nil {
		c.freeSSRC(call.ssrc)
		return nil, err
	}

	call.audio, call.rtcp, call.control = cs[0], cs[1], cs[2]
	call.src = speaker{seq: uint16(rand.Uint32()), ts0: rand.Uint32(), epoch: c.clock.Now()}

	return call, nil
}

// Call places a prearranged group call to group (TS 24.379 §10.1.1.2.1.1): an INVITE to the participating function
// with the MCPTT feature tags and ICSI, the mcptt-info of the group and client, the SDP offer of the speech and the
// control channel (§6.2.1), the implicit floor request and mc_granted as configured (TS 24.380 §14.2). The call
// reports its establishment or failure in events.
func (c *Client) Call(group sip.URI) (*Call, error) {
	call, err := c.newCall(c.cfg.Agent.NewStrand(), false)
	if err != nil {
		return nil, err
	}

	call.group = group
	call.placed = c.clock.Now()

	// The PTT pressed to place it (TS 24.380 §14.2.5): its access time from now, KPI 2.
	if c.cfg.ImplicitRequest {
		call.pressed = call.placed
	}

	// One call a group: the group's call the one the user is in (TS 24.379 §10.1.1.2.1.2 step 1c).
	if !c.claim(call) {
		call.closeMedia()
		return nil, ErrInCall
	}

	req, err := call.invite()
	if err == nil {
		var out *ua.Outgoing

		out, err = c.cfg.Agent.Invite(req, call.uacHandler(), ua.On(call.st), ua.WithPolicy(ua.ClientPolicy))
		if err == nil {
			call.st.Post(func() { call.out = out })
			return call, nil
		}
	}

	call.closeMedia()

	return nil, err
}

// invite is the INVITE of a call placed (TS 24.379 §10.1.1.2.1.1).
func (call *Call) invite() (*sip.Request, error) {
	c := call.c

	call.local = controlchannel.Params{
		ImplicitRequest: c.cfg.ImplicitRequest, Granted: c.cfg.ImplicitRequest && c.cfg.Granted, Priority: c.cfg.Priority,
		FloorSSRC: call.ssrc, HasFloorSSRC: true,
	}

	sdpOffer, err := offer(c.cfg.Addr, call.sdpID, call.audio.LocalAddr().Port(), call.control.LocalAddr().Port(), call.local)
	if err != nil {
		return nil, err
	}

	// Step 14.
	info, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{
		SessionType: mcpttinfo.SessionPrearranged,
		RequestURI:  mcpttinfo.URI(call.group.String()),
		ClientID:    mcpttinfo.Text(c.cfg.ClientID),
	}})
	if err != nil {
		return nil, err
	}

	// Steps 4, 5, 7, 10; the Session-Expires of step 9, a recommendation, the agent's (ua.SessionTimer).
	req := sip.NewRequest("INVITE", c.cfg.ParticipatingPSI)
	req.Header.Set("To", sip.Address{URI: c.cfg.ParticipatingPSI}.String())
	req.Header.Set("Contact", c.contact().String())
	// Step 9: the refresher left to the server, as recommended.
	req.Header.Set("Session-Expires", sip.SessionExpires{Seconds: sessionExpires}.String())

	for _, v := range AcceptContact() {
		req.Header.Add("Accept-Contact", v)
	}

	req.SetBodies(ua.SDP(sdpOffer), sip.NewBodyPart(mcpttinfo.ContentType, info))

	if err := c.originate(req); err != nil {
		return nil, err
	}

	return req, nil
}

func (call *Call) uacHandler() ua.Handler {
	return ua.Handler{
		Answer: func(s *ua.Session, b []byte) {
			call.sess = s
			call.takeAnswer(b)
		},
		Established: func(s *ua.Session, msg sip.Message) {
			call.sess = s
			// The 2xx confirms the session before it reports its answer.
			if _, remote, ok := s.Negotiated(); ok {
				call.takeAnswer(remote)
			}

			res, _ := msg.(*sip.Response)
			call.originated(res)
		},
		Failed: func(_ *ua.Outgoing, res *sip.Response, err error) {
			call.failed = true
			call.release()
			call.c.emit(Event{Kind: EventFailed, Call: call, Response: res, Err: err})
		},
		Ended: call.endedBy,
	}
}

// takeAnswer takes the answer to the offer of the call, once: the media to send to, and the floor participant of
// the parameters negotiated (TS 24.380 §14.4).
func (call *Call) takeAnswer(b []byte) {
	if call.taken || call.ended {
		return
	}

	call.taken = true

	n, err := takeAnswer(b, call.local)
	if err != nil {
		call.bad = err
		return
	}

	call.n = n
	call.hear(n.format)

	// The SSRC the server assigned the client's messages, or the client's own (§8.1.2, §14.4).
	ssrc := call.ssrc
	if n.control.HasFloorSSRC {
		ssrc = n.control.FloorSSRC
	}

	if !call.newParticipant(ssrc, min(call.c.cfg.Priority, n.control.Priority)) {
		return
	}

	for _, msg := range call.early {
		call.part.Receive(msg)
	}

	call.early = nil
}

// originated is the 2xx of the call placed (TS 24.379 §10.1.1.2.1.1): the floor participant told (TS 24.380
// §6.2.4.2.2).
func (call *Call) originated(res *sip.Response) {
	switch {
	case call.ended:
		return
	case call.bad != nil || call.part == nil:
		err := call.bad
		if err == nil {
			err = errMedia
		}

		call.failed = true
		call.leave()
		call.c.emit(Event{Kind: EventFailed, Call: call, Response: res, Err: err})

		return
	case call.leaving:
		call.leave()
		return
	}

	call.established = true
	call.c.cfg.Logger.Info("call established", slog.Duration("setup", call.c.clock.Now().Sub(call.placed)))

	cc := call.n.control
	o := floor.Origination{Implicit: cc.ImplicitRequest, Granted: cc.Granted}

	if cc.HasSSRC {
		o.AudioSSRC = cc.SSRC
	}

	call.c.emit(Event{Kind: EventEstablished, Call: call, Response: res})
	call.part.Originate(o)

	switch {
	case call.released:
		call.released = false
		call.part.Unpress()
	case call.c.cfg.ImplicitRequest && !cc.ImplicitRequest && !call.pressed.IsZero():
		// The implicit request not accepted (§14.3.5), the PTT still held: its Floor Request (TS 24.380 §6.2.4.3.5).
		call.part.Press()
	}
}

// newParticipant makes the floor participant of the call (TS 24.380 §6.2.1), its messages of ssrc, requesting
// priority: the user's, within the one negotiated (§14.3.3).
func (call *Call) newParticipant(ssrc uint32, priority uint8) bool {
	p, err := floor.NewParticipant(floor.ParticipantConfig{
		Clock:    strandClock{st: call.st, clock: call.c.clock},
		Out:      participantOut{call},
		Timers:   call.c.cfg.Floor,
		SSRC:     ssrc,
		Priority: priority,
	})
	if err != nil {
		call.bad = err
		return false
	}

	call.part = p

	return true
}

// OnInvite takes an invitation to a call (TS 24.379 §10.1.1.2.1.2), for the OnInvite of the agent: answered at once
// in automatic commencement (§6.2.3.1.2), or once the user accepts in manual commencement (§6.2.3.2.2), as the
// Answer-Mode of the INVITE asks or, without one, as the client is configured: the client allows both (steps 7b,
// 8b). Its offer needs AMR-WB speech and a control channel (§6.2.2).
func (c *Client) OnInvite(r *ua.Request) ua.Handler {
	call, err := c.newCall(r.Strand(), true)
	if err != nil {
		// Step 1a: not enough resources.
		_ = r.Reject(480)
		return ua.Handler{}
	}

	h := ua.Handler{Established: call.answered, Ended: call.endedBy}
	call.req, call.sess = r, r.Session()
	call.sess.SetPolicy(ua.ClientPolicy)

	code := call.invited(r)
	// §10.1.1.2.1.2 step 1c: an invitation to a group the client already has a call of, placing it or in it, the
	// other: refused, so that the client is in the group's call once (the INVITE it placed joins the call, §10.1.1.4.2
	// step 15).
	if code == 0 && !c.claim(call) {
		code = 486
	}

	if code != 0 {
		call.req = nil
		call.release()

		_ = r.Reject(code)

		return h
	}

	call.announced = true
	c.emit(Event{Kind: EventIncoming, Call: call})

	if !call.manual {
		call.accept()
		return h
	}

	// §6.2.3.2.2 step 1: the Contact of the 183 with the MCPTT feature tags.
	_ = r.Progress(ua.Reply{Code: 183, Header: sip.Header{{Name: "Contact", Value: c.contact().String()}}})

	return h
}

// invited answers the offer of an invitation, or returns the code to reject it with.
func (call *Call) invited(r *ua.Request) int {
	c := call.c
	req := r.Message()

	sdpOffer, ok := r.Offer()
	if !ok {
		return 488
	}

	local := controlchannel.Params{Priority: 0xff, FloorSSRC: call.ssrc, HasFloorSSRC: true}

	ans, n, err := answerOffer(sdpOffer, c.cfg.Addr, call.sdpID, call.audio.LocalAddr().Port(),
		call.control.LocalAddr().Port(), local)
	if err != nil {
		return 488
	}

	call.answer, call.n, call.taken = ans, n, true
	call.hear(n.format)

	ssrc := call.ssrc
	if n.offered.HasFloorSSRC {
		ssrc = n.offered.FloorSSRC
	}

	if !call.newParticipant(ssrc, min(call.c.cfg.Priority, n.control.Priority)) {
		return 500
	}

	if part, ok, err := req.Content().Find(mcpttinfo.ContentType); ok && err == nil {
		if info, _ := mcpttinfo.Decode(part.Body); info.Params != nil {
			call.group = contentURI(info.Params.CallingGroupID)
			call.caller = contentURI(info.Params.CallingUserID)
		}
	}

	call.manual = c.cfg.Manual

	if mode, ok, err := req.Header.AnswerMode(); ok && err == nil {
		switch {
		case mode.Is(sip.AnswerModeAuto):
			call.manual = false
		case mode.Is(sip.AnswerModeManual):
			call.manual = true
		}
	}

	return 0
}

func contentURI(c *mcpttinfo.Content) sip.URI {
	if c == nil {
		return sip.URI{}
	}

	v, ok := c.URI()
	if !ok {
		return sip.URI{}
	}

	u, _ := sip.ParseURI(v)

	return u
}

// accept sends the 200 of an invitation (TS 24.379 §6.2.3.1.1): the Contact with the MCPTT feature tags, the
// session timer refreshed by the client (steps 2 to 5; the agent requires timer of a UAC that supports it), the SDP
// answer; the call is then established for the floor participant (TS 24.380 §6.2.4.2.3 NOTE).
func (call *Call) accept() {
	r := call.req
	if r == nil || call.ended {
		return
	}

	call.req = nil

	// The interval asked for, reduced to the client's, never below the Min-SE (RFC 4028 §9).
	h := r.Message().Header
	se := sip.SessionExpires{Seconds: sessionExpires, Refresher: sip.RefresherUAS}

	if asked, ok, err := h.SessionExpires(); ok && err == nil {
		se.Seconds = min(asked.Seconds, sessionExpires)
	}

	if least, ok, err := h.MinSE(); ok && err == nil {
		se.Seconds = max(se.Seconds, least)
	}

	err := r.Respond(ua.Reply{
		Code: 200,
		Header: sip.Header{
			{Name: "Contact", Value: call.c.contact().String()},
			{Name: "Session-Expires", Value: se.String()},
		},
		Bodies: []sip.BodyPart{ua.SDP(call.answer)},
	})
	if err != nil {
		call.release()
		return
	}

	call.part.Join()
}

// decline declines an invitation (TS 24.379 §6.2.3.2.2 step 2).
func (call *Call) decline() {
	r := call.req
	if r == nil || call.ended {
		return
	}

	call.req = nil
	call.release()
	_ = r.Reject(480, identity.Warning(call.c.contact().URI.Host, identity.WarnUserDeclined))
}

// answered is the ACK of the 200 of an invitation.
func (call *Call) answered(*ua.Session, sip.Message) {
	if call.ended {
		return
	}

	call.established = true
	call.c.emit(Event{Kind: EventEstablished, Call: call})
}

// endedBy is the end of the session: by the server's BYE (TS 24.379 §10.1.1.2.3.3, §6.2.6), the client's, or a
// failure.
func (call *Call) endedBy(_ *ua.Session, e ua.End) {
	call.release()

	if call.failed || call.reported || call.incoming && !call.announced {
		return
	}

	call.reported = true
	call.c.emit(Event{Kind: EventEnded, Call: call, End: e})
}

// PressPTT is the user's request to talk (TS 24.380 §6.2.4.3.5).
func (call *Call) PressPTT() {
	call.st.Post(func() {
		switch {
		case call.ended:
		case !call.established:
			// Held again before the call is up: its implicit request stands.
			call.released = false
		case call.part != nil:
			call.pressed = call.c.clock.Now()
			call.part.Press()
		}
	})
}

// ReleasePTT is the user's end of talk (TS 24.380 §6.2.4.4.8, §6.2.4.5.3).
func (call *Call) ReleasePTT() {
	call.st.Post(func() {
		switch {
		case call.ended:
		case !call.established:
			// Released before the call is up: the floor its implicit request gets released at once (originated).
			call.released = true
		case call.part != nil:
			call.part.Unpress()
		}
	})
}

// Accept accepts an invitation in manual commencement (TS 24.379 §6.2.3.2.2).
func (call *Call) Accept() { call.st.Post(call.accept) }

// Decline declines an invitation in manual commencement, with a 480 (TS 24.379 §6.2.3.2.2 step 2).
func (call *Call) Decline() { call.st.Post(call.decline) }

// Leave leaves the call (TS 24.379 §10.1.1.2.3.1, §6.2.4.1): the floor participant and the media released, a BYE
// sent; a call placed not yet established is cancelled (§6.2.5.1 step 1), an invitation not answered declined.
func (call *Call) Leave() { call.st.Post(call.leave) }

func (call *Call) leave() {
	switch {
	case call.ended:
	case call.req != nil:
		call.decline()
	case call.sess != nil && (call.established || call.incoming || call.sess.State() == ua.Confirmed):
		// TS 24.380 §6.2.2 step 1 before the BYE.
		call.release()
		_ = call.sess.Bye(ua.Change{}, nil)
	case call.out != nil:
		// The 2xx may come first: the session is then ended once established.
		call.leaving = true
		if call.out.Cancel(ua.Change{}) != nil {
			return
		}
	default:
		call.leaving = true
	}
}

// release releases the media of the call: the floor participant (TS 24.380 §6.2.2), the speech sent, the sockets.
func (call *Call) release() {
	if call.ended {
		return
	}

	call.ended = true
	call.logMedia()

	if call.part != nil {
		call.part.Release()
	}

	call.stopSource()
	call.closeMedia()
}

func (call *Call) closeMedia() {
	call.mu.Lock()
	call.closed = true
	call.mu.Unlock()

	closeAll([]media.Conn{call.audio, call.rtcp, call.control})
	call.c.freeSSRC(call.ssrc)
	call.c.unclaim(call)
}

// logMedia logs the speech of the call: the packets it sent and heard, the jitter of the path from the server, the
// de-jitter buffer's delay and counts (TS 26.114 §8.2.2).
func (call *Call) logMedia() {
	var heard, bursts, initial, trailing, mid int
	for _, h := range call.Heard() {
		heard += h.Packets
		bursts += h.Bursts
		initial += h.InitialLost
		trailing += h.TrailingLost
		mid += h.MidLost
	}

	st := call.speech.Stats()
	call.c.cfg.Logger.Info("call media", slog.Int("sent", call.Sent()), slog.Int("heard", heard),
		slog.Duration("jitter", call.speech.Jitter()), slog.Duration("delay", call.speech.Delay()),
		slog.Int("received", st.Received), slog.Int("late", st.Late), slog.Int("lost", st.Lost),
		slog.Int("duplicates", st.Duplicates), slog.Int("dropped", st.Dropped), slog.Int("bursts", bursts),
		slog.Int("initial_lost", initial), slog.Int("trailing_lost", trailing), slog.Int("mid_lost", mid))
}

// Incoming reports whether the client was invited to the call; Manual whether the user is to accept it.
func (call *Call) Incoming() bool { return call.incoming }

func (call *Call) Manual() bool { return call.manual }

// Group is the group called or, of an invitation, the calling group of its mcptt-info (TS 24.379 F.1.3); Caller the
// calling user of an invitation.
func (call *Call) Group() sip.URI { return call.group }

func (call *Call) Caller() sip.URI { return call.caller }

// Sent is the number of RTP packets of speech the client sent.
func (call *Call) Sent() int { return int(call.sent.Load()) }

// Heard is what the sink heard, by talker in the order first heard.
func (call *Call) Heard() []Heard {
	call.mu.Lock()
	defer call.mu.Unlock()

	out := make([]Heard, 0, len(call.order))

	for _, s := range call.order {
		h := *call.heard[s]
		h.Party = call.parties[s]
		out = append(out, h)
	}

	return out
}

// participantOut is the floor participant's output: the control channel, the speech sent, the user.
type participantOut struct{ call *Call }

func (o participantOut) Send(msg floorctl.Message) { o.call.sendFloor(msg) }

func (o participantOut) Transmit(on bool, ssrc uint32) { o.call.transmit(on, ssrc) }

func (o participantOut) Notify(n floor.Notice) {
	switch n.Kind {
	case floor.NoticeRequestTimeout:
		o.call.probe()
	case floor.NoticeGranted:
		// KPI 1, or 2 of a call placed with the implicit request.
		if call := o.call; !call.pressed.IsZero() {
			call.c.cfg.Logger.Info("floor granted", slog.Duration("access", call.c.clock.Now().Sub(call.pressed)))
			call.pressed = time.Time{}
		}
	}

	o.call.c.emit(Event{Kind: EventFloor, Call: o.call, Floor: n})
}

// probe refreshes the session at once, a floor request unanswered (TS 24.380 §6.2.4.4.6): an UPDATE without an offer
// (RFC 3311, RFC 4028 §7.4). The call the network released ends with its 481, or without a response (RFC 5057), as
// the session timer would end it later.
func (call *Call) probe() {
	if call.ended || call.sess == nil || !call.established {
		return
	}

	if err := call.sess.Update(ua.Change{}, nil); err != nil {
		call.c.cfg.Logger.Debug("probe not sent", slog.Any("error", err))
	}
}

// sendFloor sends a floor control message to the server's control channel, one APP packet (TS 24.380 §8.1.1).
func (call *Call) sendFloor(msg floorctl.Message) {
	if call.ended {
		return
	}

	app, err := msg.App()
	if err != nil {
		return
	}

	if b, err := rtp.AppendRTCP(nil, app); err == nil {
		_ = call.control.WriteTo(b, call.n.remote.control)
	}
}

// receiveControl is a datagram on the control channel: its floor control messages, to the floor participant. An
// unknown message is ignored whole (TS 24.380 §8.1.4), logged.
func (call *Call) receiveControl(b []byte, from netip.AddrPort) {
	// The packets before a malformed one count.
	ps, err := rtp.ParseRTCP(b)
	if err != nil {
		call.c.cfg.Logger.Debug("floor control datagram malformed", slog.String("from", from.String()), slog.Any("error", err))
	}

	for _, p := range ps {
		app, ok := p.(*rtp.App)
		if !ok || app.Name != floorctl.Name {
			continue
		}

		msg, err := floorctl.Decode(app)
		if err != nil && !errors.Is(err, floorctl.ErrField) {
			call.c.cfg.Logger.Debug("floor control message dropped", slog.String("from", from.String()),
				slog.Uint64("subtype", uint64(app.Subtype)), slog.Any("error", err))

			continue
		}

		// The talker a Floor Taken announces (§8.2.3.6, §8.2.3.16): its party, and its speech to play first.
		if msg.Type == floorctl.Taken && msg.GrantedSSRC != nil {
			call.mu.Lock()
			call.parties[*msg.GrantedSSRC] = msg.GrantedPartyID
			call.mu.Unlock()

			call.speech.SetTalker(*msg.GrantedSSRC)
		}

		call.st.Post(func() { call.receiveFloor(msg) })
	}
}

func (call *Call) receiveFloor(msg floorctl.Message) {
	switch {
	case call.ended:
	case call.part == nil:
		call.early = append(call.early, msg)
	default:
		call.part.Receive(msg)
	}
}

// hear makes f the AMR-WB payload type of the speech heard, once negotiated.
func (call *Call) hear(f audioFormat) {
	call.mu.Lock()
	defer call.mu.Unlock()

	call.rx = &f
}

// receiveAudio is a datagram on the audio port: RTP media of the payload type negotiated, its frames to the de-jitter
// buffer (RFC 4867 §4.3, §4.4), recorded by its SSRC, and reported to the floor participant at most once per report
// interval (TS 24.380 §6.2.4.3.4). A payload that does not parse is discarded (RFC 4867 §4.3.2). What is dropped is
// logged, once a call for each reason.
func (call *Call) receiveAudio(b []byte, from netip.AddrPort) {
	p, err := rtp.Parse(b)
	if err != nil || len(p.Payload) == 0 {
		call.mu.Lock()
		call.dropAudio(from, "malformed RTP", err)
		call.mu.Unlock()

		return
	}

	now := call.c.clock.Now()

	call.mu.Lock()

	switch {
	case call.closed:
		call.mu.Unlock()
		return
	case call.rx == nil:
		call.dropAudio(from, "no payload type negotiated", nil)
		call.mu.Unlock()

		return
	case p.PayloadType != call.rx.pt:
		call.dropAudio(from, fmt.Sprintf("payload type %d, %d negotiated", p.PayloadType, call.rx.pt), nil)
		call.mu.Unlock()

		return
	}

	payload, err := amr.WB.ParsePayload(p.Payload, call.rx.octetAligned)
	if err != nil {
		call.dropAudio(from, "AMR-WB payload malformed", err)
		call.mu.Unlock()

		return
	}

	call.speech.Put(p.SSRC, p.Timestamp, payload.Frames, now)

	// RFC 4867 §4.3.1: a codec mode request for the client's encoder, within the mode-set, valid until the next.
	request := -1
	if payload.CMR != call.cmr && slices.Contains(call.rx.modes(), int(payload.CMR)) {
		call.cmr, request = payload.CMR, int(payload.CMR)
	}

	h := call.heard[p.SSRC]

	first := h == nil
	if first {
		h = &Heard{SSRC: p.SSRC}
		call.heard[p.SSRC] = h
		call.order = append(call.order, p.SSRC)
	}

	h.Packets++
	h.track(p, len(payload.Frames))

	report := call.lastReport.IsZero() || now.Sub(call.lastReport) >= reportInterval
	if report {
		call.lastReport = now
	}

	party := call.parties[p.SSRC]
	call.mu.Unlock()

	if !first && !report && request < 0 {
		return
	}

	ssrc := p.SSRC

	call.st.Post(func() {
		if call.ended {
			return
		}

		if report && call.part != nil {
			call.part.Media()
		}

		if first {
			call.c.emit(Event{Kind: EventHeard, Call: call, SSRC: ssrc, Party: party})
		}

		if request >= 0 {
			call.c.emit(Event{Kind: EventModeRequest, Call: call, Mode: request})
		}
	})
}

// dropAudio logs audio of from dropped for why, the first time a call; call.mu held.
func (call *Call) dropAudio(from netip.AddrPort, why string, err error) {
	if call.dropped[why] {
		return
	}

	if call.dropped == nil {
		call.dropped = map[string]bool{}
	}

	call.dropped[why] = true
	call.c.cfg.Logger.Debug("audio dropped", slog.String("reason", why), slog.String("from", from.String()), slog.Any("error", err))
}

// NextSpeech is the speech heard for the next 20 ms, for the decoder, in the storage format of RFC 4867 §5.3: a frame
// of the talker, SPEECH_LOST for one missing, NO_DATA in silence. The platform takes one each 20 ms of its playout.
func (call *Call) NextSpeech() []byte {
	b, _ := amr.WB.AppendStorage(nil, call.speech.Get(call.c.clock.Now()))

	return b
}

// SpeechDelay is the buffering delay of the speech heard, and its counts.
func (call *Call) SpeechDelay() (time.Duration, jitter.Stats) {
	return call.speech.Delay(), call.speech.Stats()
}

// speaker is the speech the client sends: its frames from the encoder while the floor participant transmits, of the
// payload type negotiated and the SSRC granted (TS 24.380 §6.2.4.4.2 step 7).
type speaker struct {
	on    bool
	ssrc  uint32
	seq   uint16
	ts0   uint32
	epoch time.Time
	// silent is set when the frame before was no speech: the next speech frame starts a talkspurt (RFC 4867 §4.1).
	silent bool

	// base is the timestamp of the first frame of the transmission, n the frames since; next the timestamp after the
	// last frame sent.
	base uint32
	n    int
	next uint32
	sent bool
	// started is when the transmission started: the delay to its first frame logged, of the capture (TS 22.179
	// R-6.15.3.2-016).
	started time.Time
}

// transmit starts the transmission of the speech with ssrc, the client's own if the server assigned none, or stops
// it, and tells the platform, its encoder at the mode negotiated.
func (call *Call) transmit(on bool, ssrc uint32) {
	src := &call.src

	switch {
	case !on && !src.on:
		return
	case !on:
		src.on = false

		call.c.emit(Event{Kind: EventTransmit, Call: call, Transmitting: false})

		return
	}

	if ssrc == 0 {
		ssrc = call.ssrc
	}

	src.ssrc = ssrc

	if src.on {
		return
	}

	// The timestamp of the first frame is the sampling instant on the clock of the call (RFC 3550 §5.1), after the
	// frames already sent.
	now := call.c.clock.Now()

	src.on, src.silent, src.n, src.started = true, true, 0, now
	src.base = src.ts0 + uint32(now.Sub(src.epoch)/(time.Second/clockRate))

	if src.sent && int32(src.base-src.next) < 0 {
		src.base = src.next
	}

	call.c.emit(Event{Kind: EventTransmit, Call: call, Transmitting: true, Mode: call.n.format.mode})
}

func (call *Call) stopSource() { call.src.on = false }

var (
	errSpeechFrame   = errors.New("client: not an AMR-WB frame of the mode-set")
	errNotNegotiated = errors.New("client: speech of a call not established")
)

// SendSpeech sends a frame of the encoder, in the storage format of RFC 4867 §5.3, while the client transmits
// (EventTransmit): each one 20 ms after the one before, its timestamp 320 after (RFC 3550 §5.1), one frame per
// packet (TS 26.179 §4.1.3: ptime 20). A NO_DATA frame of DTX takes its 20 ms without a packet (RFC 4867 §4.3.2); a
// speech frame after silence starts a talkspurt, the marker bit set (§4.1). Frames while the client does not transmit
// are dropped.
func (call *Call) SendSpeech(frame []byte) error {
	f, n, err := amr.WB.ParseStorage(frame)
	if err != nil || n != len(frame) || f.Type == amr.FrameSpeechLost {
		return errSpeechFrame
	}

	call.mu.Lock()
	format := call.rx
	call.mu.Unlock()

	switch {
	case format == nil:
		return errNotNegotiated
	case int(f.Type) < len(amr.WB.FrameBits) && !slices.Contains(format.modes(), int(f.Type)):
		return errSpeechFrame
	}

	call.st.Post(func() { call.sendSpeech(f) })

	return nil
}

func (call *Call) sendSpeech(f amr.Frame) {
	src := &call.src
	if !src.on || call.ended {
		return
	}

	ts := src.base + uint32(src.n)*frameSamples
	src.n++

	if src.n == 1 {
		call.c.cfg.Logger.Info("first frame", slog.Duration("after", call.c.clock.Now().Sub(src.started)))
	}

	speech := int(f.Type) < len(amr.WB.FrameBits)

	if f.Type == amr.FrameNoData {
		src.silent = true
		return
	}

	marker := speech && src.silent
	src.silent = !speech

	b, err := amr.WB.AppendPayload(nil, amr.Payload{CMR: amr.NoModeRequest, Frames: []amr.Frame{f}}, call.n.format.octetAligned)
	if err != nil {
		return
	}

	p := rtp.Packet{
		Marker:      marker,
		PayloadType: call.n.format.pt,
		Sequence:    src.seq,
		Timestamp:   ts,
		SSRC:        src.ssrc,
		Payload:     b,
	}

	src.seq++
	src.sent, src.next = true, ts+frameSamples

	if out, err := p.Append(nil); err == nil && call.audio.WriteTo(out, call.n.remote.audio) == nil {
		call.sent.Add(1)
	}
}
