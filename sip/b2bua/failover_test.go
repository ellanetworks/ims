package b2bua_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type serial struct {
	*b2b
	failures chan b2bua.Failure
}

func newSerial(t *testing.T, n *siptest.Net, cfg b2bua.RelayConfig, targets ...string) *serial {
	s := &serial{b2b: &b2b{ended: make(chan *b2bua.Call, 16), cfg: cfg}, failures: make(chan b2bua.Failure, 16)}

	s.party = newParty(t, n, "b2b", func(c *ua.Config) {
		c.Policy = ua.Policy{}
		c.Prepare = func(ctx context.Context, req *sip.Request) error {
			target := req.URI
			if r, err := req.Header.TopRoute(); err == nil {
				target = r.URI
			}

			addr, err := netip.ParseAddr(target.Host)
			if err != nil {
				return err
			}

			tr := sip.UDP
			if v, ok := target.Params.Get("transport"); ok && v == "tcp" {
				tr = sip.TCP
			}

			req.Flow = sip.Flow{Transport: tr, Local: s.addr, Remote: netip.AddrPortFrom(addr, target.Port)}
			req.Header.Prepend("Via", sip.NewVia(tr, s.addr).String())

			return nil
		}

		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			call := b2bua.NewCall(s.a, r.Strand(), b2bua.CallHandler{Ended: func(_ context.Context, c *b2bua.Call) { s.ended <- c }})

			from, _ := r.Message().Header.From()
			build := func(target string) *sip.Request {
				uri, _ := sip.ParseURI(target)
				out := sip.NewRequest("INVITE", uri)
				out.Header.Add("From", "<"+from.URI.String()+">")
				out.Header.Add("To", "<"+target+">")

				return out
			}

			next := 0
			relay := s.cfg
			relay.Failover = func(_ context.Context, _ *b2bua.Leg, f b2bua.Failure) *sip.Request {
				s.failures <- f

				if next++; f.Response != nil && f.Response.StatusCode != 503 || next >= len(targets) {
					return nil
				}

				return build(targets[next])
			}

			if _, err := b2bua.Bridge(context.Background(), call, r, build(targets[0]), relay); err != nil {
				t.Logf("Bridge: %v", err)
			}

			return ua.Handler{}
		}
	})

	return s
}

func (s *serial) failure(t *testing.T) b2bua.Failure {
	t.Helper()
	s.net.Quiesce()

	select {
	case f := <-s.failures:
		return f
	default:
		t.Fatal("no failure reported")
		return b2bua.Failure{}
	}
}

func (s *serial) noFailure(t *testing.T) {
	t.Helper()
	s.net.Quiesce()

	select {
	case f := <-s.failures:
		t.Fatalf("failure %+v reported", f)
	default:
	}
}

func deadUDP(n *siptest.Net) string {
	return "sip:dead@" + n.Addr().String()
}

func deadTCP(n *siptest.Net) string {
	return "sip:dead@" + n.Addr().String() + ";transport=tcp"
}

func failedWith(t *testing.T, p *party, code int) *sip.Response {
	t.Helper()

	e := p.next("failed")
	if e.res == nil || e.res.StatusCode != code {
		t.Fatalf("%s's INVITE ended with %+v, want %d", p.name, e.res, code)
	}

	return e.res
}

// RFC 3263 section 4.3
func TestFailoverOn503(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	carol := newParty(t, n, "carol", nil)
	m := &recorder{}
	b := newSerial(t, n, b2bua.RelayConfig{Media: m}, bob.uri(), carol.uri())
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	_ = bob.next("invite").req.Reject(503)

	if f := b.failure(t); f.Response == nil || f.Response.StatusCode != 503 || !f.ServerFailed() {
		t.Fatalf("failure %+v", f)
	}

	alice.none()

	in := carol.next("invite").req
	if !hasSDP(in.Message(), "offer-1") {
		t.Fatalf("carol's INVITE without the offer: %q", in.Message().Body)
	}

	_ = in.Progress(ua.Reply{Code: 180})

	if e := alice.next("progress"); e.res.StatusCode != 180 {
		t.Fatalf("alice got %d", e.res.StatusCode)
	}

	_ = in.Accept(sdpOf("answer-1")...)

	as := alice.next("established").s
	alice.next("answer")
	carol.next("established")

	checkEvents(t, m, "offer INVITE offer-1", "end INVITE offer-1 false", "offer INVITE offer-1", "answer answer-1", "end INVITE offer-1 true")

	_ = as.Bye(context.Background(), ua.Change{}, nil)

	alice.next("ended")
	carol.next("ended")
	b.callEnded(t)
	b.noFailure(t)
	settled(t, n, alice, bob, carol, b.party)
}

// RFC 3263 section 4.3
func TestFailoverOnTimeout(t *testing.T) {
	n := newNet(t)
	carol := newParty(t, n, "carol", nil)
	b := newSerial(t, n, b2bua.RelayConfig{}, deadUDP(n), carol.uri())
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	carol.none()
	n.Advance(64*transaction.DefaultT1 + 1)

	if f := b.failure(t); f.Response != nil || !errors.Is(f.Err, transaction.ErrTimeout) || !f.ServerFailed() {
		t.Fatalf("failure %+v", f)
	}

	in := carol.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	alice.next("established")
	alice.next("answer")
	carol.next("established")
}

// RFC 3263 section 4.3
func TestFailoverOnTransportError(t *testing.T) {
	n := newNet(t)
	carol := newParty(t, n, "carol", nil)
	b := newSerial(t, n, b2bua.RelayConfig{}, deadTCP(n), carol.uri())
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "", alice.handler())

	if f := b.failure(t); f.Response != nil || !errors.Is(f.Err, transaction.ErrTransport) || !f.ServerFailed() {
		t.Fatalf("failure %+v", f)
	}

	in := carol.next("invite").req
	if _, ok := in.Offer(); ok {
		t.Fatal("carol's INVITE with an offer")
	}

	_ = in.Accept(sdpOf("offer-c")...)

	alice.next("established")

	o := alice.next("offer")
	if string(o.sdp) != "offer-c" {
		t.Fatalf("alice got offer %q", o.sdp)
	}

	_ = o.offer.Answer(context.Background(), ua.Change{Bodies: sdpOf("answer-a")})

	if e := carol.next("answer"); string(e.sdp) != "answer-a" {
		t.Fatalf("carol got answer %q", e.sdp)
	}

	carol.next("established")
}

// TS 24.229 clause 5.10.2.2
// RFC 3261 section 16.7
func TestNoFailoverOnOtherFinals(t *testing.T) {
	for _, c := range []struct{ code, want int }{{486, 486}, {302, 480}, {305, 480}, {603, 603}} {
		n := newNet(t)
		bob := newParty(t, n, "bob", nil)
		carol := newParty(t, n, "carol", nil)
		b := newSerial(t, n, b2bua.RelayConfig{}, bob.uri(), carol.uri())
		alice := newParty(t, n, "alice", nil)

		alice.invite(b.uri(), "offer-1", alice.handler())

		_ = bob.next("invite").req.Reject(c.code, sip.Field{Name: "Contact", Value: "<sip:elsewhere@192.0.2.9>"},
			sip.Field{Name: "Warning", Value: `399 bob "no"`})

		res := failedWith(t, alice, c.want)
		if c.code == c.want && res.Header.Get("Warning") != `399 bob "no"` {
			t.Errorf("%d: Warning %q", c.code, res.Header.Get("Warning"))
		}

		if c.code < 600 {
			if f := b.failure(t); f.Response == nil || f.Response.StatusCode != c.code || f.ServerFailed() {
				t.Fatalf("%d: failure %+v", c.code, f)
			}
		}

		carol.none()
		b.callEnded(t)
		settled(t, n, alice, bob, carol, b.party)
	}
}

// RFC 3261 section 16.7
func TestNoFailoverAfterProvisional(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	carol := newParty(t, n, "carol", nil)
	b := newSerial(t, n, b2bua.RelayConfig{}, bob.uri(), carol.uri())
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})

	alice.next("progress")

	_ = in.Reject(503)

	failedWith(t, alice, 500)
	alice.next("ended")
	b.noFailure(t)
	carol.none()
	b.callEnded(t)
	settled(t, n, alice, bob, carol, b.party)
}

// RFC 3261 section 16.10
func TestCancelDuringFailover(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	carol := newParty(t, n, "carol", nil)
	dave := newParty(t, n, "dave", nil)
	m := &recorder{}
	b := newSerial(t, n, b2bua.RelayConfig{Media: m}, bob.uri(), carol.uri(), dave.uri())
	alice := newParty(t, n, "alice", nil)

	o := alice.invite(b.uri(), "offer-1", alice.handler())

	_ = bob.next("invite").req.Reject(503)

	b.failure(t)

	carol.next("invite")

	_ = o.Cancel(context.Background(), ua.Change{})

	failedWith(t, alice, 487)

	if e := carol.next("ended"); e.end.Cause != ua.EndCancelled {
		t.Fatalf("carol ended %+v", e.end)
	}

	b.noFailure(t)
	dave.none()
	b.callEnded(t)

	checkEvents(t, m, "offer INVITE offer-1", "end INVITE offer-1 false", "offer INVITE offer-1", "end INVITE offer-1 false")
	settled(t, n, alice, bob, carol, dave, b.party)
}

// RFC 3261 section 16.10
func TestCancelBeforeTheNextTargetAnswers(t *testing.T) {
	n := newNet(t)
	carol := newParty(t, n, "carol", nil)
	b := newSerial(t, n, b2bua.RelayConfig{}, deadTCP(n), deadUDP(n), carol.uri())
	alice := newParty(t, n, "alice", nil)

	o := alice.invite(b.uri(), "offer-1", alice.handler())

	b.failure(t)

	_ = o.Cancel(context.Background(), ua.Change{})

	failedWith(t, alice, 487)

	n.Advance(64*transaction.DefaultT1 + 1)

	b.noFailure(t)
	carol.none()
	b.callEnded(t)
	settled(t, n, alice, carol, b.party)
}

// RFC 3261 section 16.7
// RFC 3261 section 16.9
func TestEveryTargetFails(t *testing.T) {
	for _, c := range []struct {
		name   string
		last   func(*siptest.Net, *party) string
		reject int
		want   int
	}{
		{"503", func(_ *siptest.Net, p *party) string { return p.uri() }, 503, 500},
		{"486", func(_ *siptest.Net, p *party) string { return p.uri() }, 486, 486},
		{"timeout", func(n *siptest.Net, _ *party) string { return deadUDP(n) }, 0, 408},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := newNet(t)
			bob := newParty(t, n, "bob", nil)
			carol := newParty(t, n, "carol", nil)
			b := newSerial(t, n, b2bua.RelayConfig{}, deadTCP(n), bob.uri(), c.last(n, carol))
			alice := newParty(t, n, "alice", nil)

			alice.invite(b.uri(), "offer-1", alice.handler())

			b.failure(t)

			_ = bob.next("invite").req.Reject(503)

			b.failure(t)

			if c.reject != 0 {
				_ = carol.next("invite").req.Reject(c.reject)
			} else {
				n.Advance(64*transaction.DefaultT1 + 1)
			}

			b.failure(t)
			failedWith(t, alice, c.want)
			b.callEnded(t)
			settled(t, n, alice, bob, carol, b.party)
		})
	}
}

func TestEveryTargetTimesOut(t *testing.T) {
	n := newNet(t)
	b := newSerial(t, n, b2bua.RelayConfig{}, deadUDP(n), deadUDP(n))
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	n.Advance(64*transaction.DefaultT1 + 1)
	b.failure(t)

	n.Advance(64*transaction.DefaultT1 + 1)
	b.failure(t)

	failedWith(t, alice, 408)
	b.callEnded(t)
	settled(t, n, alice, b.party)
}

func TestFailoverOnADialError(t *testing.T) {
	n := newNet(t)
	carol := newParty(t, n, "carol", nil)
	b := newSerial(t, n, b2bua.RelayConfig{}, "sip:dead@unresolved.invalid", carol.uri())
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	if f := b.failure(t); f.Response != nil || f.Err == nil || f.ServerFailed() {
		t.Fatalf("failure %+v", f)
	}

	_ = carol.next("invite").req.Accept(sdpOf("answer-1")...)

	alice.next("established")
	alice.next("answer")
	carol.next("established")
}

func TestEveryDialFails(t *testing.T) {
	n := newNet(t)
	b := newSerial(t, n, b2bua.RelayConfig{}, "sip:dead@unresolved.invalid", "sip:dead@unresolved.invalid")
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	b.failure(t)
	b.failure(t)

	failedWith(t, alice, 500)
	b.callEnded(t)
	settled(t, n, alice, b.party)
}

func TestCancelStopsTheFailoverBudget(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	carol := newParty(t, n, "carol", nil)
	b := newSerial(t, n, b2bua.RelayConfig{FailoverBudget: time.Hour}, bob.uri(), carol.uri())
	alice := newParty(t, n, "alice", nil)

	o := alice.invite(b.uri(), "offer-1", alice.handler())

	_ = bob.next("invite").req.Reject(503)

	b.failure(t)

	carol.next("invite")

	_ = o.Cancel(context.Background(), ua.Change{})

	failedWith(t, alice, 487)
	carol.next("ended")
	b.callEnded(t)
	settled(t, n, alice, bob, carol, b.party)

	if d, ok := n.Clock().Next(); ok {
		t.Fatalf("a timer is still due in %v", d)
	}
}
