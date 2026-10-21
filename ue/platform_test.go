package ue

import (
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/internal/uetest"
	"github.com/ellanetworks/ims/sip"
)

// TS 33.203 §7.1, §7.2: the UE's SPIs are the platform's, all released once the UE deregistered; the sockets of both
// protected ports, UDP and TCP listener, protected by the SAs once installed.
func TestSPIsAndSocketsOfThePlatform(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	set := u.SAs()[0].Set
	k := u.ipsec

	k.mu.Lock()
	allocated := slices.Clone(k.allocated)
	k.mu.Unlock()

	if !slices.Contains(allocated, set.Local.SPIC) || !slices.Contains(allocated, set.Local.SPIS) {
		t.Fatalf("SPIs %d and %d of the set, allocated %v", set.Local.SPIC, set.Local.SPIS, allocated)
	}

	var got []protection

	for _, p := range k.protections() {
		got = append(got, protection{port: p.port, typ: p.typ})

		if p.set != set {
			t.Errorf("socket protected with %s, want %s", p.set, set)
		}
	}

	want := []protection{
		{port: set.Local.PortC, typ: syscall.SOCK_DGRAM},
		{port: set.Local.PortC, typ: syscall.SOCK_STREAM},
		{port: set.Local.PortS, typ: syscall.SOCK_DGRAM},
		{port: set.Local.PortS, typ: syscall.SOCK_STREAM},
	}
	// A request too large for UDP is tried over TCP first (RFC 3261 §18.1.1), its connection protected too.
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("protected %+v, without %+v", got, w)
		}
	}

	done := uetest.Start(u.Deregister)

	req, f := n.Recv(n.PS)
	n.OK(req, f, n.PC, 0)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	for _, spi := range k.allocated {
		if !slices.Contains(k.released, spi) {
			t.Errorf("SPI %d allocated, not released: allocated %v, released %v", spi, k.allocated, k.released)
		}
	}
}

// TS 33.203 §7.1: a TCP connection the UE dials from port_uc is protected before it connects.
func TestDialledConnectionProtected(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Transport: sip.TCP})

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	portC := u.SAs()[0].Set.Local.PortC

	streams := 0

	for _, p := range u.ipsec.protections() {
		if p.port == portC && p.typ == syscall.SOCK_STREAM {
			streams++
		}
	}

	if streams != 2 {
		t.Fatalf("port_uc's TCP sockets protected %d times, want its listener and the connection dialled", streams)
	}
}

// TS 24.229 §7.2A.4: the access network info set, the next requests carry it.
func TestAccessNetworkInfoChanged(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})
	n.Register(u)

	const cell = "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=00101000200000042"

	u.SetAccessNetworkInfo(cell)

	done := uetest.Start(u.Reregister)

	req, f := n.Recv(n.PS)
	if got := req.Header.Get("P-Access-Network-Info"); got != cell {
		t.Errorf("P-Access-Network-Info %q, want %q", got, cell)
	}

	n.OK(req, f, n.PC, 3600)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}
}

// RFC 3680 §5.1: a contact on probation ends the registration, its retry-after told; the UE does not register again by
// itself.
func TestProbation(t *testing.T) {
	n := newRegEventNetwork(t)
	u, _ := n.newUE(Config{T1: 10 * time.Millisecond})

	no := n.registerSubscribing(u)
	no.active()

	info := no.reginfo(regevent.Active, regevent.Terminated, regevent.Probation, 0)
	retry := uint32(90)

	for i := range info.Registrations {
		info.Registrations[i].Contacts[0].RetryAfter = &retry
	}

	if res := no.notify("active;expires=600000", info); res.StatusCode != 200 {
		t.Fatalf("NOTIFY answered %q", res.StartLine())
	}

	timeout := time.After(10 * time.Second)

	for {
		select {
		case e := <-u.Events():
			if e.Deregistered == "" {
				continue
			}

			if e.Deregistered != DeregisteredProbation || e.RetryAfter != 90*time.Second {
				t.Fatalf("deregistered %q after %s, want probation after 90s", e.Deregistered, e.RetryAfter)
			}

			n.PCSCF.RecvNone(200 * time.Millisecond)

			return
		case <-timeout:
			t.Fatal("no deregistration event")
		}
	}
}

// okOutbound answers the protected REGISTER req with a 200 of an outbound registration (RFC 5626 §6) and a
// Flow-Timer of flowTimer seconds (§4.4.1).
func okOutbound(n *network, req *sip.Request, f sip.Flow, flowTimer int) {
	res := sip.NewResponse(req, 200, "")

	contacts, _ := req.Header.Contacts()
	for _, c := range contacts {
		res.Header.Add("Contact", c.String()+";expires=3600")
	}

	res.Header.Add("Require", "outbound")
	res.Header.Add("Flow-Timer", strconv.Itoa(flowTimer))
	res.Header.Add("P-Associated-URI", "<"+msisdn+">")
	n.Reply(n.PC, f, res)
}

func registerOutbound(t *testing.T, n *network, u *testUE, flowTimer int) {
	t.Helper()

	done := uetest.Start(u.Register)

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, true)

	req, f = n.Recv(n.PS)
	okOutbound(n, req, f, flowTimer)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}
}

// RFC 5626 §4.4.1, §4.4.2: a registration with a Flow-Timer kept alive by STUN on its flow, at least that often.
func TestFlowTimer(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{RegID: 1})
	registerOutbound(t, n, u, 1)

	if !u.State().Outbound {
		t.Fatal("not an outbound registration")
	}

	uetest.Eventually(t, "two keep-alives", func() bool { return n.PS.STUNRequests() >= 2 })

	if !u.State().Registered {
		t.Fatal("deregistered with its keep-alives answered")
	}
}

// RFC 5626 §4.4.1, §4.5: a keep-alive not answered ends the registration, its flow failed.
func TestFlowFailed(t *testing.T) {
	saved := pongTimeout
	pongTimeout = 100 * time.Millisecond

	t.Cleanup(func() { pongTimeout = saved })

	n := newNetwork(t)
	u, kernel := n.newUE(Config{RegID: 1})
	registerOutbound(t, n, u, 1)

	n.PS.MuteSTUN(true)

	timeout := time.After(10 * time.Second)

	for {
		select {
		case e := <-u.Events():
			if e.Deregistered == "" {
				continue
			}

			if e.Deregistered != DeregisteredFlowFailed || u.State().Registered || len(u.SAs()) != 0 {
				t.Fatalf("deregistered %q, state %+v, SAs %v", e.Deregistered, u.State(), u.SAs())
			}

			if len(kernel.Removed()) == 0 {
				t.Fatal("SAs of the failed flow kept")
			}

			return
		case <-timeout:
			t.Fatal("no deregistration event")
		}
	}
}

// TS 24.229 §5.1.1.4.1 NOTE 13, RFC 5626 §4.5: a listener of the UE failing for good, its unprotected port or a protected
// one, as when the platform destroys its sockets with the network, ends the registration, its flow failed; the next
// registration listens again on the port, the unprotected one as it was, a protected one anew.
func TestListenerFailed(t *testing.T) {
	for name, port := range map[string]func(*testUE, SASet) uint16{
		"unprotected": func(u *testUE, _ SASet) uint16 { return u.Unprotected().Port() },
		"server":      func(_ *testUE, s SASet) uint16 { return s.Local.PortS },
		"client":      func(_ *testUE, s SASet) uint16 { return s.Local.PortC },
	} {
		t.Run(name, func(t *testing.T) {
			n := newNetwork(t)
			u, kernel := n.newUE(Config{})
			n.Register(u)

			set, unprotected := u.SAs()[0].Set, u.Unprotected()
			failing := port(u, set)

			rcs, err := u.layer.Sockets(netip.AddrPortFrom(u.Local(), failing))
			if err != nil {
				t.Fatal(err)
			}

			// Linux: a listening socket shut down is no longer listening; accept fails with EINVAL.
			var shut error
			if err := rcs[1].Control(func(fd uintptr) { shut = syscall.Shutdown(int(fd), syscall.SHUT_RD) }); err != nil || shut != nil {
				t.Fatalf("shutdown: %v, %v", err, shut)
			}

			timeout := time.After(10 * time.Second)

			for e := (Event{}); e.Deregistered == ""; {
				select {
				case e = <-u.Events():
				case <-timeout:
					t.Fatal("no deregistration event")
				}

				if e.Deregistered != "" && (e.Deregistered != DeregisteredFlowFailed || !errors.Is(e.Err, syscall.EINVAL)) {
					t.Fatalf("deregistered %q: %v; want %q, EINVAL", e.Deregistered, e.Err, DeregisteredFlowFailed)
				}
			}

			if u.State().Registered || len(u.SAs()) != 0 || len(kernel.Removed()) == 0 {
				t.Fatalf("state %+v, SAs %v, removed %v after the flow failed", u.State(), u.SAs(), kernel.Removed())
			}

			n.Register(u)

			again := u.SAs()[0].Set
			if u.Unprotected() != unprotected || slices.Contains([]uint16{again.Local.PortC, again.Local.PortS}, failing) {
				t.Fatalf("registered again on %s, %s; the port %d failed", u.Unprotected(), again, failing)
			}

			if _, err := u.layer.Sockets(unprotected); err != nil {
				t.Fatalf("not listening on the unprotected port again: %v", err)
			}
		})
	}
}

// Without a Flow-Timer, no keep-alive (TS 24.229 §5.1.1.2.1 d).
func TestNoFlowTimer(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{RegID: 1})
	n.Register(u)

	time.Sleep(200 * time.Millisecond)

	if got := n.PS.STUNRequests(); got != 0 {
		t.Fatalf("%d keep-alives without a Flow-Timer", got)
	}
}
