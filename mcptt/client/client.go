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

type Config struct {
	Agent *ua.Agent

	Originate func(req *sip.Request) error

	IMPU     sip.URI
	MCPTTID  sip.URI
	ClientID string
	Token    string

	Contact          func() sip.Address
	Registrar        sip.URI
	ParticipatingPSI sip.URI

	Media            media.Network
	Addr             netip.Addr
	MinPort, MaxPort uint16

	Floor                    floor.ParticipantTimers
	ImplicitRequest, Granted bool
	Priority                 uint8
	Manual                   bool

	OnEvent func(Event)
	Logger  *slog.Logger
}

type Client struct {
	cfg   Config
	clock transaction.Clock
	st    *ua.Strand

	mu     sync.Mutex
	next   uint16
	ssrcs  map[uint32]bool
	groups map[string]*Call
	reg    registration
	aff    *publication
}

var (
	ErrConfig = errors.New("client: configuration")
	ErrInCall = errors.New("client: a call of the group already")
)

// RFC 3261 section 19.1.4
func groupKey(u sip.URI) string {
	if u.User == "" && u.Host == "" {
		return ""
	}

	return u.Scheme + ":" + u.User + "@" + strings.ToLower(u.Host)
}

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

func (c *Client) unclaim(call *Call) {
	k := groupKey(call.group)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.groups[k] == call {
		delete(c.groups, k)
	}
}

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

// TS 24.379 clause 7.2.1
func Features() sip.FeatureSet {
	return sip.FeatureSet{sip.NewFeature(identity.FeatureTag), sip.NewFeature("+g.3gpp.icsi-ref", identity.ICSI)}
}

func FixedContact(a sip.Address) func() sip.Address { return func() sip.Address { return a } }

func (c *Client) contact() sip.Address { return withFeatures(c.cfg.Contact()) }

func withFeatures(a sip.Address) sip.Address {
	a = a.Clone()

	have, _ := a.Features()
	for _, f := range Features() {
		if have.Has(f.Tag) {
			continue
		}

		p, _ := f.Param()
		a.Params = append(a.Params, p)
	}

	return a
}

// TS 24.379 clause 10.1.1.2.1.1
func AcceptContact() []string {
	var out []string

	for _, f := range Features() {
		v, _ := sip.CallerPreference{Features: sip.FeatureSet{f}, Require: true, Explicit: true}.Value()
		out = append(out, v)
	}

	return out
}

func (c *Client) originate(req *sip.Request) error {
	if !req.Header.Has("P-Preferred-Service") {
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

const portsPerCall = 4

var ErrNoPort = errors.New("client: no port left")

func (c *Client) listen(hs ...func([]byte, netip.AddrPort)) ([]media.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	span := int(c.cfg.MaxPort-c.cfg.MinPort+1) / portsPerCall

	for range span {
		p := c.next

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

// RFC 3550 section 8.1
// TS 24.380 clause 14.2.7
// TS 24.380 clause 14.3.8
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

type Event struct {
	Kind         EventKind
	Call         *Call
	Floor        floor.Notice
	SSRC         uint32
	Party        string
	Response     *sip.Response
	Err          error
	End          ua.End
	Transmitting bool
	Mode         int
}

type EventKind int

const (
	EventIncoming EventKind = iota + 1
	EventEstablished
	EventFailed
	EventEnded
	EventFloor
	EventHeard
	EventRegistration
	EventTransmit
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

type strandClock struct {
	st    *ua.Strand
	clock transaction.Clock
}

func (s strandClock) AfterFunc(d time.Duration, f func()) floor.Timer { return s.st.AfterFunc(d, f) }

func (s strandClock) Now() time.Time { return s.clock.Now() }
