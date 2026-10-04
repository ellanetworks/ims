package pcrftest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/diametertest"
)

const imsPeer = "ims"

type Config struct {
	Host  string
	Realm string

	Address netip.Addr

	IMSHost  string
	IMSRealm string

	UEs []netip.Addr

	Logger *slog.Logger
}

type Request struct {
	SessionID string
	AAR       *rx.AARequest
	STR       *rx.SessionTerminationRequest
}

func (r Request) String() string {
	switch {
	case r.AAR != nil:
		return fmt.Sprintf("AAR %s %+v", r.SessionID, *r.AAR)
	case r.STR != nil:
		return fmt.Sprintf("STR %s %+v", r.SessionID, *r.STR)
	}

	return "empty request"
}

type PCRF struct {
	cfg  Config
	node *diameter.Node
	addr netip.AddrPort

	requests chan Request

	mu       sync.Mutex
	dropped  int
	refuse   func(rx.AARequest) *tgpp.Result
	answer   func(rx.AARequest) rx.AAAnswer
	hold     chan struct{}
	sessions map[string]bool
}

func New(t testing.TB, cfg Config) *PCRF {
	t.Helper()

	if !cfg.Address.IsValid() {
		cfg.Address = netip.MustParseAddr("127.0.0.1")
	}

	if cfg.Host == "" {
		cfg.Host = "pcrf." + cfg.Realm
	}

	if cfg.IMSRealm == "" {
		cfg.IMSRealm = cfg.Realm
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	p := &PCRF{cfg: cfg, requests: make(chan Request, 1024), sessions: make(map[string]bool)}

	mux := diameter.NewMux()
	mux.Handle(rx.ApplicationID, rx.CommandAA, diameter.HandlerFunc(p.aa))
	mux.Handle(rx.ApplicationID, rx.CommandSessionTermination, diameter.HandlerFunc(p.sessionTermination))

	p.node, p.addr = diametertest.Listen(t, diametertest.Config{
		Identity: diameter.Identity{
			OriginHost:      cfg.Host,
			OriginRealm:     cfg.Realm,
			HostIPAddresses: []netip.Addr{cfg.Address},
			ProductName:     "pcrftest",
		},
		Peer: diameter.Peer{
			ID:           imsPeer,
			Host:         cfg.IMSHost,
			Applications: []diameter.Application{{ID: rx.ApplicationID, VendorID: tgpp.VendorID}},
		},
		Handler: mux,
		Logger:  cfg.Logger,
	})

	t.Cleanup(func() {
		if n := p.Dropped(); n > 0 {
			t.Errorf("pcrftest: %d Rx requests dropped from the full request channel", n)
		}
	})

	return p
}

func (p *PCRF) Host() string {
	return p.cfg.Host
}

func (p *PCRF) Realm() string {
	return p.cfg.Realm
}

func (p *PCRF) Addr() netip.AddrPort {
	return p.addr
}

func (p *PCRF) Requests() <-chan Request {
	return p.requests
}

func (p *PCRF) Dropped() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.dropped
}

func (p *PCRF) record(r Request) {
	select {
	case p.requests <- r:
	default:
		p.mu.Lock()
		p.dropped++
		p.mu.Unlock()
	}
}

func (p *PCRF) Next(t testing.TB) Request {
	t.Helper()

	select {
	case r := <-p.requests:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("pcrftest: timed out waiting for an Rx request")
	}

	return Request{}
}

// TS 29.214 §4.4.1, §4.4.2, §5.4. Like the Open5GS PCRF, every AA-Request must bind to a UE and carry
// Flow-Descriptions its SMF accepts.
func (p *PCRF) aa(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	aar, err := rx.ParseAARequest(req)
	if err != nil {
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	session := tgpp.ParseEnvelope(req).SessionID
	p.record(Request{SessionID: session, AAR: &aar})

	fail := func(r tgpp.Result) *diameter.Message {
		return must(rx.NewAAErrorAnswer(req, c.LocalIdentity(), rx.AAError{ResultError: rx.ResultError{Result: r}}))
	}

	ue, bound := p.binding(aar)
	if !bound {
		return fail(tgpp.Result{Code: tgpp.ResultIPCANSessionNotAvailable, Experimental: true, VendorID: tgpp.VendorID})
	}

	if err := checkFlows(aar, ue); err != nil {
		p.cfg.Logger.Warn("pcrftest: refused Flow-Description", slog.String("session", session), slog.Any("error", err))
		return fail(tgpp.Result{Code: diameter.ResultInvalidAVPValue})
	}

	p.mu.Lock()
	known := p.sessions[session]
	hold := p.hold
	p.mu.Unlock()

	if aar.RequestType != nil && *aar.RequestType == rx.RequestUpdate && !known {
		return fail(tgpp.Result{Code: diameter.ResultUnknownSessionID})
	}

	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil
		}
	}

	p.mu.Lock()
	refuse, answer := p.refuse, p.answer
	p.mu.Unlock()

	if refuse != nil {
		if r := refuse(aar); r != nil {
			return fail(*r)
		}
	}

	a := rx.AAAnswer{}
	if answer != nil {
		a = answer(aar)
	}

	a.Class = [][]byte{[]byte(session)}

	p.mu.Lock()
	p.sessions[session] = true
	p.mu.Unlock()

	return must(rx.NewAAAnswer(req, c.LocalIdentity(), a))
}

func (p *PCRF) binding(r rx.AARequest) (netip.Addr, bool) {
	ue := r.FramedIPAddress
	if !ue.IsValid() {
		ue = r.FramedIPv6Address
	}

	if !ue.IsValid() {
		return netip.Addr{}, false
	}

	if len(p.cfg.UEs) == 0 {
		return ue, true
	}

	for _, a := range p.cfg.UEs {
		if a == ue || ue.Is6() && a.Is6() && samePrefix64(a, ue) {
			return ue, true
		}
	}

	return netip.Addr{}, false
}

func samePrefix64(a, b netip.Addr) bool {
	pa, _ := a.Prefix(64)
	pb, _ := b.Prefix(64)

	return pa == pb
}

// The Flow-Description checks of the Open5GS PCRF and SMF (lib/proto/types.c, lib/ipfw/ogs-ipfw.c).
func checkFlows(r rx.AARequest, ue netip.Addr) error {
	for _, c := range r.MediaComponents {
		for _, s := range c.SubComponents {
			if len(s.FlowDescriptions) > 2 {
				return fmt.Errorf("media %d flow %d: %d Flow-Descriptions", c.Number, s.FlowNumber, len(s.FlowDescriptions))
			}

			for _, d := range s.FlowDescriptions {
				f, err := rx.ParseFlowDescription(d)
				if err != nil {
					return err
				}

				side := f.Destination
				if f.Direction == rx.FlowDirectionIn {
					side = f.Source
				}

				switch {
				case f.DestinationPort == 0 && f.Destination.IsValid():
					return fmt.Errorf("%q: no destination port", d)
				case !side.IsValid() || !side.Contains(ue):
					return fmt.Errorf("%q: the UE %s is not on its side of the flow", d, ue)
				}
			}
		}
	}

	return nil
}

func (p *PCRF) sessionTermination(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	str, err := rx.ParseSessionTerminationRequest(req)
	if err != nil {
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	session := tgpp.ParseEnvelope(req).SessionID
	p.record(Request{SessionID: session, STR: &str})

	p.mu.Lock()
	known := p.sessions[session]
	delete(p.sessions, session)
	p.mu.Unlock()

	if !known {
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0)
	}

	return must(rx.NewSessionTerminationAnswer(req, c.LocalIdentity(), rx.SessionTerminationAnswer{}))
}

// TS 29.214 §5.4
func (p *PCRF) RefuseMedia(r tgpp.Result) {
	p.RefuseWhen(func(aar rx.AARequest) *tgpp.Result {
		if Signalling(aar) {
			return nil
		}

		return &r
	})
}

func (p *PCRF) RefuseWhen(f func(rx.AARequest) *tgpp.Result) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.refuse = f
}

func (p *PCRF) AnswerWith(f func(rx.AARequest) rx.AAAnswer) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.answer = f
}

// HoldAA delays every AA-Answer until release is called.
func (p *PCRF) HoldAA() (release func()) {
	hold := make(chan struct{})

	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()

	var once sync.Once

	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.hold == hold {
				p.hold = nil
			}
			p.mu.Unlock()

			close(hold)
		})
	}
}

func Signalling(r rx.AARequest) bool {
	for _, c := range r.MediaComponents {
		if c.Type == nil || *c.Type != rx.MediaControl {
			return false
		}
	}

	return len(r.MediaComponents) > 0
}

func must(ans *diameter.Message, err error) *diameter.Message {
	if err != nil {
		panic(fmt.Sprintf("pcrftest: %v", err))
	}

	return ans
}

var ErrNotConnected = errors.New("pcrftest: the IMS is not connected")

func (p *PCRF) envelope(session string) tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        session,
		Origin:           p.node.Identity(),
		DestinationHost:  p.cfg.IMSHost,
		DestinationRealm: p.cfg.IMSRealm,
	}
}

func (p *PCRF) do(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
	if s, ok := p.node.Peer(imsPeer); !ok || s.State != diameter.PeerOpen {
		return nil, ErrNotConnected
	}

	return p.node.Do(ctx, imsPeer, req)
}

// TS 29.214 §4.4.6.1
func (p *PCRF) ASR(ctx context.Context, session string, cause rx.AbortCause) (rx.AbortSessionAnswer, error) {
	req, err := rx.NewAbortSessionRequest(p.envelope(session), rx.AbortSessionRequest{Cause: cause})
	if err != nil {
		return rx.AbortSessionAnswer{}, err
	}

	ans, err := p.do(ctx, req)
	if err != nil {
		return rx.AbortSessionAnswer{}, err
	}

	return rx.ParseAbortSessionAnswer(ans)
}

// TS 29.214 §4.4.6.3
func (p *PCRF) RAR(ctx context.Context, session string, actions ...rx.SpecificAction) (rx.ReAuthAnswer, error) {
	return p.ReAuth(ctx, session, rx.ReAuthRequest{SpecificActions: actions})
}

// TS 29.214 §4.4.6.2
func (p *PCRF) ReAuth(ctx context.Context, session string, r rx.ReAuthRequest) (rx.ReAuthAnswer, error) {
	req, err := rx.NewReAuthRequest(p.envelope(session), r)
	if err != nil {
		return rx.ReAuthAnswer{}, err
	}

	ans, err := p.do(ctx, req)
	if err != nil {
		return rx.ReAuthAnswer{}, err
	}

	return rx.ParseReAuthAnswer(ans)
}
