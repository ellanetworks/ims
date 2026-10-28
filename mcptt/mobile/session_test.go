package mobile

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/as"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	serversettings "github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/ue"
)

// platform is the app of a session in the tests: sockets on loopback, no IPsec, its events on a channel.
type platform struct {
	events chan event
	// onEvent, if set, runs on each event, on the session's event goroutine.
	onEvent func(event)

	mu     sync.Mutex
	wakes  map[int64]int64
	awake  []int64
	aka    []byte
	akaErr error
	logs   []int
}

func newPlatform() *platform {
	return &platform{events: make(chan event, 256), wakes: map[int64]int64{}}
}

func (p *platform) BindSocket(int) error                     { return nil }
func (p *platform) AllocateSPI(string) (int64, error)        { return 0, errors.New("no IPsec") }
func (p *platform) ReleaseSPI(int64)                         {}
func (p *platform) InstallSA(string) error                   { return nil }
func (p *platform) ProtectSocket(string, int, int) error     { return nil }
func (p *platform) RemoveSA(string) error                    { return nil }
func (p *platform) Authenticate(_, _ []byte) ([]byte, error) { return p.aka, p.akaErr }

func (p *platform) Log(level int, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.logs = append(p.logs, level)
}

func (p *platform) Wake(id, ms int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.wakes[id] = ms
}

func (p *platform) CancelWake(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.wakes, id)
}

func (p *platform) KeepAwake(ms int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.awake = append(p.awake, ms)
}

func (p *platform) Event(s string) {
	var e event
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return
	}

	if p.onEvent != nil {
		p.onEvent(e)
	}

	p.events <- e
}

// await is the next event of the type (and kind or notice, if not empty), the others before it skipped.
func (p *platform) await(t *testing.T, typ, kind string) event {
	t.Helper()

	timeout := time.After(10 * time.Second)

	for {
		select {
		case e := <-p.events:
			if e.Type == typ && (kind == "" || e.Kind == kind || e.Notice == kind || e.Reason == kind) {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s %s event", typ, kind)
		}
	}
}

// awaitAll are the events of each type:kind of want, in any order, the others skipped.
func (p *platform) awaitAll(t *testing.T, want ...string) map[string]event {
	t.Helper()

	got := map[string]event{}
	timeout := time.After(10 * time.Second)

	for len(got) < len(want) {
		select {
		case e := <-p.events:
			for _, w := range want {
				typ, kind, _ := strings.Cut(w, ":")
				if _, seen := got[w]; !seen && e.Type == typ && (kind == "" || e.Kind == kind || e.Notice == kind || e.Reason == kind) {
					got[w] = e
				}
			}
		case <-timeout:
			t.Fatalf("events %v of %v only", got, want)
		}
	}

	return got
}

// directServer runs the server of the example configuration in direct mode (mcptt plan, PM 14) on loopback, free
// ports, and returns its SIP address.
func directServer(t *testing.T) netip.AddrPort {
	t.Helper()

	addr, _ := restartableServer(t)

	return addr
}

// restartableServer is directServer, and its restart: the server shut down and started anew on its ports, its
// registrations' bindings lost, as of a server restarted.
func restartableServer(t *testing.T) (netip.AddrPort, func()) {
	t.Helper()

	addr, control := serverPorts(t)
	srv := startDirect(t, addr, control)

	restart := func() {
		_ = srv.Shutdown(t.Context())
		srv = startDirect(t, addr, control)
	}

	t.Cleanup(func() { _ = srv.Shutdown(t.Context()) })

	return addr, restart
}

// serverPorts are the server's SIP address and the port of its control channels, free ports of loopback: the SIP port
// free of TCP as of UDP (the server listens on both), the two distinct, all held until both chosen.
func serverPorts(t *testing.T) (netip.AddrPort, int) {
	t.Helper()

	lc := new(net.ListenConfig)

	var addr netip.AddrPort

	for addr.Port() == 0 {
		tl, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = tl.Close() }()

		p := tl.Addr().(*net.TCPAddr).AddrPort()

		ul, err := lc.ListenPacket(t.Context(), "udp", p.String())
		if err != nil {
			continue // the UDP port taken; another, the TCP one held so as not to get it again
		}

		defer func() { _ = ul.Close() }()

		addr = p
	}

	control, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = control.Close() }()

	return addr, control.LocalAddr().(*net.UDPAddr).Port
}

// exampleSettings are alice, bob and carol, members of fire, affiliated to it on registration.
func exampleSettings() serversettings.Settings {
	const fire = "sip:fire@mcptt.example.com"

	return serversettings.Settings{
		System: serversettings.System{
			ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com", ControllingPSI: "sip:mcptt-controlling@mcptt.example.com",
		},
		ServiceConfiguration: serversettings.ServiceConfiguration{
			Domain: "mcptt.example.com",
			OnNetwork: serversettings.OnNetwork{
				NumLevelsPriorityHierarchy: new(4),
				TransmitTime:               serversettings.TransmitTime{TimeLimit: new(30 * time.Second)},
				FCTimersCounters: serversettings.FCTimersCounters{
					T1: new(4 * time.Second), T3: new(3 * time.Second), T7: new(2 * time.Second), T8: new(time.Second), C7: new(10),
				},
			},
		},
		Users: []serversettings.User{
			{MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:alice@ims.example.com"}, ImplicitAffiliations: []string{fire}},
			{
				MCPTTUserID: "sip:bob@mcptt.example.com", IMPUs: []string{"sip:bob@ims.example.com", "tel:+15555550102"},
				ImplicitAffiliations: []string{fire}, MaxAffiliationsN2: new(4), MaxSimultaneousCallsN6: new(2),
			},
			{MCPTTUserID: "sip:carol@mcptt.example.com", IMPUs: []string{"sip:carol@ims.example.com"}, ImplicitAffiliations: []string{fire}},
		},
		Groups: []serversettings.Group{{
			URI: fire, InviteMembers: true, MaxParticipants: 100, Encodings: []string{"AMR-WB"}, T4: new(30 * time.Second),
			TNG3: 10 * time.Minute, TNG1: 5 * time.Second, TNG1Action: serversettings.Proceed,
			List: serversettings.List{Entries: []serversettings.Entry{
				{URI: "sip:alice@mcptt.example.com", UserPriority: 2, Required: true},
				{URI: "sip:bob@mcptt.example.com", UserPriority: 1},
				{URI: "sip:carol@mcptt.example.com", ReceiveOnly: true},
			}},
		}},
	}
}

// fixed is the source of the settings of one snapshot.
type fixed struct{ s *serversettings.Snapshot }

func (f fixed) Current() *serversettings.Snapshot { return f.s }

func startDirect(t *testing.T, addr netip.AddrPort, control int) *server.Server {
	t.Helper()

	snap, err := serversettings.New(exampleSettings())
	if err != nil {
		t.Fatal(err)
	}

	relay, err := media.New(media.Config{
		Network: media.UDP{}, Addr: addr.Addr(), MinPort: 20000, MaxPort: 20999, ControlPort: uint16(control),
	})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := server.New(server.Config{
		UA: ua.Config{
			Transaction: transaction.Config{},
			Prepare:     as.Prepare(t.Context(), []netip.AddrPort{addr}, &sip.Resolver{}),
			Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: "127.0.0.1", Port: addr.Port()}},
		},
		Source: fixed{snap}, Mode: registrar.Direct, Media: relay,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := srv.Agent().Layer().Listen(t.Context(), addr); err != nil {
		t.Fatal(err)
	}

	return srv
}

func testSettings(server netip.AddrPort, name string, i int) settings {
	return settings{
		Version: schemaVersion,
		IMPI:    name + "@ims.example.com", IMPU: "sip:" + name + "@ims.example.com", HomeDomain: "mcptt.example.com",
		IMEI: "3569380356438" + strconv.Itoa(i), PCSCF: server.String(), Local: "127.0.0." + strconv.Itoa(i),
		MCPTTID: "sip:" + name + "@mcptt.example.com", ClientID: "urn:uuid:00000000-0000-1000-8000-00000000000" + strconv.Itoa(i),
		ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com", Groups: []string{"sip:fire@mcptt.example.com"},
		plain: true, noRegEvent: true,
	}
}

// session is the session of user name of the example configuration, at 127.0.0.i, its UE registering with the server
// in direct mode, plain, started and registered.
func session(t *testing.T, server netip.AddrPort, name string, i int, opts ...func(*settings)) (*Session, *platform) {
	t.Helper()

	p := newPlatform()

	set := testSettings(server, name, i)
	for _, o := range opts {
		o(&set)
	}

	s, err := newSession(p, set)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(s.Stop)

	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	if e := p.await(t, "registration", ""); !e.Registered {
		t.Fatalf("registration %+v", e)
	}

	if e := p.await(t, "affiliation", ""); e.Status != 200 {
		t.Fatalf("affiliation %+v", e)
	}

	return s, p
}

// TS 24.379 §7.2.1, §9.2.1.2, §10.1.1.2.1, TS 24.380 §6.2.4: two sessions log on in their registrations and affiliate
// to fire; a call of one, of the app's ID, reaches the other; the caller's speech, once granted the floor, plays at
// the member; the state tells the call and its talker; the caller leaving ends it, locally.
func TestGroupCall(t *testing.T) {
	addr := directServer(t)
	alice, pa := session(t, addr, "alice", 2)
	bob, pb := session(t, addr, "bob", 3)

	if err := alice.Call("", "sip:fire@mcptt.example.com"); !errors.Is(err, errCallID) {
		t.Fatalf("Call without an ID: %v", err)
	}

	if err := alice.Call("a1", "sip:fire@mcptt.example.com"); err != nil {
		t.Fatal(err)
	}

	if err := alice.Call("a1", "sip:fire@mcptt.example.com"); !errors.Is(err, errCallID) {
		t.Fatalf("Call of an ID in use: %v", err)
	}

	pa.await(t, "call", "established")

	in := pb.await(t, "call", "incoming")
	if in.Group != "sip:fire@mcptt.example.com" || in.Caller != "sip:alice@mcptt.example.com" || !strings.HasPrefix(in.Call, "in-") {
		t.Fatalf("invitation %+v", in)
	}

	if err := alice.PressPTT("a1"); err != nil {
		t.Fatal(err)
	}

	if e := pa.await(t, "transmit", ""); e.On == nil || !*e.On || e.Mode == nil || *e.Mode != 2 {
		t.Fatalf("transmit %+v", e)
	}

	frame := clienttest.Silence(2)
	for range 10 {
		if err := alice.SendSpeech("a1", frame); err != nil {
			t.Fatal(err)
		}

		time.Sleep(20 * time.Millisecond)
	}

	// The Floor Taken and the media race (TS 24.380 §6.2.4.4.2 step 7).
	if e := pb.awaitAll(t, "heard", "floor:taken")["heard"]; e.Call != in.Call {
		t.Fatalf("heard %+v", e)
	}

	var state struct {
		Registered bool        `json:"registered"`
		Calls      []callState `json:"calls"`
	}

	if err := json.Unmarshal([]byte(bob.State()), &state); err != nil {
		t.Fatal(err)
	}

	if !state.Registered || len(state.Calls) != 1 || state.Calls[0].ID != in.Call || state.Calls[0].Talker != "sip:alice@mcptt.example.com" {
		t.Fatalf("state %+v", state)
	}

	if got := bob.NextSpeech(in.Call); !bytes.Equal(got, frame) {
		t.Fatalf("bob plays % x, want % x", got, frame)
	}

	if err := alice.ReleasePTT("a1"); err != nil {
		t.Fatal(err)
	}

	if e := pa.await(t, "transmit", ""); e.On == nil || *e.On {
		t.Fatalf("transmit %+v after the release", e)
	}

	if err := alice.Leave("a1"); err != nil {
		t.Fatal(err)
	}

	if e := pa.await(t, "call", "ended"); e.Cause != "local" {
		t.Fatalf("ended %+v", e)
	}

	if err := alice.PressPTT("a1"); !errors.Is(err, errNoCall) {
		t.Fatalf("PressPTT of a call ended: %v", err)
	}

	if got := alice.NextSpeech("a1"); !bytes.Equal(got, noData) {
		t.Fatalf("speech of a call gone % x", got)
	}
}

// TS 24.379 §10.1.1.3.1.1 step 2a: the server restarted, its bindings lost, alice's call is refused with the warning
// 141; the session registers again, which binds her anew, and places the call again under its ID: established, not
// ended. Bob, registered again too, is invited.
func TestRetryUnknown(t *testing.T) {
	saved := minBackoff
	minBackoff = 100 * time.Millisecond

	t.Cleanup(func() { minBackoff = saved })

	// Timer F shortened: a refresh that the restart left unanswered ends sooner.
	fast := func(s *settings) { s.t1 = 20 * time.Millisecond }

	addr, restart := restartableServer(t)
	alice, pa := session(t, addr, "alice", 2, fast)
	bob, pb := session(t, addr, "bob", 3, fast)

	restart()

	// Bob's binding back, as by his own refresh, which affiliates him again. The restart lost the connection the
	// refresh may go out on: written there, it is lost with it, the server restarted knowing nothing of it, and Timer F
	// ends it, or the write fails; either way his session registers again, on a new connection (TS 24.229 §5.1.1.4.1).
	err := bob.currentUE().Reregister(t.Context())
	if err != nil && !errors.Is(err, transaction.ErrTimeout) && !errors.Is(err, transaction.ErrTransport) {
		t.Fatal(err)
	}

	if e := pb.await(t, "affiliation", ""); e.Status != 200 {
		t.Fatalf("bob's affiliation after his re-registration %+v", e)
	}

	if err := alice.Call("a1", "sip:fire@mcptt.example.com"); err != nil {
		t.Fatal(err)
	}

	if e := pa.await(t, "call", ""); e.Kind != "established" || e.Call != "a1" {
		t.Fatalf("call %+v, want a1 established", e)
	}

	pb.await(t, "call", "incoming")

	if err := alice.Leave("a1"); err != nil {
		t.Fatal(err)
	}

	pa.await(t, "call", "ended")
}

// The server shut down during a call, its BYEs end the call at both members (TS 24.379 §6.3.8.1).
func TestServerRestartEndsCalls(t *testing.T) {
	addr, restart := restartableServer(t)
	alice, pa := session(t, addr, "alice", 2)
	_, pb := session(t, addr, "bob", 3)

	if err := alice.Call("a1", "sip:fire@mcptt.example.com"); err != nil {
		t.Fatal(err)
	}

	pa.await(t, "call", "established")
	pb.await(t, "call", "established")

	restart()

	if e := pa.await(t, "call", "ended"); e.Cause != "remote" {
		t.Fatalf("alice's call ended %+v", e)
	}

	if e := pb.await(t, "call", "ended"); e.Cause != "remote" {
		t.Fatalf("bob's call ended %+v", e)
	}
}

// TS 24.229 §5.1.1.7: rejected, the session leaves its calls and registers no more; RFC 3680 §5.1: on probation, it
// registers again after the retry-after.
func TestDeregistrations(t *testing.T) {
	saved := minBackoff
	minBackoff = 100 * time.Millisecond

	t.Cleanup(func() { minBackoff = saved })

	addr := directServer(t)
	alice, pa := session(t, addr, "alice", 2)
	_, pb := session(t, addr, "bob", 3)

	alice.ueEvent(ue.Event{Deregistered: ue.DeregisteredProbation, RetryAfter: 0})

	if e := pa.await(t, "deregistered", ""); e.Reason != "probation" {
		t.Fatalf("deregistered %+v", e)
	}

	if e := pa.await(t, "registration", ""); !e.Registered {
		t.Fatalf("registration %+v after probation", e)
	}

	if err := alice.Call("a1", "sip:fire@mcptt.example.com"); err != nil {
		t.Fatal(err)
	}

	pa.await(t, "call", "established")
	pb.await(t, "call", "established")

	alice.ueEvent(ue.Event{Deregistered: ue.DeregisteredRejected})

	pa.await(t, "deregistered", "rejected")

	if e := pa.awaitAll(t, "call:ended", "stopped")["stopped"]; e.Reason != "rejected" {
		t.Fatalf("stopped %+v", e)
	}
}

// A new address on the MCPTT network, or another network, is a new registration (TS 24.229 §5.1.1.2).
func TestSetNetwork(t *testing.T) {
	addr := directServer(t)
	alice, pa := session(t, addr, "alice", 2)

	if err := alice.SetNetwork("", "127.0.0.2", addr.String(), "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000002"); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-pa.events:
		t.Fatalf("event %+v for another cell", e)
	case <-time.After(100 * time.Millisecond):
	}

	if err := alice.SetNetwork("", "127.0.0.4", addr.String(), ""); err != nil {
		t.Fatal(err)
	}

	if e := pa.await(t, "registration", ""); !e.Registered {
		t.Fatalf("registration %+v from the new address", e)
	}

	// The network lost and back, of the same address.
	if err := alice.SetNetwork("101", "127.0.0.4", addr.String(), ""); err != nil {
		t.Fatal(err)
	}

	if e := pa.await(t, "registration", ""); !e.Registered {
		t.Fatalf("registration %+v on the new network", e)
	}
}

// The events go to the platform one at a time, in order, on a goroutine of their own: Stop from one does not
// deadlock, the last event "stopped".
func TestEvents(t *testing.T) {
	addr := directServer(t)
	p := newPlatform()

	s, err := newSession(p, testSettings(addr, "alice", 2))
	if err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})

	p.onEvent = func(e event) {
		if e.Type == "affiliation" {
			go func() {
				s.Stop()
				close(stopped)
			}()
		}
	}

	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	if err := s.Start(); !errors.Is(err, errStarted) {
		t.Fatalf("second Start: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop from an event did not return")
	}

	e := p.await(t, "stopped", "")
	if e.Version != schemaVersion || e.Reason != "stop" {
		t.Fatalf("last event %+v", e)
	}

	if err := s.Start(); !errors.Is(err, errStopped) {
		t.Fatalf("Start after Stop: %v", err)
	}
}

// Settings of another schema version are refused.
func TestSettingsVersion(t *testing.T) {
	if _, err := NewSession(newPlatform(), `{"version": 2}`); err == nil {
		t.Fatal("settings of version 2 accepted")
	}
}

// TS 31.102 §7.1.2.1: RES, CK, IK (and Kc) after DB; AUTS after DC; 98 62 a MAC failure; another status word, a
// response malformed or of keys of the wrong sizes, or the platform failing, an error of the UE, not a MAC failure.
func TestAuthenticate(t *testing.T) {
	p := newPlatform()
	u := uicc{p}

	res, ck, ik := bytes.Repeat([]byte{1}, 8), bytes.Repeat([]byte{2}, 16), bytes.Repeat([]byte{3}, 16)
	db := append(append(append(append([]byte{0xdb, 8}, res...), 16), ck...), append([]byte{16}, ik...)...)
	p.aka = append(db, 8, 9, 9, 9, 9, 9, 9, 9, 9)

	r, err := u.Authenticate(nil, nil)
	if err != nil || !bytes.Equal(r.RES, res) || !bytes.Equal(r.CK, ck) || !bytes.Equal(r.IK, ik) || r.AUTS != nil {
		t.Fatalf("DB: %+v, %v", r, err)
	}

	auts := bytes.Repeat([]byte{4}, 14)
	p.aka = append([]byte{0xdc, 14}, auts...)

	if r, err := u.Authenticate(nil, nil); err != nil || !bytes.Equal(r.AUTS, auts) {
		t.Fatalf("DC: %+v, %v", r, err)
	}

	p.aka = []byte{0x98, 0x62}

	if _, err := u.Authenticate(nil, nil); !errors.Is(err, ue.ErrMACFailure) {
		t.Fatalf("98 62: %v", err)
	}

	short := append(append([]byte{0xdb, 8}, res...), 8, 1, 2, 3, 4, 5, 6, 7, 8, 16)
	short = append(short, ik...)

	for _, bad := range [][]byte{{0xdb, 8, 1}, {0xdc, 13}, {0x6f, 0x00}, {}, short} {
		p.aka = bad
		if _, err := u.Authenticate(nil, nil); err == nil || errors.Is(err, ue.ErrMACFailure) {
			t.Errorf("% x: %v", bad, err)
		}
	}

	p.aka, p.akaErr = nil, errors.New("no ISIM")

	if _, err := u.Authenticate(nil, nil); err == nil || errors.Is(err, ue.ErrMACFailure) {
		t.Errorf("platform failure: %v", err)
	}
}

// TS 33.203 Annex I: the SA set to install, its ID its local SPIs, IK with 32 zero bits for HMAC-SHA-1-96, CK for
// AES-CBC.
func TestSAJSON(t *testing.T) {
	set := ue.SASet{
		Local:     ue.Endpoint{Addr: netip.MustParseAddr("10.45.0.3"), PortC: 40000, PortS: 40001, SPIC: 1, SPIS: 2},
		Remote:    ue.Endpoint{Addr: netip.MustParseAddr("10.206.0.2"), PortC: 5064, PortS: 5063, SPIC: 3, SPIS: 4},
		Integrity: ue.HMACSHA196, Encryption: ue.EncryptionAESCBC,
	}
	keys := ue.Keys{IK: bytes.Repeat([]byte{0xab}, 16), CK: bytes.Repeat([]byte{0xcd}, 16)}

	s, err := setJSON(set, keys)
	if err != nil {
		t.Fatal(err)
	}

	var j saSetJSON
	if err := json.Unmarshal([]byte(s), &j); err != nil {
		t.Fatal(err)
	}

	if j.ID != "1-2" || j.IK != strings.Repeat("ab", 16)+"00000000" || j.CK != strings.Repeat("cd", 16) ||
		j.Remote.PortS != 5063 || j.Integrity != "hmac-sha-1-96" || j.Encryption != "aes-cbc" {
		t.Fatalf("SA set %s", s)
	}

	// The app's tests decode it too (android/app/src/test).
	golden(t, "saset.json", []byte(s+"\n"))
}

// A timer of a minute or more also wakes the device: by the alarm, it fires once, on its own goroutine, the device
// kept awake; a stopped timer cancels its alarm; another clock's alarms are not this one's; close cancels them all.
func TestClock(t *testing.T) {
	p := newPlatform()
	c := newClock(p)

	fired := make(chan struct{}, 4)

	c.AfterFunc(time.Hour, func() { fired <- struct{}{} })

	var id int64

	p.mu.Lock()

	for k := range p.wakes {
		id = k
	}
	p.mu.Unlock()

	if id == 0 || id>>32 == 0 {
		t.Fatalf("alarm ID %x, want an epoch", id)
	}

	// The alarm goes off, the platform's no more.
	p.CancelWake(id)
	c.woken(id)
	c.woken(id)

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("not fired")
	}

	p.mu.Lock()
	if len(p.awake) != 1 || p.awake[0] != awakeFor.Milliseconds() {
		t.Fatalf("kept awake %v", p.awake)
	}
	p.mu.Unlock()

	stopped := c.AfterFunc(2*time.Minute, func() { t.Error("a stopped timer fired") })
	if !stopped.Stop() {
		t.Fatal("not stopped")
	}

	other := newClock(p)
	other.AfterFunc(time.Hour, func() { t.Error("another clock's timer fired") })
	c.woken(other.epoch | 1)

	c.AfterFunc(time.Hour, func() { t.Error("a timer of a closed clock fired") })
	c.close()

	p.mu.Lock()
	if len(p.wakes) != 1 {
		t.Fatalf("alarms %v after close, want the other clock's only", p.wakes)
	}
	p.mu.Unlock()

	c.AfterFunc(time.Millisecond, func() { fired <- struct{}{} })

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("a short timer did not fire")
	}

	if d := c.Now().Sub(c.base); d < 0 || d > time.Minute {
		t.Fatalf("clock at %s from its start", d)
	}
}

// Each line of the log goes with its level, from the minimum of the settings.
func TestLogLevels(t *testing.T) {
	p := newPlatform()
	h := newLogHandler(p, "warn")
	l := slog.New(h)

	l.Info("not this")
	l.Warn("this")
	l.Error("and this")

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.logs) != 2 || p.logs[0] != 4 || p.logs[1] != 8 {
		t.Fatalf("levels %v", p.logs)
	}
}

// TS 24.229 §5.1.1.2.1, RFC 5626 §4.5: the waits after failures, half to all of the doubling bound; a Retry-After
// instead when the REGISTER's response has one.
func TestRetryDelay(t *testing.T) {
	for failures, bound := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second} {
		for range 100 {
			if d := retryDelay(failures); d < bound/2 || d > bound {
				t.Fatalf("retryDelay(%d) %v, want in [%v, %v]", failures, d, bound/2, bound)
			}
		}
	}

	// Every count of failures in the bound, past where minBackoff·2^failures overflows (31 of 5 s): the app aborted
	// there, rand.N of a negative bound panicking.
	for failures := range 101 {
		bound := maxBackoff
		if failures < 6 {
			bound = min(maxBackoff, minBackoff<<failures)
		}

		if d := retryDelay(failures); d < bound/2 || d > bound {
			t.Fatalf("retryDelay(%d) %v, want in [%v, %v]", failures, d, bound/2, bound)
		}
	}

	for _, c := range []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"120", 120 * time.Second, true},
		{"18000;duration=3600", 18000 * time.Second, true},
		{"120 (I'm in a meeting)", 120 * time.Second, true},
		{"soon", 0, false},
	} {
		res := &sip.Response{StatusCode: 480}
		res.Header = sip.Header{{Name: "Retry-After", Value: c.value}}

		if got, ok := retryAfter(&ue.ResponseError{Response: res}); got != c.want || ok != c.ok {
			t.Errorf("retryAfter(%q) %v %v, want %v %v", c.value, got, ok, c.want, c.ok)
		}
	}

	if _, ok := retryAfter(errors.New("timeout")); ok {
		t.Error("retryAfter of a timeout")
	}
}

// TS 24.229 §5.1.1.2.1: a REGISTER answered 480 with a Retry-After, the session registers again after it, not after its
// own backoff, longer here; the network back by then (the server on the P-CSCF's address), registered.
func TestRetryAfterRecovery(t *testing.T) {
	saved := minBackoff
	minBackoff = time.Minute

	t.Cleanup(func() { minBackoff = saved })

	addr, control := serverPorts(t)

	// The network down: each REGISTER answered 480, Retry-After 1.
	stub, err := new(net.ListenConfig).ListenPacket(t.Context(), "udp", addr.String())
	if err != nil {
		t.Fatal(err)
	}

	answered := make(chan struct{}, 8)

	go func() {
		buf := make([]byte, 65535)

		for {
			n, from, err := stub.ReadFrom(buf)
			if err != nil {
				return
			}

			m, err := sip.Parse(buf[:n])
			req, ok := m.(*sip.Request)

			if err != nil || !ok || req.Method != "REGISTER" {
				continue
			}

			res := sip.NewResponse(req, 480, "Temporarily Unavailable")
			res.Header = append(res.Header, sip.Field{Name: "Retry-After", Value: "1"})
			_, _ = stub.WriteTo(res.Bytes(), from)

			answered <- struct{}{}
		}
	}()

	p := newPlatform()

	s, err := newSession(p, testSettings(addr, "alice", 2))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(s.Stop)

	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("no REGISTER")
	}

	if e := p.await(t, "registration", ""); e.Registered || e.Error == "" {
		t.Fatalf("registration %+v, want the 480", e)
	}

	// The network back.
	_ = stub.Close()
	start := time.Now()
	srv := startDirect(t, addr, control)

	t.Cleanup(func() { _ = srv.Shutdown(t.Context()) })

	if e := p.await(t, "registration", ""); !e.Registered {
		t.Fatalf("registration %+v", e)
	}

	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("registered %v after the network back, want after the Retry-After", d)
	}
}

// A call refused as of a user not affiliated to the group (TS 24.379 §10.1.1.4.2, warning 120), of a server that lost
// the client's affiliations, is placed again once the client affiliated again.
func TestRetryNotAffiliated(t *testing.T) {
	addr, control := serverPorts(t)
	srv := startDirect(t, addr, control)

	t.Cleanup(func() { _ = srv.Shutdown(t.Context()) })

	alice, pa := session(t, addr, "alice", 2)
	_, pb := session(t, addr, "bob", 3)

	id, err := sip.ParseURI("sip:alice@mcptt.example.com")
	if err != nil {
		t.Fatal(err)
	}

	srv.Participating().LogOff(srv.Agent().Layer().Clock().Now(), id, testSettings(addr, "alice", 2).ClientID)

	if err := alice.Call("a1", "sip:fire@mcptt.example.com"); err != nil {
		t.Fatal(err)
	}

	if e := pa.await(t, "affiliation", ""); e.Status != 200 {
		t.Fatalf("affiliation %+v", e)
	}

	if e := pa.await(t, "call", ""); e.Kind != "established" || e.Call != "a1" {
		t.Fatalf("call %+v, want a1 established", e)
	}

	pb.await(t, "call", "incoming")
}

// Without a participating PSI, the session has that of the MCPTT AS of Ella IMS of its home domain.
func TestDefaultPSI(t *testing.T) {
	set := testSettings(netip.MustParseAddrPort("127.0.0.1:5060"), "alice", 2)
	set.ParticipatingPSI, set.HomeDomain = "", "ims.mnc001.mcc001.3gppnetwork.org"

	s, err := newSession(newPlatform(), set)
	if err != nil {
		t.Fatal(err)
	}

	if got := s.psi.String(); got != "sip:mcptt-participating@mcptt.ims.mnc001.mcc001.3gppnetwork.org" {
		t.Errorf("PSI %s", got)
	}
}
