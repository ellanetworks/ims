package b2bua_test

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// benchAgent is an agent on the in-memory net of a benchmark: the stack measured without sockets.
func benchAgent(b *testing.B, n *siptest.Net, name string, onInvite func(a *ua.Agent) func(*ua.Request) ua.Handler) (*ua.Agent, netip.AddrPort) {
	b.Helper()

	addr := n.Addr()

	var a *ua.Agent

	cfg := ua.Config{
		Prepare: func(req *sip.Request) error {
			target := req.URI
			if r, err := req.Header.TopRoute(); err == nil {
				target = r.URI
			}

			remote, err := netip.ParseAddr(target.Host)
			if err != nil {
				return err
			}

			req.Flow = sip.Flow{Transport: sip.UDP, Local: addr, Remote: netip.AddrPortFrom(remote, target.Port)}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, addr).String())

			return nil
		},
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: addr.Addr().String(), Port: addr.Port()}},
		Transaction: n.Config(transaction.Config{}),
	}

	if onInvite != nil {
		cfg.OnInvite = func(r *ua.Request) ua.Handler { return onInvite(a)(r) }
	}

	a, err := ua.New(cfg)
	if err != nil {
		b.Fatal(err)
	}

	if err := a.Layer().Serve(addr); err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_ = a.Shutdown(ctx)
	})

	return a, addr
}

const benchSDP = "v=0\r\no=- 1 1 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0\r\n"

// bridgedNet is a caller, a B2BUA bridging each INVITE to a callee, and the callee, answering each with a 200.
func bridgedNet(b *testing.B) (alice *ua.Agent, b2b netip.AddrPort) {
	b.Helper()

	// The strand checks of the tests cost a stack trace per job.
	ua.CheckStrands(false)
	b.Cleanup(func() { ua.CheckStrands(true) })

	n := siptest.NewNet(b)

	_, bob := benchAgent(b, n, "bob", func(*ua.Agent) func(*ua.Request) ua.Handler {
		return func(r *ua.Request) ua.Handler {
			_ = r.Accept(ua.SDP([]byte(benchSDP)))
			return ua.Handler{}
		}
	})

	target := sip.URI{Scheme: "sip", User: "bob", Host: bob.Addr().String(), Port: bob.Port()}

	_, b2b = benchAgent(b, n, "b2b", func(a *ua.Agent) func(*ua.Request) ua.Handler {
		return func(r *ua.Request) ua.Handler {
			call := b2bua.NewCall(a, r.Strand(), b2bua.CallHandler{})

			out := sip.NewRequest("INVITE", target)
			out.Header.Add("From", r.Message().Header.Get("From"))
			out.Header.Add("To", "<"+target.String()+">")

			_, _ = b2bua.Bridge(call, r, out, b2bua.RelayConfig{})

			return ua.Handler{}
		}
	})

	alice, _ = benchAgent(b, n, "alice", nil)

	return alice, b2b
}

// invite sends an INVITE from alice to the B2BUA, its session to established.
func invite(b *testing.B, alice *ua.Agent, b2b netip.AddrPort, i int64, h ua.Handler) {
	uri := sip.URI{Scheme: "sip", User: "bob", Host: b2b.Addr().String(), Port: b2b.Port()}
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<sip:alice@10.0.0.9>")
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Set("Call-ID", fmt.Sprintf("bridged-%d", i))
	req.SetBodies(ua.SDP([]byte(benchSDP)))

	if _, err := alice.Invite(req, h); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkBridgedCalls: calls set up through a B2BUA and hung up, by 64 concurrent callers: INVITE, 200, ACK, BYE,
// 200 on each leg. Each MCPTT call crosses several such hops (participating, controlling, participating).
func BenchmarkBridgedCalls(b *testing.B) {
	alice, b2b := bridgedNet(b)

	const callers = 64

	b.ReportAllocs()
	b.ResetTimer()

	var (
		next atomic.Int64
		wg   sync.WaitGroup
	)

	start := time.Now()

	for range callers {
		wg.Go(func() {
			for i := next.Add(1); i <= int64(b.N); i = next.Add(1) {
				ended := make(chan struct{})

				invite(b, alice, b2b, i, ua.Handler{
					Established: func(s *ua.Session, _ sip.Message) { _ = s.Bye(ua.Change{}, nil) },
					Ended:       func(*ua.Session, ua.End) { close(ended) },
					Failed:      func(*ua.Outgoing, *sip.Response, error) { b.Error("call failed") },
				})

				select {
				case <-ended:
				case <-time.After(5 * time.Second):
					b.Error("call did not end")
					return
				}
			}
		})
	}

	wg.Wait()

	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "calls/s")
}

// BenchmarkBridgedSessions: the memory of established bridged calls: the caller's session, the two of the B2BUA and
// its relay, and the callee's, all counted.
func BenchmarkBridgedSessions(b *testing.B) {
	const n = 1000

	for b.Loop() {
		b.StopTimer()

		alice, b2b := bridgedNet(b)

		runtime.GC()

		var before runtime.MemStats

		runtime.ReadMemStats(&before)

		var (
			wg       sync.WaitGroup
			sessions = make(chan *ua.Session, n)
		)

		b.StartTimer()

		for i := range n {
			wg.Add(1)

			invite(b, alice, b2b, int64(i), ua.Handler{
				Established: func(s *ua.Session, _ sip.Message) {
					sessions <- s

					wg.Done()
				},
				Failed: func(*ua.Outgoing, *sip.Response, error) { wg.Done() },
			})
		}

		wg.Wait()
		b.StopTimer()

		runtime.GC()

		var after runtime.MemStats

		runtime.ReadMemStats(&after)

		b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/n, "B/call")

		close(sessions)

		for s := range sessions {
			_ = s.Bye(ua.Change{}, nil)
		}

		b.StartTimer()
	}
}
