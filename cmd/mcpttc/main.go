// Command mcpttc is the in-house MCPTT client as a lab tool, in direct mode [PM 14]: a UDP agent that registers with
// the server, affiliates to groups, then places, joins and leaves group calls and takes the floor as told on its
// standard input, printing the call and floor events.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/client/clienttest"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type options struct {
	server, local    netip.AddrPort
	domain, psi      string
	impu, mcpttID    string
	clientID, token  string
	groups           string
	minPort, maxPort uint
	t100, t101       time.Duration
	expires          time.Duration
	implicit, debug  bool
}

func main() {
	var o options

	server := flag.String("server", "127.0.0.1:5060", "address and port of the MCPTT server, over UDP")
	local := flag.String("local", "127.0.0.1:0", "address and port of the client's SIP socket, not unspecified: its media, Contact and SDP on the address too")

	flag.StringVar(&o.domain, "domain", "mcptt.example.com", "domain of the server, the Request-URI of the REGISTERs")
	flag.StringVar(&o.psi, "psi", "", "participating PSI (default sip:mcptt-participating@<domain>)")
	flag.StringVar(&o.impu, "impu", "", "IMS public user identity, a configured IMPU of the user (required)")
	flag.StringVar(&o.mcpttID, "mcptt-id", "", "MCPTT ID of the user (required)")
	flag.StringVar(&o.clientID, "client-id", "", "MCPTT client ID, a urn:uuid (default a random one)")
	flag.StringVar(&o.token, "token", "", "access token (default an unsigned token of the MCPTT ID and client ID, which only a server that does not validate tokens takes)")
	flag.StringVar(&o.groups, "groups", "", "comma-separated MCPTT group IDs to affiliate to")
	flag.UintVar(&o.minPort, "min-port", 30000, "first media port, even: four per call")
	flag.UintVar(&o.maxPort, "max-port", 30099, "last media port")
	// TS 24.483 §8.2.11, §8.2.12 configure them, without a default: these are lab values.
	flag.DurationVar(&o.t100, "t100", time.Second, "T100 (Floor Release), TS 24.380 Table 11.1.1-1")
	flag.DurationVar(&o.t101, "t101", time.Second, "T101 (Floor Request), TS 24.380 Table 11.1.1-1")
	flag.DurationVar(&o.expires, "expires", time.Hour, "registration expiry asked for")
	flag.BoolVar(&o.implicit, "implicit", true, "make the INVITE of a call an implicit floor request (TS 24.380 §14.2.5)")
	flag.BoolVar(&o.debug, "debug", false, "log the SIP stack at debug level")

	flag.Parse()

	var err error

	if o.server, err = netip.ParseAddrPort(*server); err == nil {
		o.local, err = netip.ParseAddrPort(*local)
	}

	if err == nil {
		err = run(context.Background(), o)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "mcpttc:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options) error {
	if o.impu == "" || o.mcpttID == "" {
		return errors.New("-impu and -mcptt-id are required")
	}

	if o.psi == "" {
		o.psi = "sip:mcptt-participating@" + o.domain
	}

	impu, err := sip.ParseURI(o.impu)
	if err != nil {
		return fmt.Errorf("-impu: %w", err)
	}

	id, err := sip.ParseURI(o.mcpttID)
	if err != nil {
		return fmt.Errorf("-mcptt-id: %w", err)
	}

	psi, err := sip.ParseURI(o.psi)
	if err != nil {
		return fmt.Errorf("-psi: %w", err)
	}

	var groups []sip.URI

	for g := range strings.SplitSeq(o.groups, ",") {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}

		u, err := sip.ParseURI(g)
		if err != nil {
			return fmt.Errorf("-groups: %w", err)
		}

		groups = append(groups, u)
	}

	if o.clientID == "" {
		o.clientID = newUUID()
	}

	if o.token == "" {
		// The client then puts an unsigned token of the user and client in each REGISTER.
		fmt.Fprintln(os.Stderr, "warning: no -token: an unsigned access token, which only a server that does not validate tokens takes")
	}

	if o.minPort > 65535 || o.maxPort > 65535 {
		return errors.New("-min-port, -max-port: out of range")
	}

	if !o.local.Addr().IsValid() || o.local.Addr().IsUnspecified() {
		// The address is the Contact's and the SDP's c=, where 0.0.0.0 means no media is to be sent (RFC 3264 §8.4).
		return fmt.Errorf("-local %s: an address of the host, not an unspecified one", o.local)
	}

	level := slog.LevelWarn
	if o.debug {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// The client is set once the socket is bound, before the REGISTER that lets the server invite it.
	var cl atomic.Pointer[client.Client]

	var local atomic.Value // netip.AddrPort

	agent, err := ua.New(ua.Config{
		Transaction: transaction.Config{Logger: logger},
		Logger:      logger,
		// Every request goes to the server, the client's registrar and outbound proxy.
		Prepare: func(req *sip.Request) error {
			l, _ := local.Load().(netip.AddrPort)
			req.Flow = sip.Flow{Transport: sip.UDP, Local: l, Remote: o.server}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, l).String())

			return nil
		},
		OnInvite: func(r *ua.Request) ua.Handler {
			if c := cl.Load(); c != nil {
				return c.OnInvite(r)
			}

			_ = r.Reject(480)

			return ua.Handler{}
		},
	})
	if err != nil {
		return err
	}

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = agent.Shutdown(closeCtx)
	}()

	bound, err := agent.Layer().Listen(ctx, o.local)
	if err != nil {
		return err
	}

	local.Store(bound)

	ui := &console{out: os.Stdout}
	speaker := clienttest.NewSpeaker(agent.Layer().Clock())

	c, err := client.New(client.Config{
		Agent: agent, IMPU: impu, MCPTTID: id, ClientID: o.clientID, Token: o.token,
		Contact:          client.FixedContact(sip.Address{URI: sip.URI{Scheme: "sip", Host: sip.FormatHost(bound.Addr()), Port: bound.Port()}}),
		Registrar:        sip.URI{Scheme: "sip", Host: o.domain},
		ParticipatingPSI: psi,
		Media:            media.UDP{}, Addr: bound.Addr(), MinPort: uint16(o.minPort), MaxPort: uint16(o.maxPort),
		Floor:           floor.ParticipantTimers{T100: o.t100, T101: o.t101},
		ImplicitRequest: o.implicit, Granted: o.implicit,
		OnEvent: func(e client.Event) {
			speaker.Event(e)
			ui.event(e)
		},
	})
	if err != nil {
		return err
	}

	cl.Store(c)

	ui.printf("SIP on %s, server %s", bound, o.server)

	if err := wait("register", func(done func(*sip.Response, error)) error { return c.Register(o.expires, done) }); err != nil {
		return err
	}

	ui.printf("registered %s as %s", impu, id)

	if len(groups) > 0 {
		if err := wait("affiliate", func(done func(*sip.Response, error)) error { return c.Affiliate(groups, done) }); err != nil {
			return err
		}

		ui.printf("affiliated to %s", o.groups)
	}

	ui.printf(`commands: "call <group>", "p" (press), "r" (release), "ptt" (toggle), "leave", "quit"`)
	ui.loop(c, os.Stdin)

	if err := wait("unregister", c.Unregister); err != nil {
		ui.printf("%v", err)
	}

	return nil
}

// newUUID is a random urn:uuid (RFC 9562 §5.4).
func newUUID() string {
	var b [16]byte

	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// wait sends a request with send and waits for its final response: an error unless a 2xx.
func wait(what string, send func(done func(*sip.Response, error)) error) error {
	type result struct {
		res *sip.Response
		err error
	}

	ch := make(chan result, 1)

	if err := send(func(res *sip.Response, err error) { ch <- result{res, err} }); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	r := <-ch

	switch {
	case r.err != nil:
		return fmt.Errorf("%s: %w", what, r.err)
	case r.res.StatusCode/100 != 2:
		return fmt.Errorf("%s: %d %s", what, r.res.StatusCode, r.res.Reason)
	}

	return nil
}

// console is the interactive loop and the printing of events, which come from the strands of the calls.
type console struct {
	out io.Writer

	mu      sync.Mutex
	call    *client.Call
	talking bool
}

func (u *console) printf(format string, a ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()

	_, _ = fmt.Fprintf(u.out, format+"\n", a...)
}

// current is the call the commands act on: the last placed or received, until it ends.
func (u *console) current() *client.Call {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.call
}

func (u *console) setCall(c *client.Call) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.call, u.talking = c, false
}

func (u *console) loop(c *client.Client, in io.Reader) {
	s := bufio.NewScanner(in)

	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) == 0 {
			continue
		}

		switch cmd, call := f[0], u.current(); {
		case cmd == "quit" || cmd == "q":
			if call != nil {
				call.Leave()
			}

			return
		case cmd == "call" && len(f) == 2:
			g, err := sip.ParseURI(f[1])
			if err != nil {
				u.printf("%v", err)
				continue
			}

			call, err := c.Call(g)
			if err != nil {
				u.printf("call: %v", err)
				continue
			}

			u.setCall(call)
			u.printf("calling %s", g)
		case call == nil:
			u.printf("no call")
		case cmd == "p" || cmd == "press":
			u.press(call, true)
		case cmd == "r" || cmd == "release":
			u.press(call, false)
		case cmd == "ptt":
			u.mu.Lock()
			on := !u.talking
			u.mu.Unlock()

			u.press(call, on)
		case cmd == "leave":
			call.Leave()
		default:
			u.printf("unknown command %q", s.Text())
		}
	}
}

func (u *console) press(call *client.Call, on bool) {
	u.mu.Lock()
	u.talking = on
	u.mu.Unlock()

	if on {
		call.PressPTT()
		u.printf("PTT pressed")

		return
	}

	call.ReleasePTT()
	u.printf("PTT released")
}

var notices = map[floor.NoticeKind]string{
	floor.NoticeGranted:        "granted: talk",
	floor.NoticeDenied:         "denied",
	floor.NoticeTaken:          "taken",
	floor.NoticeIdle:           "idle",
	floor.NoticeRevoked:        "revoked",
	floor.NoticeRequestTimeout: "request timed out",
	floor.NoticeMediaEnded:     "media ended",
}

func (u *console) event(e client.Event) {
	switch e.Kind {
	case client.EventIncoming:
		u.setCall(e.Call)
		u.printf("incoming call of %s from %s", e.Call.Group(), e.Call.Caller())
	case client.EventEstablished:
		u.printf("call of %s established", e.Call.Group())
	case client.EventFailed:
		u.ended(e.Call)

		if e.Response != nil {
			u.printf("call of %s failed: %d %s", e.Call.Group(), e.Response.StatusCode, e.Response.Reason)
		} else {
			u.printf("call of %s failed: %v", e.Call.Group(), e.Err)
		}
	case client.EventEnded:
		u.ended(e.Call)
		u.printf("call of %s ended (%v, by the server: %t)", e.Call.Group(), e.End.Cause, e.End.Remote)
	case client.EventFloor:
		n := e.Floor

		switch n.Kind {
		case floor.NoticeTaken:
			u.printf("floor taken by %s", n.Party)
		case floor.NoticeDenied, floor.NoticeRevoked:
			u.printf("floor %s, cause %v", notices[n.Kind], n.Cause)
		default:
			u.printf("floor %s", notices[n.Kind])
		}
	case client.EventHeard:
		u.printf("hearing %s (SSRC %d)", e.Party, e.SSRC)
	case client.EventRegistration:
		if e.Err != nil || e.Response == nil || e.Response.StatusCode/100 != 2 {
			u.printf("registration refresh failed: %v %v", e.Response, e.Err)
		}
	}
}

// ended forgets call if it is the current one.
func (u *console) ended(call *client.Call) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.call == call {
		u.call, u.talking = nil, false
	}
}
