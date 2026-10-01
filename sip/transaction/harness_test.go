package transaction_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	t1    = transaction.DefaultT1
	t2    = transaction.DefaultT2
	t4    = transaction.DefaultT4
	quiet = 50 * time.Millisecond
)

var loopback = netip.MustParseAddr("127.0.0.1")

var transports = []sip.Transport{sip.UDP, sip.TCP}

type harness struct {
	t     *testing.T
	clock *siptest.Clock
	l     *transaction.Layer
	tu    *siptest.TU
	local netip.AddrPort
	peer  *siptest.Socket
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	clock := siptest.NewClock()
	l, tu := siptest.NewLayer(t, transaction.Config{Clock: clock})

	return &harness{
		t:     t,
		clock: clock,
		l:     l,
		tu:    tu,
		local: siptest.ListenLayer(t, l, loopback),
		peer:  siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
	}
}

func (h *harness) newRequest(method string, tr sip.Transport) *sip.Request {
	req := siptest.NewRequest(method, "sip:ue@"+h.peer.Addr().String(), tr, h.local)
	req.Flow = sip.Flow{Transport: tr, Local: h.local, Remote: h.peer.Addr()}

	return req
}

func (h *harness) request(method string, tr sip.Transport) (*transaction.ClientTransaction, *siptest.Responses, *sip.Request) {
	h.t.Helper()

	return h.send(h.newRequest(method, tr))
}

func (h *harness) send(req *sip.Request) (*transaction.ClientTransaction, *siptest.Responses, *sip.Request) {
	h.t.Helper()

	rs := siptest.NewResponses(h.t)

	tx, err := h.l.Request(context.Background(), req, rs)
	if err != nil {
		h.t.Fatal(err)
	}

	got, f := h.peer.RecvRequest()
	if f.Transport != tx.Request().Flow.Transport {
		h.t.Fatalf("%s received over %s, sent over %s", got.Method, f.Transport, tx.Request().Flow.Transport)
	}

	return tx, rs, got
}

func (h *harness) reply(req *sip.Request, code int) *sip.Response {
	h.t.Helper()

	res := sip.NewResponse(req, code, "")
	if code > 100 {
		_ = res.Header.SetToTag("peer")
	}

	h.peer.Send(req.Flow.Transport, h.local, res)

	return res
}

func (h *harness) peerRequest(method string, tr sip.Transport) *sip.Request {
	req := siptest.NewRequest(method, "sip:pcscf@"+h.local.String(), tr, h.peer.Addr())
	h.peer.Send(tr, h.local, req)

	return req
}

func (h *harness) serve(method string, tr sip.Transport) (*transaction.ServerTransaction, *sip.Request) {
	h.t.Helper()

	req := h.peerRequest(method, tr)

	if method == "INVITE" {
		h.wantResponse(100)
	}

	r := h.tu.NextRequest()
	if r.Req.Method != method {
		h.t.Fatalf("TU got %s, want %s", r.Req.Method, method)
	}

	return r.Tx, req
}

func (h *harness) respond(tx *transaction.ServerTransaction, code int) *sip.Response {
	h.t.Helper()

	res := sip.NewResponse(tx.Request(), code, "")
	if code > 100 {
		_ = res.Header.SetToTag("tu")
	}

	if err := tx.Respond(context.Background(), res); err != nil {
		h.t.Fatal(err)
	}

	return res
}

func (h *harness) wantRequest(method string) *sip.Request {
	h.t.Helper()

	req, _ := h.peer.RecvRequest()
	if req.Method != method {
		h.t.Fatalf("peer got %q, want %s", req.StartLine(), method)
	}

	return req
}

func (h *harness) wantResponse(code int) *sip.Response {
	h.t.Helper()

	res, _ := h.peer.RecvResponse()
	if res.StatusCode != code {
		h.t.Fatalf("peer got %q, want %d", res.StartLine(), code)
	}

	return res
}

func (h *harness) advance(d time.Duration) {
	h.clock.Advance(d)
}

type stater interface {
	State() transaction.State
	Done() <-chan struct{}
	Err() error
}

func wantState(t *testing.T, tx stater, want transaction.State) {
	t.Helper()

	if got := tx.State(); got != want {
		t.Fatalf("state %s, want %s", got, want)
	}
}

func wantTerminated(t *testing.T, tx stater, wantErr error) {
	t.Helper()

	wantState(t, tx, transaction.Terminated)

	select {
	case <-tx.Done():
	default:
		t.Fatal("Done not closed")
	}

	if err := tx.Err(); !errors.Is(err, wantErr) || (wantErr == nil && err != nil) {
		t.Fatalf("Err() = %v, want %v", err, wantErr)
	}
}

func wantCode(t *testing.T, res *sip.Response, code int) {
	t.Helper()

	if res.StatusCode != code {
		t.Fatalf("got %q, want %d", res.StartLine(), code)
	}
}

func topVia(t *testing.T, m sip.Message) sip.Via {
	t.Helper()

	v, err := m.Env().Header.TopVia()
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func pad(req *sip.Request, n int) {
	req.Header.Add("X-Padding", strings.Repeat("x", n))
}
