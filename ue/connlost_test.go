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

// RFC 3261 §17.1.4, TS 24.229 §5.1.1.4.1 NOTE 13: a reregistration whose connection is lost before its response fails
// at once with a transport error, as timer F would later: the UE is to register again.
func TestReregistrationConnectionLost(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP})
	registerTCP(t, n, u)

	done := uetest.Start(u.Reregister)

	_, f := n.Recv(n.PS)
	n.PS.CloseConn(f.Remote)

	if err := uetest.Wait(t, done); !errors.Is(err, transaction.ErrTransport) {
		t.Fatalf("Reregister = %v, want a transport error", err)
	}

	if e := flowFailedWithin(t, u, time.Second); !errors.Is(e.Err, transaction.ErrTransport) {
		t.Fatalf("flow failed with %v, want a transport error", e.Err)
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

	n.Register(u)

	if got := u.State().UnavailablePCSCFs; len(got) != 0 {
		t.Fatalf("P-CSCFs unavailable %v after registering through it", got)
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
