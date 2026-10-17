package ua_test

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// benchAgent is an agent on loopback UDP for the benchmarks.
type benchAgent struct {
	a    *ua.Agent
	addr netip.AddrPort
}

func newBenchAgent(b *testing.B, name string, onInvite func(r *ua.Request) ua.Handler) *benchAgent {
	b.Helper()

	c, err := new(net.ListenConfig).ListenPacket(b.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}

	addr := netip.MustParseAddrPort(c.LocalAddr().String())
	_ = c.Close()

	ba := &benchAgent{addr: addr}

	a, err := ua.New(ua.Config{
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
		Contact:  sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: addr.Addr().String(), Port: addr.Port()}},
		OnInvite: onInvite,
	})
	if err != nil {
		b.Fatal(err)
	}

	if _, err := a.Layer().Listen(context.Background(), addr); err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_ = a.Close(ctx)
	})

	ba.a = a

	return ba
}

const benchSDP = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0\r\n"

// callee answers every INVITE with a 200.
func callee(b *testing.B) *benchAgent {
	return newBenchAgent(b, "bob", func(r *ua.Request) ua.Handler {
		_ = r.Accept(ua.SDP([]byte(benchSDP)))
		return ua.Handler{}
	})
}

// call makes a call from caller to callee, and hangs it up once established: INVITE, 200, ACK, BYE, 200.
func call(b *testing.B, caller, to *benchAgent) {
	uri := sip.URI{Scheme: "sip", User: "bob", Host: to.addr.Addr().String(), Port: to.addr.Port()}
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<sip:alice@"+caller.addr.String()+">")
	req.Header.Add("To", "<"+uri.String()+">")
	req.SetBodies(ua.SDP([]byte(benchSDP)))

	ended := make(chan struct{})

	_, err := caller.a.Invite(req, ua.Handler{
		Established: func(s *ua.Session, _ sip.Message) { _ = s.Bye(ua.Change{}, nil) },
		Ended:       func(*ua.Session, ua.End) { close(ended) },
		Failed:      func(*ua.Outgoing, *sip.Response, error) { close(ended) },
	})
	if err != nil {
		b.Error(err)
		return
	}

	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		b.Error("call did not end")
	}
}

// BenchmarkCall: one call after the other, set up and hung up; ns/op is the duration of a call.
func BenchmarkCall(b *testing.B) {
	alice, bob := newBenchAgent(b, "alice", nil), callee(b)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		call(b, alice, bob)
	}
}

// BenchmarkCallsParallel: calls set up and hung up by 64 concurrent callers on one agent, to one agent.
func BenchmarkCallsParallel(b *testing.B) {
	alice, bob := newBenchAgent(b, "alice", nil), callee(b)

	const callers = 64

	b.ReportAllocs()
	b.ResetTimer()

	var (
		next atomic.Int64
		wg   sync.WaitGroup
	)

	start := time.Now()

	for range callers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for next.Add(1) <= int64(b.N) {
				call(b, alice, bob)
			}
		}()
	}

	wg.Wait()

	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "calls/s")
}

// BenchmarkEstablishedSessions: the memory and the goroutines of established sessions, both sides counted.
func BenchmarkEstablishedSessions(b *testing.B) {
	const n = 2000

	for b.Loop() {
		b.StopTimer()

		alice, bob := newBenchAgent(b, "alice", nil), callee(b)

		runtime.GC()

		var before runtime.MemStats

		runtime.ReadMemStats(&before)

		goroutines := runtime.NumGoroutine()

		var (
			wg       sync.WaitGroup
			sessions = make(chan *ua.Session, n)
		)

		b.StartTimer()

		for i := range n {
			wg.Add(1)

			uri := sip.URI{Scheme: "sip", User: "bob", Host: bob.addr.Addr().String(), Port: bob.addr.Port()}
			req := sip.NewRequest("INVITE", uri)
			req.Header.Add("From", "<sip:alice@"+alice.addr.String()+">")
			req.Header.Add("To", "<"+uri.String()+">")
			req.Header.Set("Call-ID", fmt.Sprintf("bench-%d", i))
			req.SetBodies(ua.SDP([]byte(benchSDP)))

			if _, err := alice.a.Invite(req, ua.Handler{
				Established: func(s *ua.Session, _ sip.Message) {
					sessions <- s

					wg.Done()
				},
				Failed: func(*ua.Outgoing, *sip.Response, error) { wg.Done() },
			}); err != nil {
				b.Fatal(err)
			}
		}

		wg.Wait()
		b.StopTimer()

		// Let the transactions end their retransmissions, and the strands go idle.
		time.Sleep(200 * time.Millisecond)
		runtime.GC()

		var after runtime.MemStats

		runtime.ReadMemStats(&after)

		b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/n/2, "B/session")
		b.ReportMetric(float64(runtime.NumGoroutine()-goroutines)/n, "goroutines/call")

		close(sessions)

		for s := range sessions {
			_ = s.Bye(ua.Change{}, nil)
		}

		b.StartTimer()
	}
}
