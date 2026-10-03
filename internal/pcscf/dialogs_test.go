package pcscf

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func (d *ueDialog) fromUE(s *ipsecScene, u *ue, method, cseq string, routes []string) *sip.Request {
	r := siptest.NewRequest(method, "sip:"+s.scscf.Addr().String(), sip.UDP, u.us.Addr())
	r.Header.Set("From", "<"+testIMPU+">;tag="+d.ueTag)
	r.Header.Set("To", "<"+testIMPU+">;tag="+d.scscfTag)
	r.Header.Set("Call-ID", d.sub.Header.CallID())
	r.Header.Set("CSeq", cseq+" "+method)
	r.Header.Add("Route", strings.Join(routes, ", "))

	return r
}

func (d *ueDialog) toUE(s *ipsecScene, method, cseq string, routes []string) *sip.Request {
	contacts, _ := d.sub.Header.Contacts()

	r := siptest.NewRequest(method, contacts[0].URI.String(), sip.UDP, s.scscf.Addr())
	r.Header.Set("From", "<"+testIMPU+">;tag="+d.scscfTag)
	r.Header.Set("To", "<"+testIMPU+">;tag="+d.ueTag)
	r.Header.Set("Call-ID", d.sub.Header.CallID())
	r.Header.Set("CSeq", cseq+" "+method)
	r.Header.Add("Route", strings.Join(routes, ", "))

	return r
}

func ueRoutes(d *ueDialog) []string {
	routes := slices.Clone(d.rr)
	slices.Reverse(routes)

	return routes
}

func TestAckThroughThePCSCF(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)
	d := s.subscribeUE(t, u)
	sets := s.installed()

	ack := d.fromUE(s, u, "ACK", "1", ueRoutes(d))
	ack.Header.Add("Security-Verify", "ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=3;port-s=4")
	u.uc.Send(sip.UDP, s.ps, ack)

	got, f := s.scscf.RecvRequest()
	if got.Method != "ACK" || got.Header.Has("Route") || got.Header.Has("Security-Verify") {
		t.Fatalf("S-CSCF got:\n%s\nwant the ACK without Route or Security-Verify", got)
	}

	if f.Remote != s.pcscf {
		t.Errorf("ACK from %s, want the P-CSCF port %s", f.Remote, s.pcscf)
	}

	s.scscf.Send(sip.UDP, s.pcscf, d.toUE(s, "ACK", "1", d.rr))

	got, f = u.us.RecvRequest()
	if got.Method != "ACK" || got.Header.Has("Route") {
		t.Fatalf("UE got:\n%s\nwant the ACK without Route", got)
	}

	if want := netip.AddrPortFrom(loopback, sets[0].Local.PortC); f.Remote != want {
		t.Errorf("ACK from %s, want the protected client port %s", f.Remote, want)
	}

	forged := slices.Clone(ueRoutes(d))
	for i, e := range forged {
		forged[i] = strings.Replace(e, token+"@", "otherflow@", 1)
	}

	u.uc.Send(sip.UDP, s.ps, d.fromUE(s, u, "ACK", "1", forged))
	s.scscf.RecvNone(quiet)

	outsider := siptest.NewSocket(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.3"), 0))
	outsider.Send(sip.UDP, s.pcscf, d.toUE(s, "ACK", "1", d.rr))
	u.us.RecvNone(quiet)
}

func TestCancelInDialogInvite(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)
	d := s.subscribeUE(t, u)

	invite := d.fromUE(s, u, "INVITE", "2", ueRoutes(d))
	invite.Header.Set("Contact", ueContact(u))
	u.uc.Send(sip.UDP, s.ps, invite)

	wantStatus(t, first(u.us.RecvResponse()), 100)

	fwd, f := s.scscf.RecvRequest()
	if fwd.Method != "INVITE" {
		t.Fatalf("S-CSCF got %s, want the re-INVITE", fwd.Method)
	}

	s.scscf.Send(f.Transport, f.Remote, sip.NewResponse(fwd, 100, ""))

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		t.Fatal(err)
	}

	u.uc.Send(sip.UDP, s.ps, cancel)

	fc, cf := s.scscf.RecvRequest()
	if fc.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the CANCEL", fc.Method)
	}

	if v1, v2 := mustTopVia(t, fc), mustTopVia(t, fwd); v1.Branch() != v2.Branch() {
		t.Errorf("CANCEL branch %s, want the INVITE's %s", v1.Branch(), v2.Branch())
	}

	s.scscf.Send(cf.Transport, cf.Remote, sip.NewResponse(fc, 200, ""))

	terminated := sip.NewResponse(fwd, 487, "")
	s.scscf.Send(f.Transport, f.Remote, terminated)

	codes := map[int]bool{}

	for range 2 {
		res, _ := u.us.RecvResponse()
		codes[res.StatusCode] = true
	}

	if !codes[200] || !codes[487] {
		t.Fatalf("UE got %v, want 200 to the CANCEL and 487 to the INVITE", codes)
	}

	if ack, _ := s.scscf.RecvRequest(); ack.Method != "ACK" {
		t.Errorf("S-CSCF got %s, want the ACK to the 487", ack.Method)
	}
}

func mustTopVia(t *testing.T, m sip.Message) sip.Via {
	t.Helper()

	v, err := m.Env().Header.TopVia()
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func TestCancelFromAnotherUE(t *testing.T) {
	s, u := newIPsecRegScene(t)
	s.registerOverIPsec(u)
	d := s.subscribeUE(t, u)

	invite := d.fromUE(s, u, "INVITE", "2", ueRoutes(d))
	invite.Header.Set("Contact", ueContact(u))
	u.uc.Send(sip.UDP, s.ps, invite)
	wantStatus(t, first(u.us.RecvResponse()), 100)

	fwd, f := s.scscf.RecvRequest()
	s.scscf.Send(f.Transport, f.Remote, sip.NewResponse(fwd, 100, ""))

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		t.Fatal(err)
	}

	outsider := siptest.NewSocket(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.3"), 0))
	outsider.Send(sip.UDP, s.pcscf, cancel)
	s.scscf.RecvNone(quiet)

	u.uc.Send(sip.UDP, s.ps, cancel)

	if fc, _ := s.scscf.RecvRequest(); fc.Method != "CANCEL" {
		t.Fatalf("S-CSCF got %s, want the UE's CANCEL", fc.Method)
	}
}
