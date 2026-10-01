package transaction_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

func TestProvisionalBeforeFinal(t *testing.T) {
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			h := newHarness(t)

			for range 100 {
				_, rs, sent := h.request("INVITE", tr)

				h.reply(sent, 180)
				h.reply(sent, 200)

				wantCode(t, rs.Next(), 180)
				wantCode(t, rs.Next(), 200)
			}
		})
	}
}

type relay struct {
	t      *testing.T
	l      *transaction.Layer
	callee netip.AddrPort

	mu       sync.Mutex
	branches map[*transaction.ServerTransaction]*transaction.ClientTransaction
}

func (r *relay) HandleRequest(stx *transaction.ServerTransaction, req *sip.Request) {
	out := req.Clone()
	out.Flow = sip.Flow{Transport: req.Flow.Transport, Local: req.Flow.Local, Remote: r.callee}

	via := sip.Via{Protocol: "SIP/2.0", Transport: out.Flow.Transport, Host: sip.FormatHost(out.Flow.Local.Addr()), Port: out.Flow.Local.Port()}
	via.Params.Set("branch", sip.NewBranch())
	out.Header.Prepend("Via", via.String())

	ctx, err := r.l.Request(out, branch{r: r, stx: stx})
	if err != nil {
		r.t.Error(err)
		return
	}

	r.mu.Lock()
	r.branches[stx] = ctx
	r.mu.Unlock()
}

func (r *relay) HandleCancel(stx *transaction.ServerTransaction, _ *sip.Request) {
	r.mu.Lock()
	ctx := r.branches[stx]
	r.mu.Unlock()

	if err := ctx.Cancel(); err != nil {
		r.t.Error(err)
	}
}

func (r *relay) HandleAck(ack *sip.Request) {
	r.t.Errorf("unexpected ACK %q", ack.StartLine())
}

func (r *relay) HandleTransactionError(_ *transaction.ServerTransaction, err error) {
	if !errors.Is(err, transaction.ErrClosed) {
		r.t.Error(err)
	}
}

type branch struct {
	r   *relay
	stx *transaction.ServerTransaction
}

func (b branch) HandleResponse(res *sip.Response) {
	if res.StatusCode == 100 {
		return
	}

	up := res.Clone()
	up.Header.PopFirst("Via")

	if err := b.stx.Respond(up); err != nil {
		b.r.t.Error(err)
	}
}

func (b branch) HandleError(err error) {
	if !errors.Is(err, transaction.ErrClosed) {
		b.r.t.Error(err)
	}
}

func TestCancelRelaysDownstreamFinal(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		for _, tr := range transports {
			t.Run(string(tr), func(t *testing.T) {
				caller := siptest.NewSocket(t, netip.AddrPortFrom(addr, 0))
				callee := siptest.NewSocket(t, netip.AddrPortFrom(addr, 0))

				r := &relay{t: t, callee: callee.Addr(), branches: make(map[*transaction.ServerTransaction]*transaction.ClientTransaction)}
				r.l, _ = siptest.NewLayer(t, transaction.Config{Handler: r})
				proxy := siptest.ListenLayer(t, r.l, addr)

				invite := siptest.NewRequest("INVITE", "sip:callee@"+callee.Addr().String(), tr, caller.Addr())
				caller.Send(tr, proxy, invite)
				wantSocketResponse(t, caller, 100)

				fwd, _ := callee.RecvRequest()
				if n := len(fwd.Header.Values("Via")); n != 2 {
					t.Fatalf("forwarded INVITE has %d Via fields", n)
				}

				ringing := sip.NewResponse(fwd, 180, "")
				_ = ringing.Header.SetToTag("callee")
				callee.Send(tr, proxy, ringing)
				wantSocketResponse(t, caller, 180)

				cancel, _ := sip.NewCancel(invite)
				caller.Send(tr, proxy, cancel)

				if res := wantSocketResponse(t, caller, 200); res.Header.Get("CSeq") != "1 CANCEL" {
					t.Fatalf("200 for %s", res.Header.Get("CSeq"))
				}

				down, _ := callee.RecvRequest()
				if down.Method != "CANCEL" || topVia(t, down).Branch() != topVia(t, fwd).Branch() {
					t.Fatalf("callee got %q", down.StartLine())
				}

				callee.Send(tr, proxy, sip.NewResponse(down, 200, ""))

				terminated := sip.NewResponse(fwd, 487, "")
				_ = terminated.Header.SetToTag("callee")
				callee.Send(tr, proxy, terminated)

				if ack, _ := callee.RecvRequest(); ack.Method != "ACK" {
					t.Fatalf("callee got %q, want ACK", ack.StartLine())
				}

				relayed := wantSocketResponse(t, caller, 487)
				if n := len(relayed.Header.Values("Via")); n != 1 {
					t.Fatalf("relayed 487 has %d Via fields", n)
				}

				ack, _ := sip.NewAck(invite, relayed)
				caller.Send(tr, proxy, ack)
				caller.RecvNone(quiet)
				callee.RecvNone(quiet)
			})
		}
	})
}

func wantSocketResponse(t *testing.T, s *siptest.Socket, code int) *sip.Response {
	t.Helper()

	res, _ := s.RecvResponse()
	if res.StatusCode != code {
		t.Fatalf("got %q, want %d", res.StartLine(), code)
	}

	return res
}

func TestCloseEndsTransactions(t *testing.T) {
	h := newHarness(t)
	ctx, rs, _ := h.request("INVITE", sip.UDP)
	stx, _ := h.serve("OPTIONS", sip.UDP)

	if err := h.l.Close(); err != nil {
		t.Fatal(err)
	}

	wantTerminated(t, ctx, transaction.ErrClosed)
	wantTerminated(t, stx, transaction.ErrClosed)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrClosed) {
		t.Errorf("client error %v", err)
	}

	if e := h.tu.NextError(); e.Tx != stx || !errors.Is(e.Err, transaction.ErrClosed) {
		t.Errorf("HandleTransactionError(%p, %v)", e.Tx, e.Err)
	}

	h.advance(64 * t1)
	rs.None(quiet)
	h.tu.None(quiet)
	h.peer.RecvNone(quiet)

	if _, err := h.l.Request(h.newRequest("OPTIONS", sip.UDP), nil); !errors.Is(err, transaction.ErrClosed) {
		t.Errorf("Request after Close: %v", err)
	}

	if err := h.l.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func forEachFamily(t *testing.T, f func(t *testing.T, addr netip.Addr)) {
	for _, addr := range []netip.Addr{loopback, netip.MustParseAddr("::1")} {
		t.Run(map[bool]string{true: "IPv4", false: "IPv6"}[addr.Is4()], func(t *testing.T) {
			if addr.Is6() {
				var lc net.ListenConfig

				l, err := lc.ListenPacket(context.Background(), "udp6", "[::1]:0")
				if err != nil {
					t.Skip("no IPv6 loopback")
				}

				_ = l.Close()
			}

			f(t, addr)
		})
	}
}
