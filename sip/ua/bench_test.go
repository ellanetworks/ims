package ua_test

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
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

		_ = a.Shutdown(ctx)
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

// netAgent is an agent on the in-memory net of a benchmark.
func netAgent(b *testing.B, n *siptest.Net, name string, configure func(*ua.Config)) (*ua.Agent, netip.AddrPort) {
	b.Helper()

	addr := n.Addr()

	cfg := ua.Config{
		Prepare: func(req *sip.Request) error {
			remote, err := netip.ParseAddr(req.URI.Host)
			if err != nil {
				return err
			}

			req.Flow = sip.Flow{Transport: sip.UDP, Local: addr, Remote: netip.AddrPortFrom(remote, req.URI.Port)}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, addr).String())

			return nil
		},
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: addr.Addr().String(), Port: addr.Port()}},
		Allow:       append(slices.Clone(ua.Methods), "SUBSCRIBE", "NOTIFY"),
		Transaction: n.Config(transaction.Config{}),
	}

	if configure != nil {
		configure(&cfg)
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

// BenchmarkResourceChange: one change of a resource fanned out to the NOTIFYs of its 1000 subscriptions, each
// rendered for its subscriber, sent and answered: affiliation and conference notifications to a large group
// (TS 24.379 §9.2.2.3.5, §10.1.3).
func BenchmarkResourceChange(b *testing.B) {
	const subscribers = 1000

	n := siptest.NewNet(b)

	res := ua.NewResource([]string{"a"}, ua.ResourceConfig[[]string, string]{
		Full: func(value any, seq uint32, state []string) (ua.Notification, error) {
			return ua.Notification{Bodies: []sip.BodyPart{sip.NewBodyPart("text/plain", fmt.Appendf(nil, "%v %v %d", value, state, seq))}}, nil
		},
		Partial: func(value any, seq uint32, _ []string, delta string) (ua.Notification, error) {
			return ua.Notification{Bodies: []sip.BodyPart{sip.NewBodyPart("text/plain", fmt.Appendf(nil, "%v +%s %d", value, delta, seq))}}, nil
		},
	})

	_, server := netAgent(b, n, "group", func(c *ua.Config) {
		c.OnRequest = func(r *ua.Request) {
			from, _ := r.Message().Header.From()
			_, _ = r.AcceptSubscription(ua.Acceptance{Expires: time.Hour, Resource: res, Value: from.URI.User})
		}
	})

	client, _ := netAgent(b, n, "members", nil)

	var notified atomic.Int64

	uri := sip.URI{Scheme: "sip", User: "group", Host: server.Addr().String(), Port: server.Port()}

	for i := range subscribers {
		req := sip.NewRequest("SUBSCRIBE", uri)
		req.Header.Add("From", fmt.Sprintf("<sip:member%d@10.0.0.9>", i))
		req.Header.Add("To", "<"+uri.String()+">")
		req.Header.Add("Event", "test")
		req.Header.Add("Expires", "3600")

		if _, err := client.Subscribe(req, ua.SubscriptionHandler{
			Notify: func(*ua.Subscription, *ua.Request) { notified.Add(1) },
		}); err != nil {
			b.Fatal(err)
		}
	}

	n.Quiesce()

	if got := len(res.Subscriptions()); got != subscribers {
		b.Fatalf("%d subscriptions", got)
	}

	state := []string{"a"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; b.Loop(); i++ {
		notified.Store(0)

		m := fmt.Sprint(i)
		state = append(state[:len(state):len(state)], m)
		_ = res.Change(state, m)

		// Every subscriber notified before the next change, each NOTIFY a partial one.
		n.Quiesce()

		if got := notified.Load(); got != subscribers {
			b.Fatalf("%d NOTIFYs, want %d", got, subscribers)
		}
	}

	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/subscribers, "ns/notify")
}
