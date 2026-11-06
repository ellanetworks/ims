//go:build linux

package mcptt

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"golang.org/x/sys/unix"
)

var (
	benchUsers         = flag.Int("mcpttbench.users", settings.MaxUsers, "registered users of the load benchmarks, a multiple of -mcpttbench.group-size")
	benchCalls         = flag.Int("mcpttbench.calls", 200, "concurrent group calls of BenchmarkGroupCalls, each op one round of them, so run it with -benchtime 1x or Nx")
	benchGroupSize     = flag.Int("mcpttbench.group-size", 100, "members of each group")
	benchTalk          = flag.Duration("mcpttbench.talk", time.Second, "how long the relay latency is sampled in each round, every call with a talker")
	benchSetupInterval = flag.Duration("mcpttbench.setup-interval", 10*time.Millisecond, "time between two call setups of the paced variant of BenchmarkGroupCalls")
	benchSettle        = flag.Duration("mcpttbench.settle", 64*transaction.DefaultT1, "pause after each round of BenchmarkGroupCalls")
)

const (
	perfTimeout     = 2 * time.Minute
	benchDomain     = "mcptt.example.com"
	benchHomeDomain = "ims.mnc001.mcc001.3gppnetwork.org"
	benchWorkers    = 64
)

var benchRelay = netip.MustParseAddr("127.0.0.2")

const (
	benchControlPort = 10000
	benchMinPort     = 10002
	benchMaxPort     = 60999
	benchUserMinPort = 30000
	benchUserMaxPort = 30099
	benchUserSIPPort = 5060
	benchUserCalls   = (benchUserMaxPort - benchUserMinPort + 1) / 4
)

func userAddr(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{127, 1, byte((i + 1) >> 8), byte(i + 1)})
}

func userID(i int) string { return fmt.Sprintf("sip:u%d@%s", i, benchDomain) }

func groupID(g int) string { return fmt.Sprintf("sip:g%d@%s", g, benchDomain) }

type scale struct {
	users, calls, size int
}

func benchScale(tb testing.TB) scale {
	tb.Helper()

	sc := scale{users: *benchUsers, calls: *benchCalls, size: *benchGroupSize}

	switch rows := sc.users / max(sc.size, 1); {
	case sc.size < 2 || sc.users < sc.size || sc.users%sc.size != 0 || sc.users > 65000:
		tb.Fatalf("-mcpttbench.users %d: not a multiple of -mcpttbench.group-size %d of at least 2, or more than 65000",
			sc.users, sc.size)
	case sc.calls < 1 || (sc.calls+rows-1)/rows > benchUserCalls || sc.calls*sc.size*2 > benchMaxPort-benchMinPort+1:
		tb.Fatalf("-mcpttbench.calls %d: none, more than %d per user, or more legs than the relay's ports",
			sc.calls, benchUserCalls)
	}

	return sc
}

func (sc scale) members(g int) []int {
	rows := sc.users / sc.size
	j, s := g/rows, g%rows

	out := make([]int, sc.size)
	for c := range out {
		out[c] = (s+j*c)%rows*sc.size + c
	}

	return out
}

func (sc scale) config(sipPort int, d *db.DB, logger *slog.Logger) Config {
	return Config{
		MCPTT: config.MCPTT{
			Mode: config.MCPTTModeDirect, Port: sipPort,
			Media: config.MCPTTMedia{Address: benchRelay, MinPort: benchMinPort, MaxPort: benchMaxPort, ControlPort: benchControlPort},
		},
		Addresses:  []netip.Addr{loopback},
		HomeDomain: benchHomeDomain,
		DB:         d,
		Logger:     logger,
	}
}

func (sc scale) settings() settings.Settings {
	s := settings.Settings{
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain: benchDomain,
			OnNetwork: settings.OnNetwork{
				FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)},
			},
		},
	}

	groups := make([][]string, sc.users)

	for g := range sc.calls {
		grp := settings.Group{URI: groupID(g), InviteMembers: true, TNG3: 10 * time.Minute, ReleaseOnInitiatorLeave: true}

		for _, u := range sc.members(g) {
			grp.List.Entries = append(grp.List.Entries, settings.Entry{URI: userID(u)})
			groups[u] = append(groups[u], groupID(g))
		}

		s.Groups = append(s.Groups, grp)
	}

	for u := range sc.users {
		s.Users = append(s.Users, settings.User{
			MCPTTUserID: userID(u), IMPUs: []string{fmt.Sprintf("sip:u%d@ims.example.com", u)},
			ImplicitAffiliations: groups[u],
		})
	}

	return s
}

func (sc scale) files() uint64 {
	return uint64(sc.users + sc.calls*sc.size*(3+2) + 1000)
}

type load struct {
	sc     scale
	as     *AS
	db     *db.DB
	server netip.AddrPort
	net    *stampNet
	users  []*benchUser
	groups []*benchGroup
	byKey  map[string]*benchGroup

	joined, ended atomic.Int64
	failure       atomic.Pointer[error]
}

type benchUser struct {
	i      int
	agent  *ua.Agent
	client *client.Client
}

type benchGroup struct {
	uri               sip.URI
	initiator, second int
	call              *client.Call

	secondCall          atomic.Pointer[client.Call]
	start, pttStart     int64
	granted, pttGranted atomic.Int64
	secondIdle          atomic.Bool
}

var epoch = time.Now()

func now() int64 { return int64(time.Since(epoch)) }

func newLoad(tb testing.TB, sc scale, dir string) *load {
	tb.Helper()

	requireFiles(tb, sc.files())

	port := freePort(tb)

	d, err := db.Open(context.Background(), filepath.Join(dir, "ims.db"))
	if err != nil {
		tb.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))

	as, err := Start(context.Background(), sc.config(port, d, logger))
	if err != nil {
		_ = d.Close()

		tb.Fatal(err)
	}

	provisioning := time.Now()

	if err := provision(context.Background(), as.Settings(), sc.settings()); err != nil {
		tb.Fatal(err)
	}

	tb.Logf("provisioned %d users and %d groups of %d in %s", sc.users, sc.calls, sc.size, time.Since(provisioning))

	l := &load{
		sc: sc, as: as, db: d, server: netip.AddrPortFrom(loopback, uint16(port)), net: &stampNet{},
		byKey: map[string]*benchGroup{},
	}

	for g := range sc.calls {
		ms := sc.members(g)
		bg := &benchGroup{uri: mustURI(tb, groupID(g)), initiator: ms[g%sc.size], second: ms[(g+1)%sc.size]}
		l.groups = append(l.groups, bg)
		l.byKey[settings.Key(bg.uri)] = bg
	}

	for i := range sc.users {
		l.users = append(l.users, l.newUser(tb, i, logger))
	}

	return l
}

func requireFiles(tb testing.TB, n uint64) {
	tb.Helper()

	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		tb.Fatal(err)
	}

	if lim.Cur < n {
		tb.Fatalf("%d open files allowed, %d needed: raise the limit (ulimit -n)", lim.Cur, n)
	}
}

func (l *load) newUser(tb testing.TB, i int, logger *slog.Logger) *benchUser {
	tb.Helper()

	u := &benchUser{i: i}
	local := netip.AddrPortFrom(userAddr(i), benchUserSIPPort)
	contact := sip.Address{URI: sip.URI{Scheme: "sip", Host: local.Addr().String(), Port: local.Port()}}

	agent, err := ua.New(ua.Config{
		Transaction: transaction.Config{Logger: logger},
		Logger:      logger,
		Contact:     contact,
		Prepare: func(_ context.Context, req *sip.Request) error {
			req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: l.server}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, local).String())

			return nil
		},
		OnInvite: func(_ context.Context, r *ua.Request) ua.Handler { return u.client.OnInvite(r) },
	})
	if err != nil {
		tb.Fatal(err)
	}

	u.agent = agent

	speaker := clienttest.NewSpeaker(agent.Layer().Clock())

	u.client, err = client.New(client.Config{
		Agent: agent, IMPU: mustURI(tb, fmt.Sprintf("sip:u%d@ims.example.com", i)), MCPTTID: mustURI(tb, userID(i)),
		ClientID: fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i), Contact: client.FixedContact(contact),
		Registrar: mustURI(tb, "sip:"+benchDomain), ParticipatingPSI: mustURI(tb, System(benchHomeDomain).ParticipatingPSI),
		Media: l.net, Addr: local.Addr(), MinPort: benchUserMinPort, MaxPort: benchUserMaxPort,
		Floor:           floor.ParticipantTimers{T100: time.Second, T101: time.Second},
		ImplicitRequest: true, Granted: true,
		OnEvent: func(e client.Event) {
			speaker.Event(e)
			l.event(u, e)
		},
	})
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := agent.Layer().Listen(context.Background(), local); err != nil {
		tb.Fatal(err)
	}

	return u
}

func (l *load) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	for _, u := range l.users {
		wg.Go(func() { _ = u.agent.Shutdown(ctx) })
	}

	wg.Wait()

	return errors.Join(l.as.Shutdown(ctx), l.db.Close())
}

var benchLoad struct {
	l   *load
	dir string
}

func sharedLoad(b *testing.B) *load {
	b.Helper()

	if benchLoad.l == nil {
		dir, err := os.MkdirTemp("", "mcpttbench-")
		if err != nil {
			b.Fatal(err)
		}

		benchLoad.dir = dir
		benchLoad.l = newLoad(b, benchScale(b), dir)
	}

	return benchLoad.l
}

func TestMain(m *testing.M) {
	code := m.Run()

	if l := benchLoad.l; l != nil {
		if err := l.close(); err != nil {
			fmt.Fprintln(os.Stderr, "close the load of the benchmarks:", err)
		}
	}

	if benchLoad.dir != "" {
		_ = os.RemoveAll(benchLoad.dir)
	}

	os.Exit(code)
}

func (l *load) event(u *benchUser, e client.Event) {
	if e.Call == nil {
		return
	}

	g := l.byKey[settings.Key(e.Call.Group())]
	if g == nil {
		return
	}

	switch e.Kind {
	case client.EventFailed:
		err := fmt.Errorf("call of u%d to %s failed: %v %v", u.i, g.uri, e.Response, e.Err)
		l.failure.CompareAndSwap(nil, &err)
	case client.EventEstablished:
		if e.Call.Incoming() {
			if u.i == g.second {
				g.secondCall.Store(e.Call)
			}

			l.joined.Add(1)
		}
	case client.EventEnded:
		if e.Call.Incoming() {
			l.ended.Add(1)
		}
	case client.EventFloor:
		switch {
		case e.Floor.Kind == floor.NoticeGranted && !e.Call.Incoming() && u.i == g.initiator:
			g.granted.CompareAndSwap(0, now())
		case e.Floor.Kind == floor.NoticeGranted && e.Call.Incoming() && u.i == g.second:
			g.pttGranted.CompareAndSwap(0, now())
		case e.Floor.Kind == floor.NoticeIdle && e.Call.Incoming() && u.i == g.second:
			g.secondIdle.Store(true)
		case e.Floor.Kind == floor.NoticeDenied || e.Floor.Kind == floor.NoticeRequestTimeout:
			err := fmt.Errorf("floor request of u%d in %s: notice %d", u.i, g.uri, e.Floor.Kind)
			l.failure.CompareAndSwap(nil, &err)
		}
	}
}

func (l *load) register(i int, expires time.Duration) error {
	c := l.users[i].client

	send := c.Unregister
	if expires > 0 {
		send = func(ctx context.Context, done func(*sip.Response, error)) error {
			return c.Register(ctx, expires, done)
		}
	}

	done := make(chan error, 1)

	if err := send(context.Background(), func(res *sip.Response, err error) {
		if err == nil && res.StatusCode != 200 {
			err = fmt.Errorf("REGISTER of u%d: %d", i, res.StatusCode)
		}

		done <- err
	}); err != nil {
		return err
	}

	select {
	case err := <-done:
		return err
	case <-time.After(perfTimeout):
		return fmt.Errorf("REGISTER of u%d: no response", i)
	}
}

func (l *load) registerAll(n int, expires time.Duration) ([]time.Duration, error) {
	return runWorkers(n, benchWorkers, func(i int) error { return l.register(i%len(l.users), expires) })
}

func (l *load) waitFor(tb testing.TB, what string, cond func() bool) {
	tb.Helper()

	deadline := time.Now().Add(perfTimeout)

	for !cond() {
		if err := l.failure.Load(); err != nil {
			tb.Fatal(*err)
		}

		if time.Now().After(deadline) {
			tb.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(time.Millisecond)
	}
}

type benchTimer interface {
	StartTimer()
	StopTimer()
}

type noTimer struct{}

func (noTimer) StartTimer() {}

func (noTimer) StopTimer() {}

func (l *load) round(tb testing.TB, timer benchTimer, s samples, talk, interval time.Duration) time.Duration {
	tb.Helper()

	timer.StopTimer()

	l.joined.Store(0)
	l.ended.Store(0)

	for _, g := range l.groups {
		g.secondCall.Store(nil)
		g.granted.Store(0)
		g.pttGranted.Store(0)
		g.secondIdle.Store(false)
	}

	calls, size := len(l.groups), l.sc.size

	timer.StartTimer()

	first := now()

	for i, g := range l.groups {
		time.Sleep(time.Duration(first + int64(i)*int64(interval) - now()))

		g.start = now()

		c, err := l.users[g.initiator].client.Call(context.Background(), g.uri)
		if err != nil {
			tb.Fatal(err)
		}

		g.call = c
	}

	l.waitFor(tb, "the floor granted to every initiator", func() bool {
		return !slices.ContainsFunc(l.groups, func(g *benchGroup) bool { return g.granted.Load() == 0 })
	})

	timer.StopTimer()

	last := int64(0)

	for _, g := range l.groups {
		last = max(last, g.granted.Load())
		s.add("access", time.Duration(g.granted.Load()-g.start))
	}

	members := int64(calls * (size - 1))
	l.waitFor(tb, fmt.Sprintf("%d members in the calls", members), func() bool { return l.joined.Load() == members })

	l.net.record(true)
	time.Sleep(talk)
	l.net.record(false)

	for _, d := range l.net.take() {
		s.add("relay", d)
	}

	for _, g := range l.groups {
		g.call.ReleasePTT()
	}

	l.waitFor(tb, "the floor idle at every second talker", func() bool {
		return !slices.ContainsFunc(l.groups, func(g *benchGroup) bool { return !g.secondIdle.Load() })
	})

	for _, g := range l.groups {
		g.pttStart = now()
		g.secondCall.Load().PressPTT()
	}

	l.waitFor(tb, "the floor granted to every second talker", func() bool {
		return !slices.ContainsFunc(l.groups, func(g *benchGroup) bool { return g.pttGranted.Load() == 0 })
	})

	for _, g := range l.groups {
		s.add("ptt", time.Duration(g.pttGranted.Load()-g.pttStart))
		g.secondCall.Load().ReleasePTT()
		g.call.Leave(context.Background())
	}

	l.waitFor(tb, fmt.Sprintf("%d members out of the calls", members), func() bool { return l.ended.Load() == members })

	timer.StartTimer()

	return time.Duration(last - first)
}

type stampNet struct {
	recording atomic.Bool
	shards    [64]shard
}

type shard struct {
	mu sync.Mutex
	d  []time.Duration
}

const (
	stampMagic  = 0x4d435054
	stampOffset = 2
	stampSize   = 12
)

const stampBuffer = 2048

func (n *stampNet) record(on bool) { n.recording.Store(on) }

func (n *stampNet) take() []time.Duration {
	var out []time.Duration

	for i := range n.shards {
		sh := &n.shards[i]
		sh.mu.Lock()
		out = append(out, sh.d...)
		sh.d = nil
		sh.mu.Unlock()
	}

	return out
}

func (n *stampNet) Listen(addr netip.AddrPort, h func([]byte, netip.AddrPort)) (media.Conn, error) {
	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(addr))
	if err != nil {
		return nil, err
	}

	if err := timestamps(c); err != nil {
		return nil, errors.Join(err, c.Close())
	}

	sh := &n.shards[int(addr.Port())%len(n.shards)]
	done := make(chan struct{})

	go func() {
		defer close(done)

		buf, oob := make([]byte, stampBuffer), make([]byte, 64)

		for {
			nb, noob, _, from, err := c.ReadMsgUDPAddrPort(buf, oob)
			if err != nil {
				return
			}

			if at, ok := stamp(buf[:nb]); ok && n.recording.Load() {
				if rx, ok := received(oob[:noob]); ok {
					sh.mu.Lock()
					sh.d = append(sh.d, time.Duration(rx-at))
					sh.mu.Unlock()
				}
			}

			h(buf[:nb], netip.AddrPortFrom(from.Addr().Unmap(), from.Port()))
		}
	}()

	return stampConn{c, done}, nil
}

func timestamps(c *net.UDPConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}

	var serr error

	if err := raw.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1)
	}); err != nil {
		return err
	}

	return serr
}

func received(oob []byte) (int64, bool) {
	ms, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, false
	}

	for _, m := range ms {
		if m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_TIMESTAMPNS && len(m.Data) >= 16 {
			sec, nsec := binary.NativeEndian.Uint64(m.Data), binary.NativeEndian.Uint64(m.Data[8:])
			return int64(sec)*int64(time.Second) + int64(nsec), true
		}
	}

	return 0, false
}

func stamp(b []byte) (int64, bool) {
	if rtp.IsRTCP(b) {
		return 0, false
	}

	p, err := rtp.Parse(b)
	if err != nil || len(p.Payload) < stampOffset+stampSize {
		return 0, false
	}

	s := p.Payload[stampOffset:]
	if binary.BigEndian.Uint32(s) != stampMagic {
		return 0, false
	}

	return int64(binary.BigEndian.Uint64(s[4:])), true
}

type stampConn struct {
	c    *net.UDPConn
	done chan struct{}
}

func (c stampConn) WriteTo(b []byte, to netip.AddrPort) error {
	if !rtp.IsRTCP(b) {
		b = slices.Clone(b)

		if p, err := rtp.Parse(b); err == nil && len(p.Payload) >= stampOffset+stampSize {
			s := p.Payload[stampOffset:]
			binary.BigEndian.PutUint32(s, stampMagic)
			binary.BigEndian.PutUint64(s[4:], uint64(time.Now().UnixNano()))
		}
	}

	_, err := c.c.WriteToUDPAddrPort(b, to)

	return err
}

func (c stampConn) LocalAddr() netip.AddrPort {
	a := c.c.LocalAddr().(*net.UDPAddr).AddrPort()
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

func (c stampConn) Close() error {
	err := c.c.Close()
	<-c.done

	return err
}

func runWorkers(n, workers int, f func(i int) error) ([]time.Duration, error) {
	var (
		next     atomic.Int64
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		failed   atomic.Bool
	)

	took := make([]time.Duration, n)

	for range min(workers, n) {
		wg.Go(func() {
			for !failed.Load() {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}

				start := time.Now()

				if err := f(i); err != nil {
					errOnce.Do(func() { firstErr = err })
					failed.Store(true)

					return
				}

				took[i] = time.Since(start)
			}
		})
	}

	wg.Wait()

	return took, firstErr
}

type samples map[string][]time.Duration

func (s samples) add(what string, d time.Duration) {
	s[what] = append(s[what], d)
}

func (s samples) report(b *testing.B) {
	for what, ds := range s {
		slices.Sort(ds)

		for _, q := range []struct {
			name string
			at   float64
		}{{"p50", 0.50}, {"p90", 0.90}, {"p99", 0.99}} {
			if float64(len(ds))*(1-q.at) < 1 {
				continue
			}

			rank := int(math.Ceil(q.at * float64(len(ds))))
			b.ReportMetric(float64(ds[rank-1])/float64(time.Millisecond), what+"-"+q.name+"-ms")
		}
	}
}

func BenchmarkRegistration(b *testing.B) {
	l := sharedLoad(b)

	b.ResetTimer()

	start := time.Now()

	took, err := l.registerAll(b.N, time.Hour)
	if err != nil {
		b.Fatal(err)
	}

	elapsed := time.Since(start)

	b.StopTimer()

	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "reg/s")
	samples{"reg": took}.report(b)

	if _, err := l.registerAll(min(b.N, len(l.users)), 0); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkGroupCalls(b *testing.B) {
	l := sharedLoad(b)
	sc := l.sc

	if _, err := l.registerAll(sc.users, time.Hour); err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if _, err := l.registerAll(sc.users, 0); err != nil {
			b.Error(err)
		}
	})

	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{{"burst", 0}, {"paced", *benchSetupInterval}} {
		b.Run(tc.name, func(b *testing.B) {
			all := samples{}

			var setup time.Duration

			for b.Loop() {
				setup += l.round(b, b, all, *benchTalk, tc.interval)

				b.StopTimer()
				time.Sleep(*benchSettle)
				b.StartTimer()
			}

			b.ReportMetric(float64(b.N*sc.calls)/setup.Seconds(), "setups/s")
			all.report(b)
		})
	}
}

func TestBenchSmoke(t *testing.T) {
	l := newLoad(t, scale{users: 20, calls: 8, size: 5}, t.TempDir())

	t.Cleanup(func() {
		if err := l.close(); err != nil {
			t.Error(err)
		}
	})

	if _, err := l.registerAll(20, time.Hour); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		s := samples{}
		l.round(t, noTimer{}, s, 100*time.Millisecond, time.Millisecond)

		if len(s["access"]) != 8 || len(s["ptt"]) != 8 || len(s["relay"]) == 0 {
			t.Fatalf("%d access, %d ptt, %d relay samples", len(s["access"]), len(s["ptt"]), len(s["relay"]))
		}
	}
}
