package ibcf

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type solo struct {
	b atomic.Pointer[IBCF]
}

func (s *solo) HandleRequest(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if b := s.b.Load(); b != nil {
		b.Handler().HandleRequest(ctx, tx, req)
		return
	}

	_ = tx.Respond(sip.NewResponse(req, 503, ""))
}

func (s *solo) HandleCancel(ctx context.Context, tx *transaction.ServerTransaction, cancel *sip.Request) {
	if b := s.b.Load(); b != nil {
		b.Handler().HandleCancel(ctx, tx, cancel)
	}
}

func (s *solo) HandleAck(ctx context.Context, ack *sip.Request) {
	if b := s.b.Load(); b != nil {
		b.Handler().HandleAck(ctx, ack)
	}
}

func (s *solo) HandleTransactionError(ctx context.Context, tx *transaction.ServerTransaction, err error) {
	if b := s.b.Load(); b != nil {
		b.Handler().HandleTransactionError(ctx, tx, err)
	}
}

func (s *solo) config(tc transaction.Config, cfg Config) transaction.Config {
	tc.Handler = s
	tc.Filter = func(m sip.Message) error {
		if b := s.b.Load(); b != nil {
			return b.Filter(m)
		}

		return nil
	}
	tc.TraceContext = func(f sip.Flow) bool {
		b := s.b.Load()
		return b != nil && b.TraceContext(f)
	}
	tc.SpanAttributes = func(req *sip.Request, kind trace.SpanKind) []attribute.KeyValue {
		if b := s.b.Load(); b != nil {
			return b.SpanAttributes(req, kind)
		}

		return nil
	}
	tc.SpanLinks = func(req *sip.Request) []trace.SpanContext {
		if b := s.b.Load(); b != nil {
			return b.SpanLinks(req)
		}

		return nil
	}
	tc.Untraced = func(req *sip.Request) bool {
		b := s.b.Load()
		return b != nil && b.Untraced(req)
	}
	tc.Transport.Accept = func(local, remote netip.AddrPort) bool {
		b := s.b.Load()
		return b != nil && b.Accept(local, remote)
	}
	tc.Transport.TLSClientOptions = func(f sip.Flow) (transport.TLSOptions, error) {
		if b := s.b.Load(); b != nil {
			return b.TLSClientOptions(f)
		}

		return transport.TLSOptions{}, transport.ErrNoTLSConfig
	}

	if tc.Transport.MaxConnectionsPerAddress == nil {
		tc.Transport.MaxConnectionsPerAddress = func(netip.AddrPort) int { return MaxConnectionsPerSource }
	}

	for _, a := range cfg.Advertised {
		tc.Aliases = append(tc.Aliases, a.String())
	}

	return tc
}

func launch(t testing.TB, cfg Config, tc transaction.Config, bind bool) (*IBCF, error) {
	t.Helper()

	s := &solo{}
	l := transaction.New(s.config(tc, cfg))

	t.Cleanup(func() { _ = l.Close() })

	cfg.Layer = l

	var err error

	if cfg.Internal, err = attach(t.Context(), cfg.Internal, bind, l.Listen, l.Serve); err != nil {
		return nil, err
	}

	if cfg.External, err = attach(t.Context(), cfg.External, bind, l.Listen, l.Serve); err != nil {
		return nil, err
	}

	secure := l.ClientTLS

	if cfg.Certificate != nil {
		srv := ServerTLS(cfg.Certificate, cfg.Settings)
		secure = func(ctx context.Context, a netip.AddrPort) (netip.AddrPort, error) { return l.ListenTLS(ctx, a, srv) }
	}

	if cfg.TLS, err = attach(t.Context(), cfg.TLS, bind, secure, l.Serve); err != nil {
		return nil, err
	}

	b, err := New(t.Context(), cfg)
	if err != nil {
		return nil, err
	}

	s.b.Store(b)

	return b, nil
}

func attach(ctx context.Context, want []netip.AddrPort, bind bool, listen func(context.Context, netip.AddrPort) (netip.AddrPort, error),
	serve func(netip.AddrPort) error,
) ([]netip.AddrPort, error) {
	out := make([]netip.AddrPort, 0, len(want))

	var port uint16

	for _, a := range want {
		if a.Port() != 0 || port == 0 {
			port = a.Port()
		}

		a = netip.AddrPortFrom(a.Addr().Unmap(), port)

		if !bind {
			if err := serve(a); err != nil {
				return nil, err
			}

			out = append(out, a)

			continue
		}

		bound, err := listen(ctx, a)
		if err != nil {
			return nil, err
		}

		port = bound.Port()
		out = append(out, bound)
	}

	return out, nil
}
