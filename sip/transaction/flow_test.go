package transaction_test

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

func TestResponseFlow(t *testing.T) {
	var route atomic.Pointer[sip.Flow]

	h := newHarnessConfig(t, transaction.Config{
		ResponseFlow: func(*sip.Request, *sip.Response) (sip.Flow, bool, error) {
			if f := route.Load(); f != nil {
				return *f, true, nil
			}

			return sip.Flow{}, false, nil
		},
	})

	pc := siptest.ListenLayer(t, h.l, loopback)
	us := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	route.Store(&sip.Flow{Transport: sip.UDP, Local: pc, Remote: us.Addr()})

	wantFrom := func(code int) {
		t.Helper()

		res, f := us.RecvResponse()
		if res.StatusCode != code || f.Remote != pc {
			t.Fatalf("got %q from %s, want %d from %s", res.StartLine(), f.Remote, code, pc)
		}
	}

	h.peerRequest("INVITE", sip.UDP)
	wantFrom(100)

	tx := h.tu.NextRequest().Tx
	h.respond(tx, 486)
	wantFrom(486)

	h.advance(t1)
	wantFrom(486)

	bad := h.peerRequest("OPTIONS", sip.UDP)
	bad.Header.Set("CSeq", "1 BYE")
	h.peer.Send(sip.UDP, h.local, bad)
	wantFrom(400)

	h.peer.RecvNone(quiet)
}

func TestFailedTryingStillDelivers(t *testing.T) {
	nowhere := sip.Flow{Transport: sip.UDP, Local: netip.AddrPortFrom(loopback, 1), Remote: netip.AddrPortFrom(loopback, 2)}
	h := newHarnessConfig(t, transaction.Config{
		ResponseFlow: func(*sip.Request, *sip.Response) (sip.Flow, bool, error) { return nowhere, true, nil },
	})

	h.peerRequest("INVITE", sip.UDP)
	tx := h.tu.NextRequest().Tx
	h.peer.RecvNone(quiet)

	if err := tx.Respond(sip.NewResponse(tx.Request(), 180, "")); err != nil {
		t.Fatal(err)
	}

	for _, what := range []string{"100", "180"} {
		if e := h.tu.NextError(); e.Tx != tx || !errors.Is(e.Err, transport.ErrNoListener) {
			t.Fatalf("%s: HandleTransactionError(%p, %v)", what, e.Tx, e.Err)
		}
	}

	wantState(t, tx, transaction.Proceeding)
	h.tu.None(quiet)
}

func TestStatelessAckAbsorbed(t *testing.T) {
	h := newHarness(t)

	invite := siptest.NewRequest("INVITE", "sip:pcscf@"+h.local.String(), sip.UDP, h.peer.Addr())
	invite.Body = []byte("v=0")
	invite.Header.Set("Content-Length", "3")
	h.peer.Send(sip.UDP, h.local, invite)

	rejected := h.wantResponse(400)

	ack, err := sip.NewAck(invite, rejected)
	if err != nil {
		t.Fatal(err)
	}

	h.peer.Send(sip.UDP, h.local, ack)

	other := ack.Clone()
	other.Header.Set("To", "<sip:pcscf@x>;tag=uas")
	h.peer.Send(sip.UDP, h.local, other)

	if got := h.tu.NextAck(); got.Header.Get("To") != other.Header.Get("To") {
		t.Fatalf("HandleAck got the ACK to the stateless 400: %q", got.Header.Get("To"))
	}
}

type ordered struct {
	tx atomic.Pointer[transaction.ClientTransaction]

	mu    sync.Mutex
	seen  []int
	early bool
	err   error
}

func (o *ordered) HandleResponse(res *sip.Response) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.seen = append(o.seen, res.StatusCode)

	select {
	case <-o.tx.Load().Done():
		o.early = true
	default:
	}
}

func (o *ordered) HandleError(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.err = err
}

func TestDoneAfterLastCallback(t *testing.T) {
	for _, method := range []string{"INVITE", "REGISTER"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			o := &ordered{}

			tx, err := h.l.Request(h.newRequest(method, sip.TCP), o)
			if err != nil {
				t.Fatal(err)
			}

			o.tx.Store(tx)

			sent, _ := h.peer.RecvRequest()
			h.reply(sent, 403)

			select {
			case <-tx.Done():
			case <-time.After(siptest.Timeout):
				t.Fatal("transaction did not end")
			}

			o.mu.Lock()
			defer o.mu.Unlock()

			if o.early || len(o.seen) != 1 || o.seen[0] != 403 {
				t.Fatalf("Done closed before the final response was delivered (responses %v)", o.seen)
			}
		})
	}
}

func TestClientRequestIsACopy(t *testing.T) {
	h := newHarness(t)
	tx, _, _ := h.request("INVITE", sip.UDP)

	tx.Request().Header.Set("Call-ID", "changed")

	if tx.Request().Header.CallID() == "changed" {
		t.Fatal("Request() exposes the transaction's request")
	}
}

func TestSendOnFlow(t *testing.T) {
	h := newHarness(t)

	req := h.newRequest("MESSAGE", sip.UDP)
	pad(req, 1400)

	if err := h.l.SendOnFlow(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	if _, f := h.peer.RecvRequest(); f.Transport != sip.UDP {
		t.Fatalf("SendOnFlow switched to %s", f.Transport)
	}
}

func TestResponseFlowPerResponse(t *testing.T) {
	pcs := make(chan netip.AddrPort, 2)
	dropped := errors.New("dropped")

	var h *harness

	h = newHarnessConfig(t, transaction.Config{
		ResponseFlow: func(req *sip.Request, res *sip.Response) (sip.Flow, bool, error) {
			switch res.StatusCode {
			case 100:
				return sip.Flow{}, false, nil
			case 503:
				return sip.Flow{}, false, dropped
			}

			pc := <-pcs
			pcs <- pc

			return sip.Flow{Transport: sip.UDP, Local: pc, Remote: h.peer.Addr()}, true, nil
		},
	})

	first, second := siptest.ListenLayer(t, h.l, loopback), siptest.ListenLayer(t, h.l, loopback)

	h.peerRequest("INVITE", sip.UDP)
	h.wantResponse(100)

	tx := h.tu.NextRequest().Tx

	pcs <- first

	if err := tx.Respond(sip.NewResponse(tx.Request(), 180, "")); err != nil {
		t.Fatal(err)
	}

	if res, f := h.peer.RecvResponse(); res.StatusCode != 180 || f.Remote != first {
		t.Fatalf("got %q from %s, want 180 from %s", res.StartLine(), f.Remote, first)
	}

	<-pcs

	pcs <- second

	h.respond(tx, 486)

	if res, f := h.peer.RecvResponse(); res.StatusCode != 486 || f.Remote != second {
		t.Fatalf("got %q from %s, want 486 from %s", res.StartLine(), f.Remote, second)
	}

	h.peerRequest("OPTIONS", sip.UDP)
	options := h.tu.NextRequest()

	if err := options.Tx.Respond(sip.NewResponse(options.Req, 503, "")); err != nil {
		t.Fatal(err)
	}

	if e := h.tu.NextError(); e.Tx != options.Tx || !errors.Is(e.Err, dropped) {
		t.Fatalf("HandleTransactionError(%p, %v), want the hook's error", e.Tx, e.Err)
	}

	h.peer.RecvNone(quiet)
}

// The local addresses, read without a lock as messages come and go, grow as the layer listens on more.
func TestListenWhileMatching(t *testing.T) {
	l, _ := siptest.NewLayer(t, transaction.Config{})

	const n = 8

	bound := make(chan netip.AddrPort, n)

	go func() {
		defer close(bound)

		for range n {
			a, err := l.Listen(context.Background(), netip.AddrPortFrom(loopback, 0))
			if err != nil {
				t.Error(err)
				return
			}

			bound <- a
		}
	}()

	var seen []netip.AddrPort

	for a := range bound {
		seen = append(seen, a)

		for _, a := range seen {
			if !l.IsLocal(a.Addr().String(), a.Port()) {
				t.Fatalf("%s not local once listened on", a)
			}
		}
	}

	if l.IsLocal(loopback.String(), 1) {
		t.Fatal("an address not listened on is local")
	}
}

// A listener of the layer that failed for good is told to its user, its address no longer the layer's; the transport's
// own report is the layer's to take.
func TestListenerFailure(t *testing.T) {
	type failure struct {
		local netip.AddrPort
		err   error
	}

	failed := make(chan failure, 1)
	l, _ := siptest.NewLayer(t, transaction.Config{
		OnListenerFailed: func(local netip.AddrPort, err error) { failed <- failure{local, err} },
	})
	local := siptest.ListenLayer(t, l, loopback)

	rcs, err := l.Sockets(local)
	if err != nil {
		t.Fatal(err)
	}

	// Linux: a listening socket shut down is no longer listening; accept fails with EINVAL.
	var shut error
	if err := rcs[1].Control(func(fd uintptr) { shut = syscall.Shutdown(int(fd), syscall.SHUT_RD) }); err != nil || shut != nil {
		t.Fatalf("shutdown: %v, %v", err, shut)
	}

	select {
	case f := <-failed:
		if f.local != local || !errors.Is(f.err, syscall.EINVAL) {
			t.Fatalf("failure of %s: %v; want %s, EINVAL", f.local, f.err, local)
		}
	case <-time.After(siptest.Timeout):
		t.Fatal("no listener failure")
	}

	if l.IsLocal(local.Addr().String(), local.Port()) {
		t.Fatalf("%s local once its listener failed", local)
	}

	if _, err := l.Listen(context.Background(), local); err != nil || !l.IsLocal(local.Addr().String(), local.Port()) {
		t.Fatalf("listen again on %s: %v", local, err)
	}

	defer func() {
		if recover() == nil {
			t.Error("New took a Transport.OnListenerFailed")
		}
	}()

	transaction.New(transaction.Config{
		Handler:   siptest.NewTU(t),
		Transport: transport.Config{OnListenerFailed: func(netip.AddrPort, error) {}},
	})
}
