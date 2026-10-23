//go:build linux

package mcptt

// Load benchmarks at the scale of PM 12: the server of start, in direct mode [PM 14], and its users, each a client
// on an agent of its own, in the same process over UDP on loopback. Each user has an address of its own in
// 127.1.0.0/16, and the media relay 127.0.0.2: Linux routes all of 127.0.0.0/8 to loopback.
//
// Run them at the PM 12 target, 200 concurrent group calls of 100 members, of the most users the server serves
// (settings.MaxUsers, 1,000), with a fixed number of operations, so that the percentiles of each run compare:
//
//	go test ./cmd/mcptt -run '^$' -bench '^BenchmarkRegistration$' -benchtime 1000x -count 3
//	go test ./cmd/mcptt -run '^$' -bench '^BenchmarkGroupCalls$' -benchtime 1x -count 3
//
// The group calls hold about 100,000 sockets: raise the limit of open files first (ulimit -n 200000).
//
// Each benchmark reports its throughput and the percentiles of its latencies in milliseconds: the MCPTT access time
// (KPI 1, TS 22.179 §6.15.3.1: from the request to speak to the signal to start speaking) of the call setup, a
// call's first request, and of a request within the call established (R-6.15.3.2-012a); and the server's share of
// the mouth-to-ear latency (KPI 3): from the RTP a talker sends until the kernel receives it at the members, through
// the relay. They are measured, not gated (PM 12). The users share the process, its CPUs and its scheduler with the
// server: their load is in the server's latencies.

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
	benchUsers     = flag.Int("mcpttbench.users", settings.MaxUsers, "registered users of the load benchmarks, a multiple of -mcpttbench.group-size")
	benchCalls     = flag.Int("mcpttbench.calls", 200, "concurrent group calls of BenchmarkGroupCalls, each op one round of them, so run it with -benchtime 1x or Nx")
	benchGroupSize = flag.Int("mcpttbench.group-size", 100, "members of each group")
	benchTalk      = flag.Duration("mcpttbench.talk", time.Second, "how long the relay latency is sampled in each round, every call with a talker")
	// The burst sets up about 230 calls/s on 20 threads of a Core Ultra 7 265K: 100/s is well within it.
	benchSetupInterval = flag.Duration("mcpttbench.setup-interval", 10*time.Millisecond, "time between two call setups of the paced variant of BenchmarkGroupCalls")
	// What the SIP stack keeps of the transactions and sessions of a round lasts up to 64·T1 (RFC 3261 §17), over a
	// gigabyte at the PM 12 scale: the next round starts once it is gone, so that each starts from a settled server.
	benchSettle = flag.Duration("mcpttbench.settle", 64*transaction.DefaultT1, "pause after each round of BenchmarkGroupCalls")
)

const (
	perfTimeout = 2 * time.Minute
	benchDomain = "mcptt.example.com"
	benchPSI    = "sip:mcptt-participating@" + benchDomain
	// benchWorkers are the users registering at once.
	benchWorkers = 64
)

// The addresses of the benchmarks: the server's SIP on 127.0.0.1, its media relay on 127.0.0.2, its control port
// and the range of its legs' ports below and above the ephemeral ones, for the two ports of each of the 20,000 legs;
// each user's SIP on 127.1.x.y:5060, its media in a range of four ports per call (client.Config).
var benchRelay = netip.MustParseAddr("127.0.0.2")

const (
	benchControlPort = 10000
	benchMinPort     = 10002
	benchMaxPort     = 60999
	benchUserMinPort = 30000
	benchUserMaxPort = 30099
	benchUserSIPPort = 5060
	// benchUserCalls are the calls a user's port range holds.
	benchUserCalls = (benchUserMaxPort - benchUserMinPort + 1) / 4
)

// userAddr is the address of user i.
func userAddr(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{127, 1, byte((i + 1) >> 8), byte(i + 1)})
}

func userID(i int) string { return fmt.Sprintf("sip:u%d@%s", i, benchDomain) }

func groupID(g int) string { return fmt.Sprintf("sip:g%d@%s", g, benchDomain) }

// scale is the population of a benchmark: users, and calls groups of size members. User (r, c), of index
// r·size+c, is the member c of the groups (j, s) with r = s + j·c mod users/size, so that each user is a member of
// calls·size/users groups, with other members in each.
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

// members are the users of group g.
func (sc scale) members(g int) []int {
	rows := sc.users / sc.size
	j, s := g/rows, g%rows

	out := make([]int, sc.size)
	for c := range out {
		out[c] = (s+j*c)%rows*sc.size + c
	}

	return out
}

// config is the configuration of the AS: in direct mode on SIP port sipPort, its settings in d.
func (sc scale) config(sipPort int, d *db.DB, logger *slog.Logger) Config {
	return Config{
		MCPTT: config.MCPTT{
			Mode: config.MCPTTModeDirect, Port: sipPort,
			Media: config.MCPTTMedia{Address: benchRelay, MinPort: benchMinPort, MaxPort: benchMaxPort, ControlPort: benchControlPort},
		},
		Addresses: []netip.Addr{loopback},
		DB:        d,
		Logger:    logger,
	}
}

// settings are the settings of the server: every user affiliated to its groups on registration (TS 24.379
// §9.2.2.2.15), each group's call released when its initiator leaves (§6.3.8.1 3).
func (sc scale) settings() settings.Settings {
	s := settings.Settings{
		System: settings.System{ParticipatingPSI: benchPSI, ControllingPSI: "sip:mcptt-controlling@" + benchDomain},
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

// files is the number of open files of the population in its calls: the SIP socket of each user and the three of
// each of its calls (audio, RTCP, control channel), the two of each leg of the relay (audio, RTCP), and a margin for
// the rest of the process.
func (sc scale) files() uint64 {
	return uint64(sc.users + sc.calls*sc.size*(3+2) + 1000)
}

// load is the server and its users.
type load struct {
	sc     scale
	as     *AS
	db     *db.DB
	server netip.AddrPort
	net    *stampNet
	users  []*benchUser
	groups []*benchGroup
	// byKey are the groups by their settings.Key.
	byKey map[string]*benchGroup

	// joined and ended count the members of the calls of the round established and ended; failure is the first
	// failure of a call or a floor request.
	joined, ended atomic.Int64
	failure       atomic.Pointer[error]
}

type benchUser struct {
	i      int
	agent  *ua.Agent
	client *client.Client
}

// benchGroup is a group and its call of the round: its initiator and second talker, the instants of the requests to
// speak and their grants, in nanoseconds of the monotonic clock of epoch.
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

// newLoad starts the server of sc, its configuration in dir, and makes its users, not registered; close closes them.
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

	// Through the settings, as the API provisions them: each change validated with the rest, and saved.
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

// requireFiles fails unless the process may open n files: Go raises the soft limit to the hard one at start.
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

// newUser is user i: its agent sends every request to the server, its outbound proxy and registrar in direct mode,
// and gives the client the INVITEs; the client is made before the agent listens, which then reads it.
func (l *load) newUser(tb testing.TB, i int, logger *slog.Logger) *benchUser {
	tb.Helper()

	u := &benchUser{i: i}
	local := netip.AddrPortFrom(userAddr(i), benchUserSIPPort)
	contact := sip.Address{URI: sip.URI{Scheme: "sip", Host: local.Addr().String(), Port: local.Port()}}

	agent, err := ua.New(ua.Config{
		Transaction: transaction.Config{Logger: logger},
		Logger:      logger,
		Contact:     contact,
		Prepare: func(req *sip.Request) error {
			req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: l.server}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, local).String())

			return nil
		},
		OnInvite: func(r *ua.Request) ua.Handler { return u.client.OnInvite(r) },
	})
	if err != nil {
		tb.Fatal(err)
	}

	u.agent = agent

	speaker := clienttest.NewSpeaker(agent.Layer().Clock())

	u.client, err = client.New(client.Config{
		Agent: agent, IMPU: mustURI(tb, fmt.Sprintf("sip:u%d@ims.example.com", i)), MCPTTID: mustURI(tb, userID(i)),
		ClientID: fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i), Contact: client.FixedContact(contact),
		Registrar: mustURI(tb, "sip:"+benchDomain), ParticipatingPSI: mustURI(tb, benchPSI),
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

// close closes the users' agents, then the server.
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

// benchLoad is the load of the benchmarks, at the scale of the flags: made by the first benchmark that runs, shared
// by the others and by each run of each (-count), and closed by TestMain. What the SIP stack keeps of the
// transactions and sessions of ended calls, for up to 64·T1 (RFC 3261 §17), keeps a closed load in memory as long:
// a load per run would pile them up.
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

// event is an event of user u's client: of a call of the round, by its group.
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

// register registers user i for expires, or unregisters it if zero, and returns the response's error.
func (l *load) register(i int, expires time.Duration) error {
	c := l.users[i].client

	send := c.Unregister
	if expires > 0 {
		send = func(done func(*sip.Response, error)) error { return c.Register(expires, done) }
	}

	done := make(chan error, 1)

	if err := send(func(res *sip.Response, err error) {
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

// registerAll registers n users in turn for expires, or unregisters them if zero, from benchWorkers at once, and
// returns how long each took.
func (l *load) registerAll(n int, expires time.Duration) ([]time.Duration, error) {
	return runWorkers(n, benchWorkers, func(i int) error { return l.register(i%len(l.users), expires) })
}

// waitFor waits until cond holds, or a call failed.
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

// round is one round of the group calls, the timer running during their setup: the initiators place their calls
// interval apart, at once if zero, an implicit floor request granted in the 200 (TS 24.380 §14.2.5, §14.3.4), the
// members answering automatically (TS 24.379 §6.2.3.1). Once every member is in, the initiators talk for the talk
// time while the relay latency is sampled, then release the floor; the second talker of each call requests it once
// told it is idle; then the initiators leave, which releases the calls. It returns how long the setup took, from the
// first INVITE to the last grant.
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

		c, err := l.users[g.initiator].client.Call(g.uri)
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
		g.call.Leave()
	}

	l.waitFor(tb, fmt.Sprintf("%d members out of the calls", members), func() bool { return l.ended.Load() == members })

	timer.StartTimer()

	return time.Duration(last - first)
}

// stampNet is the media network of the users, UDP sockets as media.UDP's: each RTP packet a talker sends is stamped
// with the instant it is sent, in the speech bits of its AMR-WB frame, which the relay forwards as they are
// (media.Call); each stamped packet a member receives while recording is a sample of the latency of the relay, until
// the kernel received it (SO_TIMESTAMPNS), so that the time the member takes to read it is not counted.
type stampNet struct {
	recording atomic.Bool
	shards    [64]shard
}

type shard struct {
	mu sync.Mutex
	d  []time.Duration
}

// stampMagic marks a stamp: after the 2 octets of the CMR and ToC of the frame (RFC 4867 §4.3, §4.4), 4 octets of
// magic and 8 of the instant, in nanoseconds of the Unix time, the clock of the kernel's timestamps.
const (
	stampMagic  = 0x4d435054
	stampOffset = 2
	stampSize   = 12
)

// stampBuffer is the read buffer of a socket: of the largest datagram of a call, RTP of one frame or RTCP.
const stampBuffer = 2048

func (n *stampNet) record(on bool) { n.recording.Store(on) }

// take returns the samples recorded and forgets them.
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

// timestamps has the kernel timestamp each datagram c receives (socket(7) SO_TIMESTAMPNS).
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

// received is the kernel's timestamp of a datagram, of its control messages, in nanoseconds of the Unix time.
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

// stamp is the instant a stamped RTP packet was sent.
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

// WriteTo sends b, a copy of it stamped if it is RTP with a frame long enough.
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

// runWorkers calls f for each index below n from the given number of concurrent workers, and returns how long each
// call took. It stops at the first error.
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

// samples are latencies by what they measure.
type samples map[string][]time.Duration

func (s samples) add(what string, d time.Duration) {
	s[what] = append(s[what], d)
}

// report reports the 50th, 90th and 99th percentiles of each, by nearest rank, in milliseconds; a percentile of too
// few samples is not.
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

// BenchmarkRegistration measures how fast users register (TS 24.379 §7.2.1, §7.3.2), each affiliated to its groups
// on registration, from benchWorkers at once: an op is one REGISTER and its 200, of the users in turn, the first of
// each a new registration and the others refreshes, the users unregistered after. Run it with -benchtime 1000x to
// register each of 1,000 users once.
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

// BenchmarkGroupCalls measures the setup of concurrent prearranged group calls (TS 24.379 §10.1.1) of every
// registered user, and the media and floor control of the calls: an op is one round of them (load.round), its time
// their setup. The calls are placed at once (burst), the most the server sets up, then -mcpttbench.setup-interval
// apart (paced), the KPIs applying below 70% of a node's capacity (TS 22.179 R-6.15.3.2-005). Each reports the
// setups per second, the access time of the setups (access) and of the second talkers' requests (ptt), and the
// relay latency (relay). The users register before, and unregister after.
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

// The benchmarks' load at a small scale: each user registers, and two rounds of calls run.
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
