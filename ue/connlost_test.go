package ue

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/uetest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// registerTCP registers u over TCP, and returns the flow of its protected REGISTER, as the P-CSCF sees it.
func registerTCP(t *testing.T, n *network, u *testUE) sip.Flow {
	t.Helper()

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	return f
}

// flowFailedWithin waits for the deregistration of u's failed flow.
func flowFailedWithin(t *testing.T, u *testUE, d time.Duration) Event {
	t.Helper()

	timeout := time.After(d)

	for {
		select {
		case e := <-u.Events():
			if e.Deregistered == "" {
				continue
			}

			if e.Deregistered != DeregisteredFlowFailed || u.State().Registered {
				t.Fatalf("deregistered %q, state %+v; want the flow failed", e.Deregistered, u.State())
			}

			return e
		case <-timeout:
			t.Fatal("no deregistration event")
		}
	}
}

// RFC 3261 §18.2.2: the connection of a reregistration lost before its response, the P-CSCF sends it on a new one,
// which the UE takes: the registration goes on.
func TestReregistrationConnectionLost(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP})
	registerTCP(t, n, u)

	done := uetest.Start(u.Reregister)

	req, f := n.Recv(n.PS)
	n.PS.CloseConn(f.Remote)
	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatalf("Reregister = %v", err)
	}

	if !u.State().Registered {
		t.Fatal("not registered")
	}
}

// TS 24.229 §5.1.1.4.1: a reregistration timer F ends leaves the contact bound to nothing: the UE is to register again.
func TestReregistrationTimeout(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{T1: 10 * time.Millisecond})
	n.Register(u)

	if err := u.Reregister(t.Context()); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("Reregister = %v, want a timeout", err)
	}

	if e := flowFailedWithin(t, u, time.Second); !errors.Is(e.Err, transaction.ErrTimeout) {
		t.Fatalf("flow failed with %v, want a timeout", e.Err)
	}
}

// RFC 5626 §3, §4.4: the TCP connection of an outbound registration is its flow; its loss is the flow's failure.
func TestOutboundConnectionLost(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP, RegID: 1})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	okOutbound(n, req, f, 600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	n.PS.CloseConn(f.Remote)

	flowFailedWithin(t, u, time.Second)
}

// Without outbound, the registration outlives its connection: the P-CSCF reaches the UE on one of its own.
func TestConnectionLostWithoutOutbound(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP})
	f := registerTCP(t, n, u)

	n.PS.CloseConn(f.Remote)

	select {
	case e := <-u.Events():
		if e.Deregistered != "" {
			t.Fatalf("deregistered %q on the loss of a connection", e.Deregistered)
		}
	case <-time.After(200 * time.Millisecond):
	}

	if !u.State().Registered {
		t.Fatal("not registered after the loss of a connection")
	}
}

// message sends a MESSAGE through the P-CSCF, and returns the outcome of its transaction.
func message(u *testUE) <-chan error {
	done := make(chan error, 1)

	go func() {
		req := sip.NewRequest("MESSAGE", sip.URI{Scheme: "sip", User: "+15550002", Host: domain})
		req.SetBody("text/plain", []byte("hi"))

		_, err := u.Send(context.Background(), req)
		done <- err
	}()

	return done
}

// TS 24.229 §5.1.1.4.1, NOTE 13: the flow of the registration failing, the UE stops processing its transactions,
// which end at once, dropped.
func TestFlowFailedDropsTransactions(t *testing.T) {
	saved := pongTimeout
	pongTimeout = 100 * time.Millisecond

	t.Cleanup(func() { pongTimeout = saved })

	n := newNetwork(t)
	u, _ := n.newUE(Config{RegID: 1})
	registerOutbound(t, n, u, 1)

	sent := message(u)

	req, f := n.Recv(n.PS)
	n.Reply(n.PS, f, sip.NewResponse(req, 100, ""))

	n.PS.MuteSTUN(true)

	flowFailedWithin(t, u, 10*time.Second)

	select {
	case err := <-sent:
		if !errors.Is(err, transaction.ErrDropped) {
			t.Fatalf("MESSAGE = %v, want it dropped", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MESSAGE still under way after the flow failed")
	}
}

// TS 24.229 §5.1.2A.1.6: timer F in Trying of a request through the P-CSCF leaves the contact bound to nothing and the
// P-CSCF unavailable; a registration through it makes it available again.
func TestRequestTimeoutUnbinds(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{T1: 10 * time.Millisecond})
	n.Register(u)

	pcscf := u.State().UnavailablePCSCFs
	if len(pcscf) != 0 {
		t.Fatalf("P-CSCFs unavailable %v before any failure", pcscf)
	}

	sent := message(u)

	n.Recv(n.PS)

	if err := <-sent; !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("MESSAGE = %v, want a timeout", err)
	}

	e := flowFailedWithin(t, u, time.Second)
	if !e.UnavailablePCSCF.IsValid() || !slices.Contains(u.State().UnavailablePCSCFs, e.UnavailablePCSCF) {
		t.Fatalf("P-CSCF %v unavailable, state %v", e.UnavailablePCSCF, u.State().UnavailablePCSCFs)
	}

	// TS 24.229 §5.1.9: no initial registration through it, until a new discovery clears the marks.
	if err := u.Register(t.Context()); !errors.Is(err, ErrPCSCFUnavailable) {
		t.Fatalf("Register = %v, want the P-CSCF unavailable", err)
	}

	u.ClearPCSCFMarks()
	n.Register(u)

	if got := u.State().UnavailablePCSCFs; len(got) != 0 {
		t.Fatalf("P-CSCFs unavailable %v after a new discovery", got)
	}
}

// TS 24.229 §5.1.2A.1.6 B: the P-CSCF marked is the one the contact was registered through, though the owner selected
// another one for its next registration.
func TestRequestFailureMarksTheRegisteredPCSCF(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	registered := u.pcscfAddr()
	if err := u.SetPCSCF(netip.AddrPortFrom(registered.Addr(), registered.Port()+1)); err != nil {
		t.Fatal(err)
	}

	req := sip.NewRequest("MESSAGE", sip.URI{Scheme: "sip", Host: domain})
	req.Flow = sip.Flow{Transport: sip.UDP, Local: u.Unprotected(), Remote: netip.AddrPortFrom(registered.Addr(), 5063)}

	u.requestFailed(req, transaction.Trying, transaction.ErrTimeout)

	if e := flowFailedWithin(t, u, time.Second); e.UnavailablePCSCF != registered {
		t.Fatalf("P-CSCF %v marked, want %v", e.UnavailablePCSCF, registered)
	}

	if got := u.State().UnavailablePCSCFs; !slices.Equal(got, []netip.AddrPort{registered}) {
		t.Fatalf("P-CSCFs unavailable %v, want %v", got, registered)
	}
}

// The flow of the registration failing after a request's failure unbound the contact, its transactions are dropped
// all the same (TS 24.229 §5.1.1.4.1).
func TestFlowFailedAfterARequestFailure(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	sent := message(u)

	n.Recv(n.PS)

	req := sip.NewRequest("OPTIONS", sip.URI{Scheme: "sip", Host: domain})
	req.Flow = sip.Flow{Transport: sip.UDP, Local: u.Unprotected(), Remote: netip.AddrPortFrom(u.pcscfAddr().Addr(), 5063)}

	u.requestFailed(req, transaction.Trying, transaction.ErrTimeout)
	flowFailedWithin(t, u, time.Second)

	u.flowFailed(errors.New("keep-alive not answered"))

	select {
	case err := <-sent:
		if !errors.Is(err, transaction.ErrDropped) {
			t.Fatalf("MESSAGE = %v, want it dropped", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MESSAGE still under way after the flow failed")
	}
}

// TS 24.229 §5.1.9: a mark lasts the retry delay of RFC 5626 §4.5, of base-time 30 s and max-time 1800 s, plus 5
// minutes.
func TestMarkDuration(t *testing.T) {
	for failures, w := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 5: 960 * time.Second, 6: 1800 * time.Second, 40: 1800 * time.Second} {
		for range 50 {
			if d := markDuration(failures); d < w/2+5*time.Minute || d > w+5*time.Minute {
				t.Fatalf("markDuration(%d) = %s, want between %s and %s", failures, d, w/2+5*time.Minute, w+5*time.Minute)
			}
		}
	}
}

// TS 24.229 §5.1.2A.1.6: only a timeout or a fatal transport error of a request other than a REGISTER, through the
// P-CSCF, in Trying of a non-INVITE or in Calling of an INVITE, leaves the contact bound to nothing.
func TestRequestFailureRule(t *testing.T) {
	timeout, transport := transaction.ErrTimeout, fmt.Errorf("%w: lost", transaction.ErrTransport)

	for _, c := range []struct {
		name      string
		method    string
		in        transaction.State
		err       error
		elsewhere bool
		unbinds   bool
	}{
		{"MESSAGE timer F in Trying", "MESSAGE", transaction.Trying, timeout, false, true},
		{"MESSAGE transport error in Trying", "MESSAGE", transaction.Trying, transport, false, true},
		{"MESSAGE timer F in Proceeding", "MESSAGE", transaction.Proceeding, timeout, false, false},
		{"INVITE timer B in Calling", "INVITE", transaction.Calling, timeout, false, true},
		{"INVITE in Proceeding", "INVITE", transaction.Proceeding, transport, false, false},
		{"REGISTER", "REGISTER", transaction.Trying, timeout, false, false},
		{"dropped", "MESSAGE", transaction.Trying, transaction.ErrDropped, false, false},
		{"not through the P-CSCF", "MESSAGE", transaction.Trying, timeout, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := newNetwork(t)
			u, _ := n.newUE(Config{})
			n.Register(u)

			remote := netip.AddrPortFrom(u.pcscfAddr().Addr(), 5063)
			if c.elsewhere {
				remote = netip.MustParseAddrPort("192.0.2.1:5060")
			}

			req := sip.NewRequest(c.method, sip.URI{Scheme: "sip", Host: domain})
			req.Flow = sip.Flow{Transport: sip.UDP, Local: u.Unprotected(), Remote: remote}

			u.requestFailed(req, c.in, c.err)

			if !c.unbinds {
				for quiet := time.After(100 * time.Millisecond); ; {
					select {
					case e := <-u.Events():
						if e.Deregistered != "" {
							t.Fatalf("deregistered %q", e.Deregistered)
						}

						continue
					case <-quiet:
					}

					break
				}

				if !u.State().Registered {
					t.Fatal("deregistered")
				}

				return
			}

			e := flowFailedWithin(t, u, time.Second)
			if e.UnavailablePCSCF != u.pcscfAddr() || !slices.Equal(u.State().UnavailablePCSCFs, []netip.AddrPort{u.pcscfAddr()}) {
				t.Fatalf("P-CSCF %v unavailable, state %v", e.UnavailablePCSCF, u.State().UnavailablePCSCFs)
			}
		})
	}
}
