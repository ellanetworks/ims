package transaction_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

var (
	servedA = netip.MustParseAddrPort("127.0.0.1:5070")
	servedB = netip.MustParseAddrPort("127.0.0.1:5080")
)

type memoryHarness struct {
	t     *testing.T
	clock *siptest.Clock
	l     *transaction.Layer
	tu    *siptest.TU
	peer  *siptest.Peer
}

func newMemoryHarness(t *testing.T) *memoryHarness {
	t.Helper()

	clock := siptest.NewClock()
	l, tu := siptest.NewLayer(t, transaction.Config{Clock: clock})

	for _, a := range []netip.AddrPort{servedA, servedB} {
		if err := l.Serve(a); err != nil {
			t.Fatal(err)
		}
	}

	return &memoryHarness{t: t, clock: clock, l: l, tu: tu, peer: siptest.NewPeer(t, l, netip.MustParseAddrPort("127.0.0.1:6000"))}
}

func newFlowRequest(method string, tr sip.Transport, from, to netip.AddrPort) *sip.Request {
	req := siptest.NewRequest(method, "sip:ue@"+to.String(), tr, from)
	req.Flow = sip.Flow{Transport: tr, Local: from, Remote: to}

	return req
}

func (h *memoryHarness) request(method string, tr sip.Transport, from, to netip.AddrPort) (*transaction.ClientTransaction, *siptest.Responses) {
	h.t.Helper()

	return h.send(newFlowRequest(method, tr, from, to))
}

func (h *memoryHarness) send(req *sip.Request) (*transaction.ClientTransaction, *siptest.Responses) {
	h.t.Helper()

	rs := siptest.NewResponses(h.t)

	tx, err := h.l.Request(req, rs)
	if err != nil {
		h.t.Fatal(err)
	}

	return tx, rs
}

func TestServedAddressesTalkInMemory(t *testing.T) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			h := newMemoryHarness(t)

			client, rs := h.request("INVITE", tr, servedA, servedB)

			got := h.tu.NextRequest()
			if f := got.Req.Flow; f != (sip.Flow{Transport: tr, Local: servedB, Remote: servedA}) {
				t.Fatalf("request on %s", f)
			}

			wantCode(t, rs.Next(), 100)

			h.clock.Advance(10 * t1)
			h.tu.None(quiet)

			for _, code := range []int{180, 183, 200} {
				if err := got.Tx.Respond(sip.NewResponse(got.Req, code, "")); err != nil {
					t.Fatal(err)
				}
			}

			for _, code := range []int{180, 183, 200} {
				wantCode(t, rs.Next(), code)
			}

			wantState(t, client, transaction.Accepted)

			ack := newFlowRequest("ACK", tr, servedA, servedB)
			ack.Header.Set("Call-ID", got.Req.Header.CallID())
			ack.Header.Set("CSeq", "1 ACK")

			if err := h.l.SendAck(context.Background(), ack); err != nil {
				t.Fatal(err)
			}

			if a := h.tu.NextAck(); a.Flow.Local != servedB {
				t.Fatalf("ACK on %s", a.Flow)
			}
		})
	}
}

func TestServedAddressCancel(t *testing.T) {
	h := newMemoryHarness(t)

	client, rs := h.request("INVITE", sip.UDP, servedA, servedB)
	got := h.tu.NextRequest()

	wantCode(t, rs.Next(), 100)

	if err := client.Cancel(); err != nil {
		t.Fatal(err)
	}

	cancel := h.tu.NextCancel()
	if cancel.Tx != got.Tx {
		t.Fatal("CANCEL matched another transaction")
	}

	if err := got.Tx.Respond(sip.NewResponse(got.Req, 487, "")); err != nil {
		t.Fatal(err)
	}

	wantCode(t, rs.Next(), 487)

	h.clock.Advance(4 * t1)
	rs.None(quiet)
	wantState(t, got.Tx, transaction.Terminated)
}

func TestNonInviteInMemoryIsReliable(t *testing.T) {
	h := newMemoryHarness(t)

	client, rs := h.request("OPTIONS", sip.UDP, servedA, servedB)
	got := h.tu.NextRequest()

	if err := got.Tx.Respond(sip.NewResponse(got.Req, 200, "")); err != nil {
		t.Fatal(err)
	}

	wantCode(t, rs.Next(), 200)

	wantTerminated(t, client, nil)
	wantTerminated(t, got.Tx, nil)
}

func TestPeerTalksToServedAddress(t *testing.T) {
	h := newMemoryHarness(t)

	req := siptest.NewRequest("MESSAGE", "sip:x@"+servedA.String(), sip.UDP, h.peer.Addr())
	h.peer.Send(sip.UDP, servedA, req)

	got := h.tu.NextRequest()
	if got.Req.Flow.Remote != h.peer.Addr() || got.Req.Flow.Local != servedA {
		t.Fatalf("request on %s", got.Req.Flow)
	}

	if err := got.Tx.Respond(sip.NewResponse(got.Req, 202, "")); err != nil {
		t.Fatal(err)
	}

	res, f := h.peer.RecvResponse()
	if res.StatusCode != 202 || f.Remote != servedA {
		t.Fatalf("peer got %q on %s", res.StartLine(), f)
	}

	large := newFlowRequest("MESSAGE", sip.UDP, servedA, h.peer.Addr())
	pad(large, 2*transport.MaxUDPRequest)

	_, rs := h.send(large)
	out, f := h.peer.RecvRequest()

	if f.Transport != sip.UDP || topVia(t, out).Transport != sip.UDP {
		t.Fatalf("request on %s with Via %s", f, topVia(t, out))
	}

	reply := sip.NewResponse(out, 200, "")
	_ = reply.Header.SetToTag("peer")
	h.peer.Send(sip.UDP, servedA, reply)
	wantCode(t, rs.Next(), 200)
}

func TestServedAddressCannotReachTheNetwork(t *testing.T) {
	h := newMemoryHarness(t)
	remote := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	_, rs := h.request("OPTIONS", sip.UDP, servedA, remote.Addr())

	if err := rs.NextError(); !errors.Is(err, transport.ErrNoListener) {
		t.Errorf("error %v, want ErrNoListener", err)
	}

	remote.RecvNone(quiet)
}

func TestServeAndAttachConflict(t *testing.T) {
	h := newMemoryHarness(t)

	if _, err := h.l.Attach(servedA, func(sip.Message) {}); !errors.Is(err, transaction.ErrInUse) {
		t.Errorf("Attach on a served address: %v, want ErrInUse", err)
	}

	if err := h.l.Serve(h.peer.Addr()); !errors.Is(err, transaction.ErrInUse) {
		t.Errorf("Serve on a peer's address: %v, want ErrInUse", err)
	}

	unknown := netip.MustParseAddrPort("127.0.0.1:7000")
	m := siptest.NewRequest("OPTIONS", "sip:x@"+servedA.String(), sip.UDP, unknown)
	m.Flow = sip.Flow{Transport: sip.UDP, Local: unknown, Remote: servedA}

	if err := h.l.SendOnFlow(context.Background(), m); !errors.Is(err, transport.ErrNoListener) {
		t.Errorf("send from an unknown address: %v, want ErrNoListener", err)
	}
}
