//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/testue"
)

var perf = flag.Bool("perf", false, "measure the registrations and the calls through the IMS, logging and tracing off")

func TestPerformance(t *testing.T) {
	if !*perf {
		t.Skip("run with -perf")
	}

	s := newSceneWith(t, func(srv *server.Server) { srv.Logger = slog.New(slog.DiscardHandler) })

	go func() {
		for range s.hss.Requests() {
		}
	}()
	go func() {
		for range s.pcrf.Requests() {
		}
	}()

	pairs := make([]pair, 32)

	for i := range pairs {
		from, to := perfSubscriber(2*i), perfSubscriber(2*i+1)
		s.hss.Add(from.hss())
		s.hss.Add(to.hss())

		pairs[i] = pair{from: s.quietCaller(i%subscribers, from), to: to.msisdn + ";user=phone"}

		go answer(t, s.quietCaller((i+1)%subscribers, to))
	}

	var warm sync.WaitGroup

	for _, p := range pairs {
		warm.Go(func() {
			if err := perfCall(t.Context(), p.from, p.to); err != nil {
				t.Errorf("first call: %v", err)
			}
		})
	}

	warm.Wait()

	for _, sc := range []struct {
		what    string
		pairs   int
		perPair int
	}{
		{"1 call at a time", 1, 1},
		{"64 at once, 2 callers", 2, 32},
		{"64 at once, 32 callers", 32, 2},
		{"256 at once, 32 callers", 32, 8},
	} {
		t.Run(sc.what, func(t *testing.T) {
			var l latencies

			r := testing.Benchmark(func(bb *testing.B) {
				bb.ReportAllocs()
				l.reset()

				var (
					next   atomic.Int64
					failed atomic.Value
					wg     sync.WaitGroup
				)

				for _, p := range pairs[:sc.pairs] {
					for range sc.perPair {
						wg.Go(func() {
							for next.Add(1) <= int64(bb.N) {
								start := time.Now()

								if err := perfCall(t.Context(), p.from, p.to); err != nil {
									failed.Store(err)
									return
								}

								l.add(time.Since(start))
							}
						})
					}
				}

				wg.Wait()

				if err, ok := failed.Load().(error); ok {
					bb.Fatal(err)
				}
			})

			report(t, sc.what, r, &l)
		})
	}

	t.Run("registrations", func(t *testing.T) {
		u := s.quietDevice(4, subscriberAt(2), testue.Config{IMEI: "35693803564399"})

		var l latencies

		r := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			l.reset()

			for b.Loop() {
				start := time.Now()

				if err := u.Register(t.Context()); err != nil {
					b.Fatalf("Register: %v", err)
				}

				if err := u.Deregister(t.Context()); err != nil {
					b.Fatalf("Deregister: %v", err)
				}

				l.add(time.Since(start))
			}
		})

		report(t, "register + deregister", r, &l)
	})
}

type pair struct {
	from *testue.UE
	to   string
}

func perfSubscriber(i int) subscriber {
	imsi := fmt.Sprintf("001010000%06d", 1000+i)

	return subscriber{
		imsi:   imsi,
		impi:   imsi + "@" + domain,
		imei:   fmt.Sprintf("356938%08d", i),
		msisdn: fmt.Sprintf("sip:+1555200%04d@%s", i, domain),
		tel:    fmt.Sprintf("tel:+1555200%04d", i),
	}
}

func (sub subscriber) hss() hsstest.Subscriber {
	return hsstest.Subscriber{
		IMPI: sub.impi, IMSI: sub.imsi, K: testK, OPc: testOPc, SQN: 32,
		IMPUs: []cx.ProfileIdentity{{Identity: "sip:" + sub.impi, Barred: true}, {Identity: sub.msisdn}, {Identity: sub.tel}},
	}
}

type latencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *latencies) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.d = l.d[:0]
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.d = append(l.d, d)
}

func (l *latencies) at(q float64) time.Duration {
	if len(l.d) == 0 {
		return 0
	}

	return l.d[min(len(l.d)-1, int(q*float64(len(l.d))))]
}

func report(t *testing.T, what string, r testing.BenchmarkResult, l *latencies) {
	t.Helper()

	slices.Sort(l.d)

	t.Logf("%-28s %6d ops %8.1f ops/s  p50 %9s  p99 %9s  max %9s  %8d B/op %6d allocs/op", what, r.N,
		float64(r.N)/r.T.Seconds(), l.at(0.5).Round(time.Microsecond), l.at(0.99).Round(time.Microsecond),
		l.at(1).Round(time.Microsecond), r.AllocedBytesPerOp(), r.AllocsPerOp())
}

func perfCall(ctx context.Context, u *testue.UE, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	c, err := u.MMTel().Invite(target, testue.CallOptions{})
	if err != nil {
		return err
	}

	if _, err := c.Wait(ctx); err != nil {
		return err
	}

	if err := c.Bye(ctx); err != nil {
		return err
	}

	select {
	case <-c.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func answer(t *testing.T, u *testue.UE) {
	for c := range u.MMTel().Calls() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := c.Ring(ctx); err != nil {
				t.Logf("Ring: %v", err)
				return
			}

			if err := c.Answer(ctx); err != nil {
				t.Logf("Answer: %v", err)
				return
			}

			select {
			case <-c.Done():
			case <-ctx.Done():
				t.Logf("call not ended")
			}
		}()
	}
}

func (s *scene) quietCaller(i int, sub subscriber) *testue.UE {
	s.t.Helper()

	u := s.quietDevice(i, sub, testue.Config{MMTel: testue.MMTelConfig{AcceptCalls: true}})
	s.register(u)

	return u
}

func (s *scene) quietDevice(i int, id subscriber, cfg testue.Config) *testue.UE {
	s.t.Helper()

	h := s.host(i)

	cfg.IMSI = id.imsi
	if cfg.IMEI == "" {
		cfg.IMEI = id.imei
	}

	cfg.PCSCF = netip.AddrPortFrom(imsAddrs[0].Addr(), pcscfPort)
	cfg.Local = ueAddrsAt(i)[0].Addr()
	cfg.Do = h.ns.Do
	cfg.Logger = slog.New(slog.DiscardHandler)
	cfg.K, cfg.OPc = testK, testOPc
	cfg.Kernel = testue.XFRMOf(h.xfrm)

	u, err := testue.New(cfg)
	if err != nil {
		s.t.Fatal(err)
	}

	s.t.Cleanup(func() { _ = u.Close() })

	return u
}
