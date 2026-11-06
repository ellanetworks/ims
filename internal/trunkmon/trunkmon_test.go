package trunkmon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeSettings struct {
	mu      sync.Mutex
	st      settings.Settings
	changed chan struct{}
}

func newFakeSettings(trunks ...settings.Trunk) *fakeSettings {
	return &fakeSettings{
		st:      settings.Settings{Operator: settings.Operator{MCC: "001", MNC: "01"}, Trunks: trunks},
		changed: make(chan struct{}),
	}
}

func (s *fakeSettings) Get() settings.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.st
}

func (s *fakeSettings) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.changed
}

func (s *fakeSettings) setTrunks(trunks ...settings.Trunk) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.st.Trunks = trunks
	close(s.changed)
	s.changed = make(chan struct{})
}

func fakeResolve(_ context.Context, u sip.URI, transports []sip.Transport) ([]sip.Server, error) {
	if u.Host == "unresolvable.example" {
		return nil, errors.New("no such host")
	}

	tr := sip.UDP
	if t, ok := u.Params.Get("transport"); ok {
		tr = sip.Transport(strings.ToUpper(t))
	}

	if a, err := netip.ParseAddr(u.Host); err == nil {
		port := u.Port
		if port == 0 {
			port = sip.DefaultPortFor(tr)
		}

		return []sip.Server{{Transport: tr, Addr: netip.AddrPortFrom(a, port)}}, nil
	}

	if _, ok := u.Params.Get("transport"); !ok {
		tr = transports[0]
	}

	return []sip.Server{
		{Transport: tr, Addr: netip.MustParseAddrPort("192.0.2.1:5060")},
		{Transport: tr, Addr: netip.MustParseAddrPort("192.0.2.2:5060")},
	}, nil
}

type sent struct {
	req *sip.Request
	to  sip.Server
}

type fakeSender struct {
	noTLS  bool
	mu     sync.Mutex
	reply  func(ctx context.Context, to sip.Server) (int, error)
	sent   []sent
	active int
	most   int
}

func newFakeSender(code int) *fakeSender {
	s := &fakeSender{}
	s.answer(code, nil)

	return s
}

func (s *fakeSender) answer(code int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reply = func(context.Context, sip.Server) (int, error) { return code, err }
}

func (s *fakeSender) use(f func(ctx context.Context, to sip.Server) (int, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reply = f
}

func (s *fakeSender) Send(ctx context.Context, req *sip.Request, to sip.Server) (*sip.Response, error) {
	s.mu.Lock()
	s.sent = append(s.sent, sent{req: req, to: to})
	s.active++
	s.most = max(s.most, s.active)
	reply := s.reply
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()

	code, err := reply(ctx, to)
	if err != nil {
		return nil, err
	}

	return sip.NewResponse(req, code, ""), nil
}

func (s *fakeSender) Transports(t settings.Trunk) []sip.Transport {
	var out []sip.Transport

	switch {
	case t.TLS.Required:
	case t.Trusted:
		out = []sip.Transport{sip.TCP}
	default:
		out = []sip.Transport{sip.UDP, sip.TCP}
	}

	if t.TLS.Trusts() && !s.noTLS {
		out = append(out, sip.TLS)
	}

	return out
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.sent)
}

func (s *fakeSender) all() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.sent)
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)

	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()

	was := !t.stopped
	t.stopped = true
	t.c.timers = slices.DeleteFunc(t.c.timers, func(o *fakeTimer) bool { return o == t })

	return was
}

func (c *fakeClock) pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []time.Duration

	for _, t := range c.timers {
		out = append(out, t.at.Sub(c.now))
	}

	return out
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)

	var due []*fakeTimer

	c.timers = slices.DeleteFunc(c.timers, func(t *fakeTimer) bool {
		if !t.at.After(c.now) {
			t.stopped = true
			due = append(due, t)

			return true
		}

		return false
	})
	c.mu.Unlock()

	for _, t := range due {
		t.f()
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(time.Millisecond)
	}
}

func trunk(id string, interval time.Duration) settings.Trunk {
	return settings.Trunk{
		ID:                id,
		Name:              "trunk " + id,
		Addresses:         []settings.TrunkAddress{{Host: "sip.carrier.example"}},
		Domain:            "carrier.example",
		KeepaliveInterval: interval,
		Enabled:           true,
	}
}

type harness struct {
	t        *testing.T
	settings *fakeSettings
	sender   *fakeSender
	clock    *fakeClock
	metrics  *Metrics
	mon      *Monitor
}

func start(t *testing.T, sender *fakeSender, failures int, trunks ...settings.Trunk) *harness {
	t.Helper()

	h := &harness{t: t, settings: newFakeSettings(trunks...), sender: sender, clock: newFakeClock(), metrics: NewMetrics()}

	mon, err := Start(Config{
		Settings: h.settings, Resolve: fakeResolve, Sender: sender, Metrics: h.metrics, Clock: h.clock,
		Failures: failures,
	})
	if err != nil {
		t.Fatal(err)
	}

	h.mon = mon

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := mon.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	return h
}

func (h *harness) waitScheduled(n int) {
	h.t.Helper()
	eventually(h.t, fmt.Sprintf("%d scheduled probes", n), func() bool { return len(h.clock.pending()) == n })
}

func (h *harness) state(id string) settings.TrunkState {
	return h.mon.TrunkStatus(id).State
}

func (h *harness) gauge(id string) float64 {
	h.t.Helper()

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(h.metrics.Collectors()...)

	families, err := reg.Gather()
	if err != nil {
		h.t.Fatal(err)
	}

	for _, f := range families {
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == id {
				return m.GetGauge().GetValue()
			}
		}
	}

	h.t.Fatalf("no ellaims_trunk_up series for %s", id)

	return 0
}

func (h *harness) series() int {
	return testutil.CollectAndCount(h.metrics)
}

func TestUpOn200(t *testing.T) {
	h := start(t, newFakeSender(200), 2, trunk("a", 30*time.Second))
	h.waitScheduled(1)

	st := h.mon.TrunkStatus("a")
	if st.State != settings.TrunkUp || !st.Since.Equal(h.clock.Now()) {
		t.Fatalf("status = %+v, want up since %v", st, h.clock.Now())
	}

	if g := h.gauge("a"); g != 1 {
		t.Fatalf("trunk_up = %v, want 1", g)
	}

	if d := h.clock.pending(); d[0] != 30*time.Second {
		t.Fatalf("next probe in %v, want 30s", d[0])
	}
}

func TestOptionsRequest(t *testing.T) {
	h := start(t, newFakeSender(200), 2, trunk("a", 30*time.Second))
	h.waitScheduled(1)
	h.clock.advance(30 * time.Second)
	eventually(t, "a second probe", func() bool { return h.sender.count() == 2 })

	all := h.sender.all()
	req := all[0].req

	checks := map[string][2]string{
		"Request-URI":  {req.URI.String(), "sip:carrier.example"},
		"Method":       {req.Method, "OPTIONS"},
		"To":           {req.Header.Get("To"), "<sip:carrier.example>"},
		"Max-Forwards": {req.Header.Get("Max-Forwards"), "70"},
		"CSeq":         {req.Header.Get("CSeq"), "1 OPTIONS"},
		"Accept":       {req.Header.Get("Accept"), "application/sdp"},
		"Target":       {all[0].to.Addr.String(), "192.0.2.1:5060"},
	}

	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}

	from, err := req.Header.From()
	if err != nil || from.URI.String() != "sip:ims.mnc001.mcc001.3gppnetwork.org" || from.Tag() == "" {
		t.Errorf("From = %q", req.Header.Get("From"))
	}

	if req.Header.CallID() == "" || req.Header.CallID() == all[1].req.Header.CallID() {
		t.Errorf("Call-IDs %q and %q are not unique", req.Header.CallID(), all[1].req.Header.CallID())
	}
}

func TestAnyFinalResponseButServiceUnavailableIsAlive(t *testing.T) {
	for _, code := range []int{200, 403, 404, 405, 408, 486, 500, 603} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h := start(t, newFakeSender(code), 1, trunk("a", 30*time.Second))
			h.waitScheduled(1)

			if s := h.state("a"); s != settings.TrunkUp {
				t.Fatalf("state = %s, want up", s)
			}
		})
	}
}

func TestDownAfterFailures(t *testing.T) {
	for name, fail := range map[string]func(*fakeSender){
		"timeout":             func(s *fakeSender) { s.answer(0, context.DeadlineExceeded) },
		"transport error":     func(s *fakeSender) { s.answer(0, errors.New("connection refused")) },
		"service unavailable": func(s *fakeSender) { s.answer(503, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			sender := &fakeSender{}
			fail(sender)

			h := start(t, sender, 3, trunk("a", 30*time.Second))

			for i := 1; i <= 2; i++ {
				h.waitScheduled(1)

				if s := h.state("a"); s != settings.TrunkUnknown {
					t.Fatalf("after %d failures state = %s, want unknown", i, s)
				}

				if n := h.series(); n != 0 {
					t.Fatalf("after %d failures %d trunk_up series, want none", i, n)
				}

				h.clock.advance(30 * time.Second)
			}

			h.waitScheduled(1)

			st := h.mon.TrunkStatus("a")
			if st.State != settings.TrunkDown || !st.Since.Equal(h.clock.Now()) {
				t.Fatalf("status = %+v, want down since %v", st, h.clock.Now())
			}

			if g := h.gauge("a"); g != 0 {
				t.Fatalf("trunk_up = %v, want 0", g)
			}

			if n := sender.count(); n != 6 {
				t.Fatalf("%d OPTIONS sent, want 6 (two servers, three rounds)", n)
			}
		})
	}
}

func TestRecovery(t *testing.T) {
	sender := newFakeSender(200)
	h := start(t, sender, 2, trunk("a", 10*time.Second))
	h.waitScheduled(1)

	up := h.mon.TrunkStatus("a").Since

	sender.answer(0, context.DeadlineExceeded)
	h.clock.advance(10 * time.Second)
	h.waitScheduled(1)

	if st := h.mon.TrunkStatus("a"); st.State != settings.TrunkUp || !st.Since.Equal(up) {
		t.Fatalf("after one failure status = %+v, want up since %v", st, up)
	}

	h.clock.advance(10 * time.Second)
	h.waitScheduled(1)

	down := h.mon.TrunkStatus("a")
	if down.State != settings.TrunkDown {
		t.Fatalf("state = %s, want down", down.State)
	}

	h.clock.advance(10 * time.Second)
	h.waitScheduled(1)

	if s := h.state("a"); s != settings.TrunkDown {
		t.Fatalf("still failing: state = %s, want down", s)
	}

	sender.answer(200, nil)
	h.clock.advance(10 * time.Second)
	h.waitScheduled(1)

	st := h.mon.TrunkStatus("a")
	if st.State != settings.TrunkUp || !st.Since.Equal(h.clock.Now()) || st.Since.Equal(down.Since) {
		t.Fatalf("status = %+v, want up since %v", st, h.clock.Now())
	}

	if g := h.gauge("a"); g != 1 {
		t.Fatalf("trunk_up = %v, want 1", g)
	}
}

func TestSecondServerAnswers(t *testing.T) {
	sender := &fakeSender{}
	sender.use(func(_ context.Context, to sip.Server) (int, error) {
		if to.Addr.Addr() == netip.MustParseAddr("192.0.2.1") {
			return 0, context.DeadlineExceeded
		}

		return 200, nil
	})

	h := start(t, sender, 1, trunk("a", 30*time.Second))
	h.waitScheduled(1)

	if s := h.state("a"); s != settings.TrunkUp {
		t.Fatalf("state = %s, want up", s)
	}
}

func TestSecondAddressAnswers(t *testing.T) {
	tr := trunk("a", 30*time.Second)
	tr.Addresses = []settings.TrunkAddress{{Host: "unresolvable.example"}, {Host: "198.51.100.7", Port: 5080}}

	sender := newFakeSender(200)
	h := start(t, sender, 1, tr)
	h.waitScheduled(1)

	if s := h.state("a"); s != settings.TrunkUp {
		t.Fatalf("state = %s, want up", s)
	}

	if to := sender.all()[0].to.Addr.String(); to != "198.51.100.7:5080" {
		t.Fatalf("sent to %s, want 198.51.100.7:5080", to)
	}
}

func TestNotProbed(t *testing.T) {
	off := trunk("off", 0)
	disabled := trunk("disabled", 30*time.Second)
	disabled.Enabled = false

	sender := newFakeSender(200)
	h := start(t, sender, 1, off, disabled, trunk("on", 30*time.Second))
	h.waitScheduled(1)

	for _, id := range []string{"off", "disabled"} {
		if st := h.mon.TrunkStatus(id); st.State != settings.TrunkUnknown || !st.Since.IsZero() {
			t.Errorf("%s: status = %+v, want unknown", id, st)
		}
	}

	if n := sender.count(); n != 1 {
		t.Fatalf("%d OPTIONS sent, want 1", n)
	}

	if n := h.series(); n != 1 {
		t.Fatalf("%d trunk_up series, want 1", n)
	}
}

func TestSettingsChanges(t *testing.T) {
	sender := newFakeSender(200)
	h := start(t, sender, 1, trunk("a", 0))

	if d := h.clock.pending(); len(d) != 0 || sender.count() != 0 {
		t.Fatalf("keepalive off: %d probes scheduled, %d sent", len(d), sender.count())
	}

	h.settings.setTrunks(trunk("a", 30*time.Second))
	h.waitScheduled(1)

	if s := h.state("a"); s != settings.TrunkUp {
		t.Fatalf("keepalive on: state = %s, want up", s)
	}

	h.clock.advance(10 * time.Second)
	h.settings.setTrunks(trunk("a", 15*time.Second))
	eventually(t, "the probe rescheduled", func() bool {
		d := h.clock.pending()
		return len(d) == 1 && d[0] == 5*time.Second
	})

	if s := h.state("a"); s != settings.TrunkUp {
		t.Fatalf("interval changed: state = %s, want up", s)
	}

	renamed := trunk("a", 15*time.Second)
	renamed.Name = "renamed"
	h.settings.setTrunks(renamed)
	time.Sleep(10 * time.Millisecond)

	if d := h.clock.pending(); len(d) != 1 || d[0] != 5*time.Second || sender.count() != 1 {
		t.Fatalf("renamed: probes in %v, %d sent", d, sender.count())
	}

	sender.answer(0, context.DeadlineExceeded)

	moved := trunk("a", 15*time.Second)
	moved.Addresses = []settings.TrunkAddress{{Host: "198.51.100.7"}}
	h.settings.setTrunks(moved)
	eventually(t, "the new address probed", func() bool { return h.state("a") == settings.TrunkDown })

	if to := sender.all()[1].to.Addr.String(); to != "198.51.100.7:5060" {
		t.Fatalf("sent to %s, want 198.51.100.7:5060", to)
	}

	h.waitScheduled(1)

	disabled := moved
	disabled.Enabled = false
	h.settings.setTrunks(disabled)
	eventually(t, "the trunk forgotten", func() bool { return h.state("a") == settings.TrunkUnknown })

	if d := h.clock.pending(); len(d) != 0 {
		t.Fatalf("disabled: probes in %v", d)
	}

	if n := h.series(); n != 0 {
		t.Fatalf("disabled: %d trunk_up series, want none", n)
	}

	sender.answer(200, nil)
	h.settings.setTrunks(disabled, trunk("b", 30*time.Second))
	h.waitScheduled(1)

	if s := h.state("b"); s != settings.TrunkUp {
		t.Fatalf("added: state = %s, want up", s)
	}
}

func TestDeletedTrunkForgotten(t *testing.T) {
	sender := newFakeSender(200)
	h := start(t, sender, 1, trunk("a", 30*time.Second), trunk("b", 30*time.Second))
	h.waitScheduled(2)

	if n := h.series(); n != 2 {
		t.Fatalf("%d trunk_up series, want 2", n)
	}

	h.settings.setTrunks(trunk("b", 30*time.Second))
	eventually(t, "trunk a forgotten", func() bool { return h.state("a") == settings.TrunkUnknown })

	if n := h.series(); n != 1 {
		t.Fatalf("%d trunk_up series, want 1", n)
	}

	if g := h.gauge("b"); g != 1 {
		t.Fatalf("trunk_up of b = %v, want 1", g)
	}

	if d := h.clock.pending(); len(d) != 1 {
		t.Fatalf("probes in %v, want one", d)
	}
}

func TestDeletedDuringProbe(t *testing.T) {
	release := make(chan struct{})
	sender := &fakeSender{}
	sender.use(func(ctx context.Context, _ sip.Server) (int, error) {
		select {
		case <-release:
			return 200, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})

	h := start(t, sender, 1, trunk("a", 30*time.Second))
	eventually(t, "a probe in flight", func() bool { return sender.count() == 1 })

	h.settings.setTrunks()
	eventually(t, "the probe cancelled", func() bool {
		sender.mu.Lock()
		defer sender.mu.Unlock()

		return sender.active == 0
	})
	close(release)
	time.Sleep(10 * time.Millisecond)

	if s := h.state("a"); s != settings.TrunkUnknown {
		t.Fatalf("state = %s, want unknown", s)
	}

	if d := h.clock.pending(); len(d) != 0 {
		t.Fatalf("probes in %v", d)
	}

	if n := h.series(); n != 0 {
		t.Fatalf("%d trunk_up series, want none", n)
	}
}

func TestBoundedConcurrency(t *testing.T) {
	release := make(chan struct{})
	sender := &fakeSender{}
	sender.use(func(context.Context, sip.Server) (int, error) {
		<-release
		return 200, nil
	})

	var trunks []settings.Trunk
	for i := range 6 {
		trunks = append(trunks, trunk(fmt.Sprint(i), 30*time.Second))
	}

	h := &harness{t: t, settings: newFakeSettings(trunks...), sender: sender, clock: newFakeClock(), metrics: NewMetrics()}

	mon, err := Start(Config{
		Settings: h.settings, Resolve: fakeResolve, Sender: sender, Metrics: h.metrics, Clock: h.clock, MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	h.mon = mon

	eventually(t, "two probes in flight", func() bool { return sender.count() == 2 })
	time.Sleep(20 * time.Millisecond)

	if n := sender.count(); n != 2 {
		t.Fatalf("%d probes in flight, want 2", n)
	}

	close(release)
	h.waitScheduled(6)

	sender.mu.Lock()
	most := sender.most
	sender.mu.Unlock()

	if most != 2 {
		t.Fatalf("at most %d probes in flight, want 2", most)
	}

	if err := mon.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCancelsProbes(t *testing.T) {
	sender := &fakeSender{}
	sender.use(func(ctx context.Context, _ sip.Server) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})

	s := newFakeSettings(trunk("a", 30*time.Second))

	mon, err := Start(Config{Settings: s, Resolve: fakeResolve, Sender: sender, Clock: newFakeClock()})
	if err != nil {
		t.Fatal(err)
	}

	eventually(t, "a probe in flight", func() bool { return sender.count() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mon.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	s.setTrunks(trunk("b", 30*time.Second))
	time.Sleep(10 * time.Millisecond)

	if n := sender.count(); n != 1 {
		t.Fatalf("%d OPTIONS sent after shutdown, want 1", n)
	}
}

func TestConcurrentAccess(t *testing.T) {
	var calls atomic.Int64

	sender := &fakeSender{}
	sender.use(func(context.Context, sip.Server) (int, error) {
		if calls.Add(1)%3 == 0 {
			return 0, errors.New("connection refused")
		}

		return 200, nil
	})

	s := newFakeSettings()
	metrics := NewMetrics()

	mon, err := Start(Config{Settings: s, Resolve: fakeResolve, Sender: sender, Metrics: metrics, Failures: 1})
	if err != nil {
		t.Fatal(err)
	}

	ids := []string{"a", "b", "c", "d"}
	stop := make(chan struct{})

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				for _, id := range ids {
					_ = settings.StatusOf(mon, id).Usable()
				}

				_ = testutil.CollectAndCount(metrics)
			}
		})
	}

	for i := range 200 {
		var trunks []settings.Trunk

		for j, id := range ids {
			if (i+j)%3 == 0 {
				continue
			}

			tr := trunk(id, time.Duration(1+(i+j)%3)*time.Millisecond)
			if (i+j)%5 == 0 {
				tr.Addresses = []settings.TrunkAddress{{Host: "198.51.100.7"}}
			}

			trunks = append(trunks, tr)
		}

		s.setTrunks(trunks...)
		time.Sleep(200 * time.Microsecond)
	}

	close(stop)
	wg.Wait()

	if err := mon.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	if calls.Load() == 0 {
		t.Fatal("no OPTIONS sent")
	}
}

type fakeDNS struct {
	naptr map[string][]sip.NAPTR
	srv   map[string][]*net.SRV
	hosts map[string]netip.Addr
}

func (d fakeDNS) LookupNAPTR(_ context.Context, name string) ([]sip.NAPTR, error) {
	return d.naptr[name], nil
}

func (d fakeDNS) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	key := name
	if service != "" {
		key = "_" + service + "._" + proto + "." + name
	}

	if r, ok := d.srv[key]; ok {
		return key, r, nil
	}

	return "", nil, &net.DNSError{Err: "no such host", Name: key, IsNotFound: true}
}

func (d fakeDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := d.hosts[host]; ok {
		return []netip.Addr{a}, nil
	}

	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestTransportsPerTrunk(t *testing.T) {
	dns := fakeDNS{
		naptr: map[string][]sip.NAPTR{
			"tls.example": {{Order: 10, Flags: "s", Service: "SIPS+D2T", Replacement: "_sips._tcp.tls.example."}},
			"both.example": {
				{Order: 10, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.both.example."},
				{Order: 20, Flags: "s", Service: "SIPS+D2T", Replacement: "_sips._tcp.both.example."},
			},
		},
		srv: map[string][]*net.SRV{
			"_sip._udp.udp.example":   {{Target: "edge.udp.example", Port: 5060}},
			"_sips._tcp.tls.example":  {{Target: "edge.tls.example", Port: 5061}},
			"_sip._udp.both.example":  {{Target: "edge.both.example", Port: 5060}},
			"_sips._tcp.both.example": {{Target: "edge.both.example", Port: 5061}},
		},
		hosts: map[string]netip.Addr{
			"udp.example":       netip.MustParseAddr("192.0.2.10"),
			"edge.udp.example":  netip.MustParseAddr("192.0.2.11"),
			"edge.tls.example":  netip.MustParseAddr("192.0.2.12"),
			"edge.both.example": netip.MustParseAddr("192.0.2.13"),
			"tls.example":       netip.MustParseAddr("192.0.2.14"),
			"both.example":      netip.MustParseAddr("192.0.2.15"),
		},
	}

	pinned := settings.TrunkTLS{Pins: [][32]byte{{1}}}

	for _, c := range []struct {
		name    string
		trusted bool
		tls     settings.TrunkTLS
		addr    settings.TrunkAddress
		want    string
		allowed []sip.Transport
	}{
		{
			"untrusted host", false,
			settings.TrunkTLS{},
			settings.TrunkAddress{Host: "udp.example"},
			"UDP 192.0.2.11:5060",
			[]sip.Transport{sip.UDP, sip.TCP},
		},
		{
			"untrusted IP", false,
			settings.TrunkTLS{},
			settings.TrunkAddress{Host: "198.51.100.7"},
			"UDP 198.51.100.7:5060",
			[]sip.Transport{sip.UDP, sip.TCP},
		},
		{
			"untrusted TLS host", false, pinned,
			settings.TrunkAddress{Host: "tls.example"},
			"TLS 192.0.2.12:5061",
			[]sip.Transport{sip.UDP, sip.TCP, sip.TLS},
		},
		{
			"trusted host with UDP SRV only", true,
			settings.TrunkTLS{},
			settings.TrunkAddress{Host: "udp.example"},
			"TCP 192.0.2.10:5060",
			[]sip.Transport{sip.TCP},
		},
		{
			"trusted IP", true,
			settings.TrunkTLS{},
			settings.TrunkAddress{Host: "198.51.100.7"},
			"TCP 198.51.100.7:5060",
			[]sip.Transport{sip.TCP},
		},
		{
			"trusted host and port", true,
			settings.TrunkTLS{},
			settings.TrunkAddress{Host: "udp.example", Port: 5080},
			"TCP 192.0.2.10:5080",
			[]sip.Transport{sip.TCP},
		},
		{
			"untrusted TLS host, UDP first", false, pinned,
			settings.TrunkAddress{Host: "both.example"},
			"UDP 192.0.2.13:5060",
			[]sip.Transport{sip.UDP, sip.TCP, sip.TLS},
		},
		{
			"untrusted host, TLS not trusted", false,
			settings.TrunkTLS{},
			settings.TrunkAddress{Host: "tls.example"},
			"UDP 192.0.2.14:5060",
			[]sip.Transport{sip.UDP, sip.TCP},
		},
		{
			"trusted TLS host", true, pinned,
			settings.TrunkAddress{Host: "both.example"},
			"TLS 192.0.2.13:5061",
			[]sip.Transport{sip.TCP, sip.TLS},
		},
		{
			"trusted TLS host without NAPTR", true, pinned,
			settings.TrunkAddress{Host: "udp.example"},
			"TCP 192.0.2.10:5060",
			[]sip.Transport{sip.TCP, sip.TLS},
		},
		{
			"TLS required host", false,
			settings.TrunkTLS{Required: true, Pins: pinned.Pins},
			settings.TrunkAddress{Host: "both.example"},
			"TLS 192.0.2.13:5061",
			[]sip.Transport{sip.TLS},
		},
		{
			"TLS required IP", true,
			settings.TrunkTLS{Required: true, Pins: pinned.Pins},
			settings.TrunkAddress{Host: "198.51.100.7", Transport: settings.SIPTransportTLS},
			"TLS 198.51.100.7:5061",
			[]sip.Transport{sip.TLS},
		},
		{
			"TLS required IP without transport", true,
			settings.TrunkTLS{Required: true, Pins: pinned.Pins},
			settings.TrunkAddress{Host: "198.51.100.7"},
			"TLS 198.51.100.7:5061",
			[]sip.Transport{sip.TLS},
		},
		{
			"explicit transport", false,
			settings.TrunkTLS{},
			settings.TrunkAddress{Host: "198.51.100.7", Transport: settings.SIPTransportTCP},
			"TCP 198.51.100.7:5060",
			[]sip.Transport{sip.UDP, sip.TCP},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := trunk("a", 30*time.Second)
			tr.Trusted, tr.TLS, tr.Addresses = c.trusted, c.tls, []settings.TrunkAddress{c.addr}

			var (
				mu   sync.Mutex
				seen [][]sip.Transport
			)

			resolve := func(ctx context.Context, u sip.URI, transports []sip.Transport) ([]sip.Server, error) {
				mu.Lock()

				seen = append(seen, slices.Clone(transports))
				mu.Unlock()

				return (&sip.Resolver{DNS: dns, Transports: transports}).Resolve(ctx, u)
			}

			sender := newFakeSender(200)
			clock := newFakeClock()

			mon, err := Start(Config{Settings: newFakeSettings(tr), Resolve: resolve, Sender: sender, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = mon.Shutdown(context.Background()) }()

			eventually(t, "a probe", func() bool { return len(clock.pending()) == 1 })

			all := sender.all()
			if len(all) != 1 {
				t.Fatalf("%d OPTIONS sent, want 1", len(all))
			}

			if got := string(all[0].to.Transport) + " " + all[0].to.Addr.String(); got != c.want {
				t.Fatalf("probed %s, want %s", got, c.want)
			}

			mu.Lock()
			defer mu.Unlock()

			if !slices.Equal(seen[0], c.allowed) {
				t.Fatalf("resolved with %v, want %v", seen[0], c.allowed)
			}
		})
	}
}

func TestTLSPreferredWithoutTLSListener(t *testing.T) {
	dns := fakeDNS{
		naptr: map[string][]sip.NAPTR{
			"carrier.example": {
				{Order: 10, Flags: "s", Service: "SIPS+D2T", Replacement: "_sips._tcp.carrier.example."},
				{Order: 20, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.carrier.example."},
			},
		},
		srv: map[string][]*net.SRV{
			"_sips._tcp.carrier.example": {{Target: "edge.carrier.example", Port: 5061}},
			"_sip._udp.carrier.example":  {{Target: "edge.carrier.example", Port: 5060}},
		},
		hosts: map[string]netip.Addr{"edge.carrier.example": netip.MustParseAddr("192.0.2.13")},
	}

	tr := trunk("a", 30*time.Second)
	tr.Addresses = []settings.TrunkAddress{{Host: "carrier.example"}}
	tr.TLS = settings.TrunkTLS{Pins: [][32]byte{{1}}}

	sender := newFakeSender(200)
	sender.noTLS = true
	sender.use(func(_ context.Context, to sip.Server) (int, error) {
		if to.Transport == sip.TLS {
			return 0, errors.New("no TLS listener")
		}

		return 200, nil
	})

	resolve := func(ctx context.Context, u sip.URI, trs []sip.Transport) ([]sip.Server, error) {
		return (&sip.Resolver{DNS: dns, Transports: trs}).Resolve(ctx, u)
	}

	clock := newFakeClock()

	mon, err := Start(Config{Settings: newFakeSettings(tr), Resolve: resolve, Sender: sender, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = mon.Shutdown(context.Background()) }()

	eventually(t, "a probe", func() bool { return len(clock.pending()) == 1 })

	for _, s := range sender.all() {
		if s.to.Transport == sip.TLS {
			t.Fatalf("probed %s %s without a TLS listener", s.to.Transport, s.to.Addr)
		}
	}

	if st := mon.TrunkStatus("a").State; st != settings.TrunkUp {
		t.Fatalf("state %s, want up", st)
	}
}
