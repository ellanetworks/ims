package siptest

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
)

type Recorder struct {
	t           testing.TB
	messages    chan sip.Message
	parseErrors chan *sip.ParseError
	done        chan struct{}
}

func NewRecorder(t testing.TB) *Recorder {
	r := &Recorder{
		t:           t,
		messages:    make(chan sip.Message, 256),
		parseErrors: make(chan *sip.ParseError, 256),
		done:        make(chan struct{}),
	}

	t.Cleanup(func() { close(r.done) })

	return r
}

func (r *Recorder) HandleMessage(m sip.Message) {
	select {
	case r.messages <- m:
	case <-r.done:
	}
}

func (r *Recorder) HandleParseError(err *sip.ParseError) {
	select {
	case r.parseErrors <- err:
	case <-r.done:
	}
}

func (r *Recorder) Next() sip.Message {
	r.t.Helper()

	select {
	case m := <-r.messages:
		return m
	case <-time.After(Timeout):
		r.t.Fatal("siptest: no message received")
		return nil
	}
}

func (r *Recorder) NextRequest() *sip.Request {
	r.t.Helper()

	m := r.Next()

	req, ok := m.(*sip.Request)
	if !ok {
		r.t.Fatalf("siptest: got %q, want a request", m.StartLine())
	}

	return req
}

func (r *Recorder) NextResponse() *sip.Response {
	r.t.Helper()

	m := r.Next()

	res, ok := m.(*sip.Response)
	if !ok {
		r.t.Fatalf("siptest: got %q, want a response", m.StartLine())
	}

	return res
}

func (r *Recorder) NextParseError() *sip.ParseError {
	r.t.Helper()

	select {
	case err := <-r.parseErrors:
		return err
	case <-time.After(Timeout):
		r.t.Fatal("siptest: no parse error received")
		return nil
	}
}

func (r *Recorder) None(d time.Duration) {
	r.t.Helper()

	select {
	case m := <-r.messages:
		r.t.Fatalf("siptest: unexpected %q", m.StartLine())
	case err := <-r.parseErrors:
		r.t.Fatalf("siptest: unexpected parse error: %v", err)
	case <-time.After(d):
	}
}

func NewTransport(t testing.TB, cfg transport.Config) (*transport.Transport, *Recorder) {
	t.Helper()

	rec := NewRecorder(t)
	if cfg.Handler == nil {
		cfg.Handler = rec
	}

	tr := transport.New(cfg)

	t.Cleanup(func() { _ = tr.Close() })

	return tr, rec
}

func Listen(t testing.TB, tr *transport.Transport, addr netip.Addr) netip.AddrPort {
	t.Helper()

	local, err := tr.Listen(context.Background(), netip.AddrPortFrom(addr, 0))
	if err != nil {
		t.Fatal(err)
	}

	return local
}
