package b2bua_test

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

const wait = 5 * time.Second

func init() {
	// Calls, legs and relays are used on their strands only.
	ua.CheckStrands(true)
}

type event struct {
	kind  string
	s     *ua.Session
	res   *sip.Response
	req   *ua.Request
	sdp   []byte
	offer *ua.Offer
	end   ua.End
	msg   sip.Message
}

// party is a user agent on the net, recording the events of its sessions.
type party struct {
	t      *testing.T
	net    *siptest.Net
	name   string
	a      *ua.Agent
	addr   netip.AddrPort
	events chan event

	mu       sync.Mutex
	onInvite func(r *ua.Request) ua.Handler
}

func newNet(t *testing.T) *siptest.Net { return siptest.NewNet(t) }

func newParty(t *testing.T, n *siptest.Net, name string, configure func(*ua.Config)) *party {
	t.Helper()

	p := &party{t: t, net: n, name: name, addr: n.Addr(), events: make(chan event, 256)}

	cfg := ua.Config{
		Prepare: p.prepare,
		Contact: sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: p.addr.Addr().String(), Port: p.addr.Port()}},
		Policy:  ua.ClientPolicy,
		Allow:   append(slices.Clone(ua.Methods), "INFO", "MESSAGE"),
		OnInvite: func(r *ua.Request) ua.Handler {
			p.mu.Lock()
			f := p.onInvite
			p.mu.Unlock()

			if f != nil {
				return f(r)
			}

			p.record(event{kind: "invite", req: r})

			return p.handler()
		},
	}

	if configure != nil {
		configure(&cfg)
	}

	cfg.Transaction = n.Config(cfg.Transaction)

	a, err := ua.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Layer().Serve(p.addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		n.Await(func() { _ = a.Close(context.Background()) }, 2*64*transaction.DefaultT1)
	})

	p.a = a

	return p
}

// prepare sends to the host and port of the top Route, or else of the Request-URI.
func (p *party) prepare(req *sip.Request) error {
	target := req.URI
	if r, err := req.Header.TopRoute(); err == nil {
		target = r.URI
	}

	addr, err := netip.ParseAddr(target.Host)
	if err != nil {
		return err
	}

	req.Flow = sip.Flow{Transport: sip.UDP, Local: p.addr, Remote: netip.AddrPortFrom(addr, target.Port)}
	req.Header.Prepend("Via", sip.NewVia(sip.UDP, p.addr).String())

	return nil
}

func (p *party) uri() string { return fmt.Sprintf("sip:%s@%s", p.name, p.addr) }

func (p *party) record(e event) {
	select {
	case p.events <- e:
	default:
		p.t.Errorf("%s: event queue full, %s dropped", p.name, e.kind)
	}
}

func (p *party) handler() ua.Handler {
	return ua.Handler{
		Progress:    func(s *ua.Session, res *sip.Response) { p.record(event{kind: "progress", s: s, res: res}) },
		Established: func(s *ua.Session, m sip.Message) { p.record(event{kind: "established", s: s, msg: m}) },
		Answer:      func(s *ua.Session, sdp []byte) { p.record(event{kind: "answer", s: s, sdp: sdp}) },
		Offer:       func(s *ua.Session, o *ua.Offer) { p.record(event{kind: "offer", s: s, offer: o, sdp: o.SDP()}) },
		Request:     func(s *ua.Session, r *ua.Request) { p.record(event{kind: "request", s: s, req: r}) },
		Ended:       func(s *ua.Session, e ua.End) { p.record(event{kind: "ended", s: s, end: e}) },
		Failed: func(_ *ua.Outgoing, res *sip.Response, _ error) {
			p.record(event{kind: "failed", res: res})
		},
	}
}

// next is the next event, of kind.
func (p *party) next(kind string) event {
	p.t.Helper()
	p.net.Quiesce()

	select {
	case e := <-p.events:
		if e.kind != kind {
			p.t.Fatalf("%s: %s event, want %s (%+v)", p.name, e.kind, kind, e)
		}

		return e
	case <-time.After(wait):
		p.t.Fatalf("%s: no %s event", p.name, kind)
		return event{}
	}
}

// none checks that the party has no event once the net is quiet.
func (p *party) none() {
	p.t.Helper()
	p.net.Quiesce()

	select {
	case e := <-p.events:
		p.t.Fatalf("%s: unexpected %s event (%+v)", p.name, e.kind, e)
	default:
	}
}

// invite sends an INVITE to target, with the SDP sdp unless empty.
func (p *party) invite(target string, sdp string, h ua.Handler, header ...sip.Field) *ua.Outgoing {
	p.t.Helper()

	uri, _ := sip.ParseURI(target)
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+p.uri()+">")
	req.Header.Add("To", "<"+target+">")

	for _, f := range header {
		req.Header.Add(f.Name, f.Value)
	}

	if sdp != "" {
		req.SetBodies(ua.SDP([]byte(sdp)))
	}

	o, err := p.a.Invite(req, h)
	if err != nil {
		p.t.Fatal(err)
	}

	return o
}

// b2b is a B2BUA bridging each INVITE it gets to bob.
type b2b struct {
	*party

	ended   chan *b2bua.Call
	relays  chan *b2bua.Relay
	strands []*ua.Strand
	cfg     b2bua.RelayConfig
}

func newB2B(t *testing.T, n *siptest.Net, to func() *party, cfg b2bua.RelayConfig) *b2b {
	return newB2BWith(t, n, to, cfg, nil)
}

func newB2BWith(t *testing.T, n *siptest.Net, to func() *party, cfg b2bua.RelayConfig, configure func(*ua.Config)) *b2b {
	b := &b2b{ended: make(chan *b2bua.Call, 16), relays: make(chan *b2bua.Relay, 16), cfg: cfg}

	b.party = newParty(t, n, "b2b", func(c *ua.Config) {
		c.Policy = ua.Policy{}

		if configure != nil {
			configure(c)
		}

		c.OnInvite = func(r *ua.Request) ua.Handler {
			call := b2bua.NewCall(b.a, r.Strand(), b2bua.CallHandler{Ended: func(c *b2bua.Call) { b.ended <- c }})
			b.strands = append(b.strands, r.Strand())

			target := to()
			uri, _ := sip.ParseURI(target.uri())
			out := sip.NewRequest("INVITE", uri)

			from, _ := r.Message().Header.From()
			out.Header.Add("From", "<"+from.URI.String()+">")
			out.Header.Add("To", "<"+target.uri()+">")

			rel, err := b2bua.Bridge(call, r, out, b.cfg)
			if rel != nil {
				b.relays <- rel
			}

			if err != nil {
				t.Logf("Bridge: %v", err)
			}

			return ua.Handler{}
		}
	})

	return b
}

func (b *b2b) callEnded(t *testing.T) {
	t.Helper()
	b.net.Quiesce()

	select {
	case <-b.ended:
	default:
		t.Fatal("the call did not end")
	}
}

func sdpOf(s string) []sip.BodyPart { return []sip.BodyPart{ua.SDP([]byte(s))} }

func hasSDP(m sip.Message, want string) bool {
	part, ok, _ := m.Env().Content().FindFunc(sip.BodyPart.IsSession)
	return ok && string(part.Body) == want
}

var netAddr = netip.MustParseAddrPort("192.0.2.2:5060")

// nextAll takes events of the given kinds in any order, as those of different early dialogs are.
func (p *party) nextAll(kinds ...string) map[string]event {
	p.t.Helper()
	p.net.Quiesce()

	got := map[string]event{}

	for range kinds {
		select {
		case e := <-p.events:
			got[e.kind] = e
		case <-time.After(wait):
			p.t.Fatalf("%s: events %v, want %v", p.name, got, kinds)
		}
	}

	for _, k := range kinds {
		if _, ok := got[k]; !ok {
			p.t.Fatalf("%s: no %s event among %v", p.name, k, got)
		}
	}

	return got
}

// settled checks that nothing is left in the agents of the parties once their transactions and lingering dialogs
// timed out: every leg ended, every INVITE answered.
func settled(t *testing.T, n *siptest.Net, parties ...*party) {
	t.Helper()

	n.Advance(64*transaction.DefaultT1 + time.Second)
	n.Advance(64*transaction.DefaultT1 + time.Second)

	for _, p := range parties {
		if st := p.a.Stats(); st != (ua.Stats{}) {
			t.Errorf("%s: left %+v", p.name, st)
		}
	}
}

// peekKind is the kind of the next event of the party, once the net is quiet, for loops that skip some.
func (p *party) peekKind() string {
	p.t.Helper()
	p.net.Quiesce()

	select {
	case e := <-p.events:
		// Put back in front: the channel is drained by the next call to next.
		rest := []event{e}

		for len(p.events) > 0 {
			rest = append(rest, <-p.events)
		}

		for _, r := range rest {
			p.events <- r
		}

		return e.kind
	default:
		p.t.Fatalf("%s: no event", p.name)
		return ""
	}
}
