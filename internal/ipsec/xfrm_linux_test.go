//go:build linux

package ipsec

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestKernelStructSizes(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("sizes are for 64-bit platforms")
	}

	for _, tc := range []struct {
		name      string
		got, want uintptr
	}{
		{"xfrm_selector", unsafe.Sizeof(xfrmSelector{}), 56},
		{"xfrm_id", unsafe.Sizeof(xfrmID{}), 24},
		{"xfrm_usersa_info", unsafe.Sizeof(xfrmUsersaInfo{}), 224},
		{"xfrm_usersa_id", unsafe.Sizeof(xfrmUsersaID{}), 24},
		{"xfrm_userpolicy_info", unsafe.Sizeof(xfrmUserpolicyInfo{}), 168},
		{"xfrm_userpolicy_id", unsafe.Sizeof(xfrmUserpolicyID{}), 64},
		{"xfrm_user_tmpl", unsafe.Sizeof(xfrmUserTmpl{}), 64},
	} {
		if tc.got != tc.want {
			t.Errorf("sizeof(struct %s) = %d, want %d", tc.name, tc.got, tc.want)
		}
	}

	if off := unsafe.Offsetof(xfrmUserTmpl{}.saddr); off != 28 {
		t.Errorf("offsetof(xfrm_user_tmpl.saddr) = %d, want 28", off)
	}
}

func TestInstallCarriesTraffic(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		for _, alg := range []struct {
			i Integrity
			e Encryption
		}{
			{HMACSHA196, EncryptionNull},
			{HMACMD596, EncryptionNull},
			{HMACSHA196, AESCBC},
			{HMACMD596, AESCBC},
		} {
			t.Run(fmt.Sprintf("v6=%v/%s/%s", v6, alg.i, alg.e), func(t *testing.T) {
				l := newLab(t)
				s := l.set(t, v6, alg.i, alg.e)
				l.install(t, s)

				p, u := s.Local, s.Remote
				pc, ps := listenUDP(t, l.p, p.Addr, p.PortC), listenUDP(t, l.p, p.Addr, p.PortS)
				uc, us := listenUDP(t, l.u, u.Addr, u.PortC), listenUDP(t, l.u, u.Addr, u.PortS)

				// TS 33.203 §7.1: over UDP, the UE sends from port_uc to
				// port_ps and the P-CSCF from port_pc to port_us. Every path
				// is checked, as replies over TCP use the others.
				for _, c := range []struct {
					name string
					ok   bool
				}{
					{"uc -> ps", udpDelivered(t, uc, ps, 2*time.Second)},
					{"ps -> uc", udpDelivered(t, ps, uc, 2*time.Second)},
					{"pc -> us", udpDelivered(t, pc, us, 2*time.Second)},
					{"us -> pc", udpDelivered(t, us, pc, 2*time.Second)},
				} {
					if !c.ok {
						t.Errorf("UDP %s not delivered", c.name)
					}
				}

				_, _, _, _ = pc.Close(), ps.Close(), uc.Close(), us.Close()

				tcpExchange(t, l.u, netip.AddrPortFrom(u.Addr, u.PortC), l.p, netip.AddrPortFrom(p.Addr, p.PortS))
				tcpExchange(t, l.p, netip.AddrPortFrom(p.Addr, p.PortC), l.u, netip.AddrPortFrom(u.Addr, u.PortS))

				packets, policies := l.px.owned(t)
				if len(packets) != 4 || policies != 4 {
					t.Fatalf("P-CSCF has %d SAs and %d policies, want 4 and 4", len(packets), policies)
				}

				for _, spi := range []uint32{p.SPIC, p.SPIS, u.SPIC, u.SPIS} {
					if packets[spi] == 0 {
						t.Errorf("SA with SPI %d carried no packets", spi)
					}
				}
			})
		}
	}
}

func TestProtectedPortsRejectBadPackets(t *testing.T) {
	l := newLab(t)
	s := l.set(t, false, HMACSHA196, EncryptionNull)

	if err := l.px.Install(s, l.keys); err != nil {
		t.Fatal(err)
	}

	p, u := s.Local, s.Remote
	ps := listenUDP(t, l.p, p.Addr, p.PortS)
	uc := listenUDP(t, l.u, u.Addr, u.PortC)

	if udpDelivered(t, uc, ps, 300*time.Millisecond) {
		t.Error("unprotected packet delivered on a protected port")
	}

	wrong := Keys{CK: l.keys.CK, IK: []byte("0000000000000000")}
	if err := l.ux.Install(s.Reverse(), wrong); err != nil {
		t.Fatal(err)
	}

	if udpDelivered(t, uc, ps, 300*time.Millisecond) {
		t.Error("packet with a bad integrity check delivered")
	}

	if err := l.ux.Remove(s.Reverse()); err != nil {
		t.Fatal(err)
	}

	if err := l.ux.Install(s.Reverse(), l.keys); err != nil {
		t.Fatal(err)
	}

	if !udpDelivered(t, uc, ps, 2*time.Second) {
		t.Error("protected packet not delivered")
	}

	if err := l.px.Remove(s); err != nil {
		t.Fatal(err)
	}

	if err := l.ux.Remove(s.Reverse()); err != nil {
		t.Fatal(err)
	}

	if !udpDelivered(t, uc, ps, 2*time.Second) {
		t.Error("unprotected packet not delivered after Remove")
	}

	if packets, policies := l.px.owned(t); len(packets) != 0 || policies != 0 {
		t.Errorf("after Remove: %d SAs and %d policies", len(packets), policies)
	}

	if err := l.px.Remove(s); err != nil {
		t.Errorf("second Remove: %v", err)
	}
}

func TestInstallConflictRollsBack(t *testing.T) {
	l := newLab(t)
	a := l.set(t, false, HMACSHA196, EncryptionNull)

	if err := l.px.Install(a, l.keys); err != nil {
		t.Fatal(err)
	}

	// b reuses a's outbound SPIs towards the same UE: its first two SAs are
	// added, the third collides.
	b := l.set(t, false, HMACSHA196, EncryptionNull)
	b.Remote.SPIC, b.Remote.SPIS = a.Remote.SPIC, a.Remote.SPIS

	err := l.px.Install(b, l.keys)
	if !errors.Is(err, unix.EEXIST) {
		t.Fatalf("Install(conflicting) = %v, want EEXIST", err)
	}

	if packets, policies := l.px.owned(t); len(packets) != 4 || policies != 4 {
		t.Errorf("after rollback: %d SAs and %d policies, want 4 and 4", len(packets), policies)
	}

	if missing, err := l.px.Reconcile([]Set{a}); err != nil || missing != nil {
		t.Errorf("Reconcile = %v, %v; the first set was damaged", missing, err)
	}

	if err := l.px.Install(a, l.keys); !errors.Is(err, unix.EEXIST) {
		t.Errorf("Install(duplicate) = %v, want EEXIST", err)
	}

	if packets, _ := l.px.owned(t); len(packets) != 4 {
		t.Errorf("duplicate Install removed the original: %d SAs", len(packets))
	}
}

func TestReconcile(t *testing.T) {
	l := newLab(t)
	a := l.set(t, false, HMACSHA196, EncryptionNull)
	b := l.set(t, true, HMACMD596, AESCBC)

	for _, s := range []Set{a, b} {
		if err := l.px.Install(s, l.keys); err != nil {
			t.Fatal(err)
		}
	}

	l.p.ip(t, "xfrm", "state", "add", "src", "10.0.0.1", "dst", "10.0.0.9", "proto", "esp", "spi", "0x77",
		"mode", "transport", "auth-trunc", "hmac(sha1)", "0x"+fmt.Sprintf("%040x", 1), "96", "enc", "ecb(cipher_null)", "")
	l.p.ip(t, "xfrm", "policy", "add", "src", "10.0.0.1", "dst", "10.0.0.9", "dir", "out",
		"tmpl", "proto", "esp", "mode", "transport")

	missing, err := l.px.Reconcile([]Set{a})
	if err != nil || missing != nil {
		t.Fatalf("Reconcile = %v, %v", missing, err)
	}

	packets, policies := l.px.owned(t)
	if len(packets) != 4 || policies != 4 {
		t.Errorf("after Reconcile: %d SAs and %d policies, want 4 and 4", len(packets), policies)
	}

	for _, spi := range []uint32{a.Local.SPIC, a.Local.SPIS, a.Remote.SPIC, a.Remote.SPIS} {
		if _, ok := packets[spi]; !ok {
			t.Errorf("SA %d of the kept set deleted", spi)
		}
	}

	var foreign []byte

	l.p.do(func() {
		foreign, _ = exec.CommandContext(context.Background(), "ip", "xfrm", "state", "list", "spi", "0x77").Output()
	})

	if len(foreign) == 0 {
		t.Error("Reconcile deleted an SA it does not own")
	}

	if err := l.px.deleteSA(a.sas()[2]); err != nil {
		t.Fatal(err)
	}

	missing, err = l.px.Reconcile([]Set{a})
	if err != nil || len(missing) != 1 || missing[0] != a {
		t.Fatalf("Reconcile after deleting an SA = %v, %v; want [a]", missing, err)
	}

	if err := l.px.Remove(a); err != nil {
		t.Fatalf("Remove(partial): %v", err)
	}

	if missing, err := l.px.Reconcile(nil); err != nil || missing != nil {
		t.Fatalf("Reconcile(nil) = %v, %v", missing, err)
	}

	if packets, policies := l.px.owned(t); len(packets) != 0 || policies != 0 {
		t.Errorf("Reconcile(nil) left %d SAs and %d policies", len(packets), policies)
	}
}

func TestProbe(t *testing.T) {
	l := newLab(t)

	// An interrupted probe left an SA behind.
	l.p.ip(t, "xfrm", "state", "add", "src", "192.0.2.1", "dst", "10.0.0.1", "proto", "esp", "spi", "0x1001",
		"mode", "transport", "auth-trunc", "hmac(sha1)", "0x"+fmt.Sprintf("%040x", 1), "96", "enc", "ecb(cipher_null)", "",
		"sel", "src", "192.0.2.1", "dst", "10.0.0.1", "sport", "1", "dport", "2")

	for _, a := range []netip.Addr{l.p4, l.p6} {
		if err := l.px.Probe(a); err != nil {
			t.Errorf("Probe(%s): %v", a, err)
		}
	}

	if packets, policies := l.px.owned(t); len(packets) != 0 || policies != 0 {
		t.Errorf("Probe left %d SAs and %d policies", len(packets), policies)
	}
}

func TestInstallValidates(t *testing.T) {
	l := newLab(t)
	good := l.set(t, false, HMACSHA196, EncryptionNull)

	for name, s := range map[string]Set{
		"mixed families": func() Set { s := good; s.Remote.Addr = l.u6; return s }(),
		"no port":        func() Set { s := good; s.Remote.PortS = 0; return s }(),
		"mapped address": func() Set { s := good; s.Remote.Addr = netip.AddrFrom16(l.u4.As16()); return s }(),
		"no SPI":         func() Set { s := good; s.Local.SPIC = 0; return s }(),
		"algorithm":      func() Set { s := good; s.Integrity = "hmac-sha2-256-128"; return s }(),
	} {
		if err := l.px.Install(s, l.keys); err == nil {
			t.Errorf("Install(%s): no error", name)
		}
	}

	if err := l.px.Install(good, Keys{CK: l.keys.CK, IK: []byte("short")}); !errors.Is(err, ErrBadKeys) {
		t.Errorf("Install(short IK) = %v, want ErrBadKeys", err)
	}

	if packets, policies := l.px.owned(t); len(packets) != 0 || policies != 0 {
		t.Errorf("failed Installs left %d SAs and %d policies", len(packets), policies)
	}
}

func TestNetlinkErrorMessage(t *testing.T) {
	l := newLab(t)
	s := l.set(t, false, HMACSHA196, EncryptionNull)

	bad := algo{name: "hmac(nonexistent)", trunc: 96, key: make([]byte, 20)}
	crypt, _ := encryptionAlgo(EncryptionNull)

	err := l.px.newSA(s.sas()[0], bad, crypt)

	var ne *NetlinkError
	if !errors.As(err, &ne) {
		t.Fatalf("newSA(unknown algorithm) = %v, want a NetlinkError", err)
	}
}
