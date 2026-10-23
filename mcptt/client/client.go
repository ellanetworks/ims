// Package client is the MCPTT client (TS 24.379 §6.2, TS 24.380 §6.2) of Ella MCPTT's clients, the Android one and the
// lab's: registration for service authorisation (§7.2.1), affiliation (§9.2.1.2), prearranged group calls placed and
// received (§10.1.1.2.1), left (§10.1.1.2.3) or ended by the server, the floor participant of each call (TS 24.380
// §6.2.4) on its control channel, and the AMR-WB speech of each call in RTP (TS 26.179 §4.1, RFC 4867): the frames of
// the platform's encoder sent while the client has the floor (Call.SendSpeech), those heard played out of a
// de-jitter buffer to its decoder (Call.NextSpeech). clienttest speaks for the tests and the lab.
//
// It runs on a sip/ua agent it is given, with a function completing the initial requests it sends: in direct mode
// [PM 14] an agent of its own registering with the server, through the IMS the agent of an IMS UE (ims/ue). Its
// media sockets are on a media.Network, mediatest's in the tests, and its timers on the agent's clock, on the strand
// of each call.
package client

import (
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// Config configures a client.
type Config struct {
	// Agent is the agent the client runs on. Its OnInvite gives the client the INVITEs for it (Client.OnInvite).
	Agent *ua.Agent

	// Originate completes the initial requests the client sends but REGISTER, before they go to the agent: the Route,
	// From and P-Preferred-Identity of the IMS UE hosting the client (UE.Originate of ims/ue). Without it, in direct
	// mode, the client sets From to IMPU.
	Originate func(req *sip.Request) error

	// IMPU is the public user identity of the client: the To and From of its REGISTERs, the From of its requests in
	// direct mode.
	IMPU sip.URI
	// MCPTTID is the MCPTT ID of the user (TS 24.379 §4.2), of its affiliations (§9.2.1.2).
	MCPTTID sip.URI
	// ClientID is the MCPTT client ID, a urn:uuid (TS 23.280 §8.1.4).
	ClientID string
	// Token is the access token of the service authorisation (TS 24.379 §7.2.1), given by an identity management
	// server. Without one [PM 4], each REGISTER carries an UnsignedToken of MCPTTID and ClientID, made then.
	Token string

	// Contact is the Contact of the client now: of its REGISTERs, its INVITEs and its responses to INVITEs, with the
	// MCPTT feature tags (TS 24.379 §7.2.1, §10.1.1.2.1.1 step 4, §6.2.3.1.1 steps 3, 4), added when it lacks them.
	// Through the IMS, the UE's (UE.Contact of ims/ue), which its registrations change (TS 33.203 §7.1 NOTE 10); of
	// an agent of fixed address, FixedContact.
	Contact func() sip.Address
	// Registrar is the Request-URI of a REGISTER in direct mode: the server's domain.
	Registrar sip.URI
	// ParticipatingPSI is the public service identity of the participating function serving the user: the
	// Request-URI of its INVITEs and PUBLISHes (TS 24.379 §10.1.1.2.1.1 step 10, §9.2.1.2 step 1).
	ParticipatingPSI sip.URI

	// Media is the network of the media sockets, on Addr, the address of the SDP too; ports from MinPort to MaxPort,
	// four per call: audio on an even port, its RTCP on the next (RFC 3550 §11), the control channel on the one after.
	Media            media.Network
	Addr             netip.Addr
	MinPort, MaxPort uint16

	// Floor are the timers and counters of the floor participant (TS 24.483 §8.2.11–§8.2.13): T100 and T101
	// required.
	Floor floor.ParticipantTimers
	// ImplicitRequest makes the INVITE of a call an implicit floor request (TS 24.380 §14.2.5); Granted accepts the
	// floor granted in its 200 (§14.2.4).
	ImplicitRequest, Granted bool
	// Priority is the floor priority the client asks for (§14.2.3), the default priority if 0.
	Priority uint8
	// Manual is the service setting for answering calls: manual commencement (TS 24.379 §6.2.3.2), the user
	// accepting the call with Call.Accept or declining it; automatic commencement (§6.2.3.1) otherwise.
	Manual bool

	// OnEvent receives the events of the client, on the strand of their call, or of the registration.
	OnEvent func(Event)
	// Logger logs, at debug, the media and floor control messages the calls drop; none without one.
	Logger *slog.Logger
}

// Client is an MCPTT client.
type Client struct {
	cfg   Config
	clock transaction.Clock
	// st is the strand of the registration and the publications.
	st *ua.Strand

	mu    sync.Mutex
	next  uint16
	ssrcs map[uint32]bool
	// groups are the calls by group, one a group: a group's call placed or invited to until it ends.
	groups map[string]*Call
	reg    registration
	// aff is the publication of the affiliations, nil without one.
	aff *publication
}

// Errors of New, and of Call.
var (
	ErrConfig = errors.New("client: configuration")
	// ErrInCall is the error of a call to a group the client already has a call of.
	ErrInCall = errors.New("client: a call of the group already")
)

// groupKey is the key of a group's URI: its scheme, user and host, the host in lower case (RFC 3261 §19.1.4).
func groupKey(u sip.URI) string {
	if u.User == "" && u.Host == "" {
		return ""
	}

	return u.Scheme + ":" + u.User + "@" + strings.ToLower(u.Host)
}

// claim makes call the call of its group, false if the group has another; a call of no group claims nothing.
func (c *Client) claim(call *Call) bool {
	k := groupKey(call.group)
	if k == "" {
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if o, ok := c.groups[k]; ok && o != call {
		return false
	}

	c.groups[k] = call

	return true
}

// unclaim ends call as the call of its group.
func (c *Client) unclaim(call *Call) {
	k := groupKey(call.group)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.groups[k] == call {
		delete(c.groups, k)
	}
}

// New makes a client.
func New(cfg Config) (*Client, error) {
	switch {
	case cfg.Agent == nil || cfg.Media == nil:
		return nil, errors.Join(ErrConfig, errors.New("an agent and a media network required"))
	case cfg.MinPort%2 != 0 || cfg.MaxPort < cfg.MinPort+portsPerCall-1:
		return nil, errors.Join(ErrConfig, errors.New("the port range holds no call"))
	case cfg.Floor.T100 <= 0 || cfg.Floor.T101 <= 0:
		return nil, errors.Join(ErrConfig, errors.New("T100 and T101 required"))
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	c := &Client{
		cfg: cfg, clock: cfg.Agent.Layer().Clock(), st: cfg.Agent.NewStrand(),
		next: cfg.MinPort, ssrcs: map[uint32]bool{}, groups: map[string]*Call{},
	}

	return c, nil
}

// Features are the feature tags of MCPTT, in the Contact of the client (TS 24.379 §7.2.1): those a UE hosting it
// registers for it.
func Features() sip.FeatureSet {
	return sip.FeatureSet{sip.NewFeature(identity.FeatureTag), sip.NewFeature("+g.3gpp.icsi-ref", identity.ICSI)}
}

// withFeatures is a with the MCPTT feature tags it lacks.
// FixedContact is the Contact of a client whose agent has the fixed address a.
func FixedContact(a sip.Address) func() sip.Address { return func() sip.Address { return a } }

// contact is the Contact of the client now, with the MCPTT feature tags.
func (c *Client) contact() sip.Address { return withFeatures(c.cfg.Contact()) }

func withFeatures(a sip.Address) sip.Address {
	a = a.Clone()

	have, _ := a.Features()
	for _, f := range Features() {
		if have.Has(f.Tag) {
			continue
		}

		// The terms of features encode.
		p, _ := f.Param()
		a.Params = append(a.Params, p)
	}

	return a
}

// AcceptContact are the Accept-Contact values of an initial INVITE (TS 24.379 §10.1.1.2.1.1 steps 5, 7): each
// feature tag, required and explicit.
func AcceptContact() []string {
	var out []string

	for _, f := range Features() {
		// The terms of features encode.
		v, _ := sip.CallerPreference{Features: sip.FeatureSet{f}, Require: true, Explicit: true}.Value()
		out = append(out, v)
	}

	return out
}

// originate completes an initial request with what the IMS UE or, in direct mode, the client adds.
func (c *Client) originate(req *sip.Request) error {
	if !req.Header.Has("P-Preferred-Service") {
		// TS 24.379 §9.2.1.2 step 3, §10.1.1.2.1.1 step 6.
		req.Header.Add("P-Preferred-Service", identity.ICSI)
	}

	if c.cfg.Originate != nil {
		return c.cfg.Originate(req)
	}

	if !req.Header.Has("From") {
		req.Header.Add("From", sip.Address{URI: c.cfg.IMPU}.String())
	}

	return nil
}

func (c *Client) emit(e Event) {
	if c.cfg.OnEvent != nil {
		c.cfg.OnEvent(e)
	}
}

// portsPerCall are the ports of a call: audio, RTCP, control channel, and one left so that the next audio port is
// even.
const portsPerCall = 4

// ErrNoPort is the error of a call without ports left.
var ErrNoPort = errors.New("client: no port left")

// listen opens the sockets of a call on consecutive ports of the range, one per handler.
func (c *Client) listen(hs ...func([]byte, netip.AddrPort)) ([]media.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	span := int(c.cfg.MaxPort-c.cfg.MinPort+1) / portsPerCall

	for range span {
		p := c.next

		// The next call's ports all within the range, else from its start.
		next := int(p) + portsPerCall
		if next+portsPerCall-1 > int(c.cfg.MaxPort) {
			next = int(c.cfg.MinPort)
		}

		c.next = uint16(next)

		cs, err := c.open(p, hs)
		if err == nil {
			return cs, nil
		}
	}

	return nil, ErrNoPort
}

func (c *Client) open(port uint16, hs []func([]byte, netip.AddrPort)) ([]media.Conn, error) {
	var cs []media.Conn

	for i, h := range hs {
		conn, err := c.cfg.Media.Listen(netip.AddrPortFrom(c.cfg.Addr, port+uint16(i)), h)
		if err != nil {
			closeAll(cs)
			return nil, err
		}

		cs = append(cs, conn)
	}

	return cs, nil
}

func closeAll(cs []media.Conn) {
	for _, conn := range cs {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

// ssrc is a new random SSRC (RFC 3550 §8.1), unique among those of the client's calls: the one it assigns the
// server in mc_floor_ssrc (TS 24.380 §14.2.7, §14.3.8), its own RTCP SSRC and RTP SSRC without one assigned.
func (c *Client) ssrc() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		if s := rand.Uint32(); s != 0 && !c.ssrcs[s] {
			c.ssrcs[s] = true
			return s
		}
	}
}

func (c *Client) freeSSRC(s uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.ssrcs, s)
}

// Event is an event of the client.
type Event struct {
	Kind EventKind
	// Call is the call of the event, nil for EventRegistration.
	Call *Call
	// Floor is the notice of the floor participant of EventFloor.
	Floor floor.Notice
	// SSRC and Party are the talker of EventHeard: its RTP SSRC, and its MCPTT ID as the Floor Taken announcing it
	// told, "" before it came.
	SSRC  uint32
	Party string
	// Response is the final response of EventFailed and EventRegistration, and the 2xx of the EventEstablished of
	// a call placed, with its P-Answer-State (TS 24.379 §10.1.1.2.1.1 step 2A); Err the error without one.
	Response *sip.Response
	Err      error
	// End is how the call ended, of EventEnded.
	End ua.End
	// Transmitting tells, of EventTransmit, whether the client sends speech now; Mode is then the AMR-WB mode of the
	// frames its encoder is to make (TS 26.179 §4.1.1), and of EventModeRequest the mode the server requests.
	Transmitting bool
	Mode         int
}

// EventKind is the kind of an event.
type EventKind int

const (
	// EventIncoming: an invitation to a call (TS 24.379 §10.1.1.2.1.2), Call.Manual telling whether the user is to
	// accept it.
	EventIncoming EventKind = iota + 1
	// EventEstablished: the call is established, its 200 received (§10.1.1.2.1.1) or, invited, its ACK.
	EventEstablished
	// EventFailed: the INVITE of the call failed.
	EventFailed
	// EventEnded: the call ended, left or released.
	EventEnded
	// EventFloor: a notice of the floor participant (TS 24.380 §6.2.4).
	EventFloor
	// EventHeard: media of a talker heard for the first time in the call.
	EventHeard
	// EventRegistration: the response to a REGISTER refreshing the registration.
	EventRegistration
	// EventTransmit: the client starts or stops sending speech (Call.SendSpeech), its floor granted or no more
	// (TS 24.380 §6.2.4).
	EventTransmit
	// EventModeRequest: the server requests another mode of the client's encoder (RFC 4867 §4.3.1), within the
	// mode-set.
	EventModeRequest
)

func (k EventKind) String() string {
	switch k {
	case EventIncoming:
		return "incoming"
	case EventEstablished:
		return "established"
	case EventFailed:
		return "failed"
	case EventEnded:
		return "ended"
	case EventFloor:
		return "floor"
	case EventHeard:
		return "heard"
	case EventRegistration:
		return "registration"
	case EventTransmit:
		return "transmit"
	case EventModeRequest:
		return "mode"
	}

	return "unknown"
}

// strandClock is the clock of the floor participant: the timers of the call's strand, on the agent's clock.
type strandClock struct {
	st    *ua.Strand
	clock transaction.Clock
}

func (s strandClock) AfterFunc(d time.Duration, f func()) floor.Timer { return s.st.AfterFunc(d, f) }

func (s strandClock) Now() time.Time { return s.clock.Now() }
