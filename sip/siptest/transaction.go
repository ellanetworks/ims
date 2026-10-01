package siptest

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type ServerRequest struct {
	Tx  *transaction.ServerTransaction
	Req *sip.Request
}

type TransactionError struct {
	Tx  *transaction.ServerTransaction
	Err error
}

type TU struct {
	t        testing.TB
	requests chan ServerRequest
	cancels  chan ServerRequest
	acks     chan *sip.Request
	errs     chan TransactionError
	done     chan struct{}
	stop     sync.Once
}

func NewTU(t testing.TB) *TU {
	u := &TU{
		t:        t,
		requests: make(chan ServerRequest, 256),
		cancels:  make(chan ServerRequest, 256),
		acks:     make(chan *sip.Request, 256),
		errs:     make(chan TransactionError, 256),
		done:     make(chan struct{}),
	}

	t.Cleanup(u.Stop)

	return u
}

func (u *TU) Stop() {
	u.stop.Do(func() { close(u.done) })
}

func (u *TU) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	put(u.requests, ServerRequest{Tx: tx, Req: req}, u.done)
}

func (u *TU) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	put(u.cancels, ServerRequest{Tx: tx, Req: cancel}, u.done)
}

func (u *TU) HandleAck(ack *sip.Request) {
	put(u.acks, ack, u.done)
}

func (u *TU) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	put(u.errs, TransactionError{Tx: tx, Err: err}, u.done)
}

func (u *TU) NextRequest() ServerRequest {
	u.t.Helper()
	return get(u.t, u.requests, "request")
}

func (u *TU) NextCancel() ServerRequest {
	u.t.Helper()
	return get(u.t, u.cancels, "CANCEL")
}

func (u *TU) NextAck() *sip.Request {
	u.t.Helper()
	return get(u.t, u.acks, "ACK")
}

func (u *TU) NextError() TransactionError {
	u.t.Helper()
	return get(u.t, u.errs, "transaction error")
}

func (u *TU) None(d time.Duration) {
	u.t.Helper()

	select {
	case r := <-u.requests:
		u.t.Fatalf("siptest: unexpected request %q", r.Req.StartLine())
	case r := <-u.cancels:
		u.t.Fatalf("siptest: unexpected CANCEL %q", r.Req.StartLine())
	case a := <-u.acks:
		u.t.Fatalf("siptest: unexpected ACK %q", a.StartLine())
	case e := <-u.errs:
		u.t.Fatalf("siptest: unexpected transaction error: %v", e.Err)
	case <-time.After(d):
	}
}

type Responses struct {
	t         testing.TB
	responses chan *sip.Response
	errs      chan error
	done      chan struct{}
}

func NewResponses(t testing.TB) *Responses {
	r := &Responses{
		t:         t,
		responses: make(chan *sip.Response, 256),
		errs:      make(chan error, 16),
		done:      make(chan struct{}),
	}

	t.Cleanup(func() { close(r.done) })

	return r
}

func (r *Responses) HandleResponse(res *sip.Response) {
	put(r.responses, res, r.done)
}

func (r *Responses) HandleError(err error) {
	put(r.errs, err, r.done)
}

func (r *Responses) Next() *sip.Response {
	r.t.Helper()
	return get(r.t, r.responses, "response")
}

func (r *Responses) NextError() error {
	r.t.Helper()
	return get(r.t, r.errs, "transaction error")
}

func (r *Responses) None(d time.Duration) {
	r.t.Helper()

	select {
	case res := <-r.responses:
		r.t.Fatalf("siptest: unexpected response %q", res.StartLine())
	case err := <-r.errs:
		r.t.Fatalf("siptest: unexpected transaction error: %v", err)
	case <-time.After(d):
	}
}

func NewLayer(t testing.TB, cfg transaction.Config) (*transaction.Layer, *TU) {
	t.Helper()

	tu := NewTU(t)
	if cfg.Handler == nil {
		cfg.Handler = tu
	}

	l := transaction.New(cfg)

	t.Cleanup(func() {
		tu.Stop()

		_ = l.Close()
	})

	return l, tu
}

func ListenLayer(t testing.TB, l *transaction.Layer, addr netip.Addr) netip.AddrPort {
	t.Helper()

	local, err := l.Listen(context.Background(), netip.AddrPortFrom(addr, 0))
	if err != nil {
		t.Fatal(err)
	}

	return local
}

func put[T any](ch chan<- T, v T, done <-chan struct{}) {
	select {
	case ch <- v:
	case <-done:
	}
}

func get[T any](t testing.TB, ch <-chan T, what string) T {
	t.Helper()

	select {
	case v := <-ch:
		return v
	case <-time.After(Timeout):
		t.Fatalf("siptest: no %s", what)

		var zero T

		return zero
	}
}
