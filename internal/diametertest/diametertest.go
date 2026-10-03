// Package diametertest builds Diameter nodes for tests: a passive node that
// accepts one peer over TCP, an active node that connects to one, and an
// in-memory loop that answers requests without a network.
package diametertest

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
)

const openTimeout = 10 * time.Second

// Config describes a test node and the one peer it talks to, over TCP.
type Config struct {
	Identity diameter.Identity
	Peer     diameter.Peer
	Handler  diameter.Handler

	OnPeerStateChange func(diameter.PeerStatus)
	Logger            *slog.Logger
}

func newNode(t testing.TB, cfg Config) *diameter.Node {
	t.Helper()

	if cfg.Handler == nil {
		cfg.Handler = diameter.NewMux()
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	cfg.Peer.Transport = diameter.TransportTCP

	node, err := diameter.New(diameter.Config{
		Identity:          cfg.Identity,
		Handler:           cfg.Handler,
		OnPeerStateChange: cfg.OnPeerStateChange,
		Logger:            cfg.Logger,
	})
	if err != nil {
		t.Fatalf("diametertest: %v", err)
	}

	if err := node.SetPeers([]diameter.Peer{cfg.Peer}); err != nil {
		t.Fatalf("diametertest: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	return node
}

// Listen starts a passive node on the first of its HostIPAddresses, port 0,
// and returns it with the address it accepts the peer on.
func Listen(t testing.TB, cfg Config) (*diameter.Node, netip.AddrPort) {
	t.Helper()

	addr := cfg.Identity.HostIPAddresses[0]

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", netip.AddrPortFrom(addr, 0).String())
	if err != nil {
		t.Fatalf("diametertest: listen: %v", err)
	}

	cfg.Peer.Passive = true
	if len(cfg.Peer.Addresses) == 0 {
		cfg.Peer.Addresses = []netip.Addr{addr}
	}

	node := newNode(t, cfg)

	go func() { _ = node.Serve(diameter.NewTCPListener(ln.(*net.TCPListener))) }()

	return node, ln.Addr().(*net.TCPAddr).AddrPort()
}

// Dial starts an active node, connects it to its peer at addr and waits for
// the connection to open.
func Dial(t testing.TB, cfg Config, addr netip.AddrPort) *diameter.Node {
	t.Helper()

	cfg.Peer.Addresses = []netip.Addr{addr.Addr()}
	cfg.Peer.Port = addr.Port()

	node := newNode(t, cfg)
	WaitOpen(t, node, cfg.Peer.ID)

	return node
}

// WaitOpen waits for the node's peer to reach the open state.
func WaitOpen(t testing.TB, node *diameter.Node, peerID string) {
	t.Helper()

	deadline := time.Now().Add(openTimeout)

	for p, _ := node.Peer(peerID); p.State != diameter.PeerOpen; p, _ = node.Peer(peerID) {
		if time.Now().After(deadline) {
			t.Fatalf("diametertest: timed out waiting for peer %s to open", peerID)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// Loop stands in for a node's client side: Do encodes the request, hands it
// to Handler and decodes the answer, with no connection. A nil answer means
// the peer never answers, so Do waits for the context.
type Loop struct {
	Local   diameter.Identity
	Handler func(ctx context.Context, req *diameter.Message) *diameter.Message

	down atomic.Bool
	seq  atomic.Uint64
}

func (l *Loop) Identity() diameter.Identity {
	return l.Local
}

func (l *Loop) NewSessionID() string {
	return fmt.Sprintf("%s;%d", l.Local.OriginHost, l.seq.Add(1))
}

// SetDown makes Do fail as it does when the peer is not connected.
func (l *Loop) SetDown(down bool) {
	l.down.Store(down)
}

func (l *Loop) Do(ctx context.Context, _ string, req *diameter.Message, _ ...diameter.DoOption) (*diameter.Message, error) {
	if l.down.Load() {
		return nil, diameter.ErrNotConnected
	}

	m := *req
	m.Flags |= diameter.FlagRequest

	in, err := roundTrip(&m)
	if err != nil {
		return nil, err
	}

	ans := l.Handler(ctx, in)
	if ans == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	return roundTrip(ans)
}

func roundTrip(m *diameter.Message) (*diameter.Message, error) {
	b, err := m.Marshal()
	if err != nil {
		return nil, err
	}

	return diameter.Unmarshal(b)
}
