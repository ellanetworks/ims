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

	mu      sync.Mutex
	dropped int
	refusal *tgpp.Result
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

	p := &PCRF{cfg: cfg, requests: make(chan Request, 1024)}

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

func (p *PCRF) WaitConnected(t testing.TB) {
	t.Helper()

	diametertest.WaitOpen(t, p.node, imsPeer)
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

func (p *PCRF) aa(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	aar, err := rx.ParseAARequest(req)
	if err != nil {
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	session := tgpp.ParseEnvelope(req).SessionID
	p.record(Request{SessionID: session, AAR: &aar})

	p.mu.Lock()
	refusal := p.refusal
	p.mu.Unlock()

	if refusal != nil && !Signalling(aar) {
		return must(rx.NewAAErrorAnswer(req, c.LocalIdentity(), rx.AAError{ResultError: rx.ResultError{Result: *refusal}}))
	}

	return must(rx.NewAAAnswer(req, c.LocalIdentity(), rx.AAAnswer{Class: [][]byte{[]byte(session)}}))
}

func (p *PCRF) sessionTermination(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	str, err := rx.ParseSessionTerminationRequest(req)
	if err != nil {
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	p.record(Request{SessionID: tgpp.ParseEnvelope(req).SessionID, STR: &str})

	return must(rx.NewSessionTerminationAnswer(req, c.LocalIdentity(), rx.SessionTerminationAnswer{}))
}

// TS 29.214 §5.4
func (p *PCRF) RefuseMedia(r tgpp.Result) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.refusal = &r
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
