package server

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

func startServer(t *testing.T) *Server {
	t.Helper()

	srv := &Server{Config: testConfig(t), Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	return srv
}

func sipListener(t *testing.T, srv *Server, role string, a netip.Addr) netip.AddrPort {
	t.Helper()

	for _, l := range srv.sip.Listeners() {
		if l.Role == role && l.Address.Addr() == a {
			return l.Address
		}
	}

	t.Fatalf("no SIP listener on %s in %v", a, srv.sip.Listeners())

	return netip.AddrPort{}
}

func wantResponse(t *testing.T, ue *siptest.Socket, code int, method string) *sip.Response {
	t.Helper()

	res, _ := ue.RecvResponse()

	cseq, err := res.Header.CSeq()
	if err != nil {
		t.Fatal(err)
	}

	if res.StatusCode != code || cseq.Method != method {
		t.Fatalf("got %q to %s, want %d to %s", res.StartLine(), cseq.Method, code, method)
	}

	return res
}

func TestSIPPlaceholder(t *testing.T) {
	srv := startServer(t)

	for _, addr := range []netip.Addr{loopback, loopback6} {
		for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
			t.Run(addr.String()+"/"+string(tr), func(t *testing.T) {
				pcscf := sipListener(t, srv, rolePCSCF, addr)
				ue := siptest.NewSocket(t, netip.AddrPortFrom(addr, 0))
				self := "sip:" + imsRealm + ":" + strconv.Itoa(int(pcscf.Port()))

				t.Run("OPTIONS", func(t *testing.T) {
					ue.Send(tr, pcscf, siptest.NewRequest("OPTIONS", self, tr, ue.Addr()))

					res := wantResponse(t, ue, 200, "OPTIONS")

					want := map[string]string{
						"Allow": placeholderAllow, "Accept": "", "Accept-Encoding": "", "Accept-Language": "en", "Supported": "",
					}
					for name, value := range want {
						if !res.Header.Has(name) || res.Header.Get(name) != value {
							t.Errorf("%s = %q (present %t), want %q", name, res.Header.Get(name), res.Header.Has(name), value)
						}
					}
				})

				t.Run("OPTIONS to a user", func(t *testing.T) {
					ue.Send(tr, pcscf, siptest.NewRequest("OPTIONS", "sip:+15551230002@"+imsRealm, tr, ue.Addr()))
					wantResponse(t, ue, 480, "OPTIONS")
				})

				t.Run("REGISTER with the HSS down", func(t *testing.T) {
					register := siptest.NewRequest("REGISTER", "sip:"+imsRealm, tr, ue.Addr())
					register.Header.Set("To", "<sip:001010000000001@"+imsRealm+">")
					register.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;"+
						"port-c=6301;port-s=6300;alg=hmac-sha-1-96;ealg=null")
					ue.Send(tr, pcscf, register)

					wantResponse(t, ue, 480, "REGISTER")
				})

				t.Run("SUBSCRIBE", func(t *testing.T) {
					ue.Send(tr, pcscf, siptest.NewRequest("SUBSCRIBE", "sip:"+imsRealm, tr, ue.Addr()))

					res := wantResponse(t, ue, 405, "SUBSCRIBE")
					if got := res.Header.Get("Allow"); got != placeholderAllow {
						t.Fatalf("Allow = %q, want %q", got, placeholderAllow)
					}
				})

				t.Run("unknown method", func(t *testing.T) {
					ue.Send(tr, pcscf, siptest.NewRequest("FOO", "sip:"+imsRealm, tr, ue.Addr()))
					wantResponse(t, ue, 501, "FOO")
				})

				t.Run("INVITE CANCEL", func(t *testing.T) {
					invite := siptest.NewRequest("INVITE", "sip:+15551230002@"+imsRealm, tr, ue.Addr())
					ue.Send(tr, pcscf, invite)
					wantResponse(t, ue, 100, "INVITE")

					cancel, err := sip.NewCancel(invite)
					if err != nil {
						t.Fatal(err)
					}

					ue.Send(tr, pcscf, cancel)

					got := map[string]*sip.Response{}

					for range 2 {
						res, _ := ue.RecvResponse()

						cseq, err := res.Header.CSeq()
						if err != nil {
							t.Fatal(err)
						}

						got[cseq.Method] = res
					}

					if res := got["CANCEL"]; res == nil || res.StatusCode != 200 {
						t.Fatalf("response to CANCEL = %v, want 200", res)
					}

					res := got["INVITE"]
					if res == nil || res.StatusCode != 487 {
						t.Fatalf("response to INVITE = %v, want 487", res)
					}

					ack, err := sip.NewAck(invite, res)
					if err != nil {
						t.Fatal(err)
					}

					ue.Send(tr, pcscf, ack)
				})
			})
		}
	}
}

func TestSIPShutdownClosesListeners(t *testing.T) {
	srv := &Server{Config: testConfig(t), Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var listeners []netip.AddrPort

	for _, l := range srv.sip.Listeners() {
		listeners = append(listeners, l.Address)
	}

	if len(listeners) != 12 {
		t.Fatalf("listeners = %v, want one per role or protected port, and address", listeners)
	}

	srv.Shutdown(ctx)

	for _, l := range listeners {
		var d net.Dialer

		if c, err := d.DialContext(ctx, "tcp", l.String()); err == nil {
			_ = c.Close()

			t.Fatalf("TCP connect to %s succeeded after Shutdown", l)
		}

		var lc net.ListenConfig

		pc, err := lc.ListenPacket(ctx, "udp", l.String())
		if err != nil {
			t.Fatalf("UDP port %s still held after Shutdown: %v", l, err)
		}

		_ = pc.Close()
	}
}

func TestSIPListenFailureFailsStart(t *testing.T) {
	taken := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	cfg := testConfig(t)
	cfg.PCSCF.Port = int(taken.Addr().Port())
	cfg.SIP.Addresses = []netip.Addr{loopback6, loopback}

	srv := &Server{Config: cfg, Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}

	ctx := context.Background()

	err := srv.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "start SIP") {
		srv.Shutdown(ctx)
		t.Fatalf("Start = %v, want a SIP listen error", err)
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", netip.AddrPortFrom(loopback6, taken.Addr().Port()).String())
	if err != nil {
		t.Fatalf("[::1]:%d still held after the failed Start: %v", taken.Addr().Port(), err)
	}

	_ = ln.Close()

	taken.Close()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start after the port is free: %v", err)
	}

	srv.Shutdown(ctx)
}

func TestSIPPlaceholderRefusesUncancelledInvite(t *testing.T) {
	h := newPlaceholderHandler(slog.New(slog.DiscardHandler), nil)
	h.inviteTimeout = 50 * time.Millisecond

	layer, _ := siptest.NewLayer(t, transaction.Config{Handler: h, Logger: slog.New(slog.DiscardHandler)})
	pcscf := siptest.ListenLayer(t, layer, loopback)
	ue := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	ue.Send(sip.UDP, pcscf, siptest.NewRequest("INVITE", "sip:+15551230002@"+imsRealm, sip.UDP, ue.Addr()))
	wantResponse(t, ue, 100, "INVITE")
	wantResponse(t, ue, 480, "INVITE")
}

func TestSIPPlaceholderIsSelf(t *testing.T) {
	h := newPlaceholderHandler(slog.New(slog.DiscardHandler), []string{imsRealm, "pcscf.example.org"})
	h.addListener(netip.MustParseAddrPort("10.0.0.5:5060"))
	h.addListener(netip.MustParseAddrPort("[2001:db8::5]:5070"))

	tests := []struct {
		uri  string
		want bool
	}{
		{"sip:" + imsRealm, true},
		{"sip:" + strings.ToUpper(imsRealm) + ":5060", true},
		{"sip:pcscf.example.org;transport=tcp", true},
		{"sip:pcscf.example.org:5070", true},
		{"sip:10.0.0.5", true},
		{"sip:[2001:db8::5]:5070", true},
		{"sip:pcscf.example.org:5080", false},
		{"sips:pcscf.example.org", false},
		{"sip:10.0.0.5:5070", false},
		{"sip:[2001:db8::5]", false},
		{"sip:10.0.0.6", false},
		{"sip:other.example.org", false},
		{"sip:+15551230002@" + imsRealm, false},
		{"tel:+15551230002", false},
	}

	for _, tt := range tests {
		u, err := sip.ParseURI(tt.uri)
		if err != nil {
			t.Fatal(err)
		}

		if got := h.isSelf(u); got != tt.want {
			t.Errorf("isSelf(%s) = %t, want %t", tt.uri, got, tt.want)
		}
	}
}
