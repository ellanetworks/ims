package icscf

import (
	"reflect"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const offNet = "tel:+16617629239"

func usBGCF() *bgcf.BGCF {
	s := settings.Settings{
		Trunks:    []settings.Trunk{{ID: "telnyx", Name: "Telnyx", Enabled: true}},
		SIPRoutes: []settings.SIPRoute{{ID: "us", Prefix: "+1", Trunks: []string{"telnyx"}}},
	}

	return bgcf.New(func() settings.Settings { return s }, nil)
}

func userUnknownAnswer() *hssAnswer {
	return &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorUserUnknown)}
}

func TestUnknownNumberRoutedToIBCF(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"tel URI", offNet + ";cpc=ordinary", offNet + ";cpc=ordinary"},
		{"SIP URI with user=phone", "sip:+16617629239@" + homeDomain + ";user=phone", offNet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{bgcf: usBGCF(), ibcf: true})
			u := h.fromSCSCF()

			h.hss.answerLIR(userUnknownAnswer())

			invite := u.invite(tt.target, func(r *sip.Request) {
				r.Header.Add("P-Charging-Vector", "icid-value=1234")
				r.Header.Add("P-Served-User", "<"+testIMPU+">;sescase=orig;regstate=reg")
				r.Header.Add("P-Asserted-Identity", "<"+testIMPU+">")
			})
			u.send(invite)

			if lir := h.hss.nextLIR(t); lir.PublicIdentity != offNet || lir.Originating {
				t.Fatalf("LIR = %+v, want a terminating query for %s", lir, offNet)
			}

			req, f := h.ibcf.recv()

			if req.URI.String() != tt.want {
				t.Fatalf("Request-URI = %s, want %s", req.URI, tt.want)
			}

			if got, want := routes(t, req), []string{"sip:" + h.ibcf.sock.Addr().String() + ";lr"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("Routes = %v, want %v", got, want)
			}

			switch {
			case req.Header.Has("P-Served-User"):
				t.Fatal("P-Served-User forwarded to the IBCF")
			case req.Header.Get("P-Charging-Vector") != "icid-value=1234":
				t.Fatalf("P-Charging-Vector = %q, want the icid-value kept", req.Header.Get("P-Charging-Vector"))
			case req.Header.Get("P-Asserted-Identity") == "":
				t.Fatal("P-Asserted-Identity removed")
			case req.Header.Has("Record-Route"):
				t.Fatal("Record-Route added")
			}

			h.ibcf.respond(req, f, 486)
			u.ack(invite, u.wantFinal(486))
			h.scscf.sock.RecvNone(quiet)
		})
	}
}

func (h *harness) fromSCSCF() *ue {
	return &ue{h: h, sock: h.scscf.sock}
}

func TestUnknownNumberNotRoutedToIBCF(t *testing.T) {
	tests := []struct {
		name   string
		opts   harnessOptions
		source func(h *harness) *ue
		edit   func(h *harness, r *sip.Request)
		method string
		target string
		answer *hssAnswer
		want   int
	}{
		{name: "no SIP route", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: "tel:+442079460000", want: 404},
		{
			name: "from the IBCF", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: offNet, want: 404,
			source: func(h *harness) *ue { return &ue{h: h, sock: h.ibcf.sock} },
		},
		{
			name: "from outside the trust domain", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: offNet, want: 404,
			source: func(h *harness) *ue { return h.newUE(untrusted) },
		},
		{
			name: "from the trust domain, not the S-CSCF", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: offNet, want: 404,
			source: func(h *harness) *ue { return h.newUE(loopback) },
		},
		{
			name: "identity not registered", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: offNet, want: 480,
			answer: &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)},
		},
		{name: "local number", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: "tel:6617629239;phone-context=+1", want: 404},
		{name: "SIP URI", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: "sip:+16617629239@" + homeDomain, want: 404},
		{
			name: "from the IBCF, configured IPv4-mapped", opts: harnessOptions{bgcf: usBGCF(), ibcf: true, ibcfMapped: true}, target: offNet,
			want: 404, source: func(h *harness) *ue { return &ue{h: h, sock: h.ibcf.sock} },
		},
		{
			name: "on-net only", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, target: offNet, want: 404,
			edit: func(h *harness, r *sip.Request) { r.Header.Add("Route", "<"+h.icscfURI()+";"+bgcf.OnNetParam+";lr>") },
		},
		{name: "MESSAGE", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, method: "MESSAGE", target: offNet, want: 404},
		{name: "SUBSCRIBE", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, method: "SUBSCRIBE", target: offNet, want: 404},
		{name: "OPTIONS", opts: harnessOptions{bgcf: usBGCF(), ibcf: true}, method: "OPTIONS", target: offNet, want: 404},
		{name: "no BGCF", opts: harnessOptions{ibcf: true}, target: offNet, want: 404},
		{name: "no IBCF", opts: harnessOptions{bgcf: usBGCF()}, target: offNet, want: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.opts)

			u := h.fromSCSCF()
			if tt.source != nil {
				u = tt.source(h)
			}

			answer := tt.answer
			if answer == nil {
				answer = userUnknownAnswer()
			}

			h.hss.answerLIR(answer)

			method := tt.method
			if method == "" {
				method = "INVITE"
			}

			req := siptest.NewRequest(method, tt.target, sip.UDP, u.sock.Addr())
			if tt.edit != nil {
				tt.edit(h, req)
			}

			u.send(req)
			h.hss.nextLIR(t)

			if res := u.wantFinal(tt.want); method == "INVITE" {
				u.ack(req, res)
			}

			if h.ibcf != nil {
				h.ibcf.sock.RecvNone(quiet)
			}

			h.scscf.sock.RecvNone(quiet)
		})
	}
}

func TestOriginatingUnknownUserNotRoutedToIBCF(t *testing.T) {
	h := newHarness(t, harnessOptions{bgcf: usBGCF(), ibcf: true})
	u := h.fromSCSCF()

	h.hss.answerLIR(userUnknownAnswer())

	invite := u.invite(offNet, func(r *sip.Request) {
		r.Header.Add("Route", "<"+h.icscfURI()+";lr;orig>")
		r.Header.Add("P-Served-User", "<tel:+15551230009>")
	})
	u.send(invite)

	if lir := h.hss.nextLIR(t); !lir.Originating {
		t.Fatalf("LIR = %+v, want an originating query", lir)
	}

	u.ack(invite, u.wantFinal(404))
	h.ibcf.sock.RecvNone(quiet)
}

func TestRoutedToIBCFFromTheSCSCF(t *testing.T) {
	tests := []struct {
		name string
		edit func(h *harness, r *sip.Request)
	}{
		{"same address as the IBCF, another port", func(h *harness, r *sip.Request) {
			r.Header.Add("Route", "<"+h.icscfURI()+";lr>")
		}},
		{"retargeted by an AS after an IBCF entry", func(h *harness, r *sip.Request) {
			r.Header.Add("Route", "<"+h.icscfURI()+";lr>")
			r.Header.Add("P-Asserted-Identity", "<sip:+442079460000@"+homeDomain+";user=phone>")
			r.Header.Add("History-Info", "<tel:+15551230002>;index=1")
			r.Header.Add("History-Info", "<"+offNet+";cause=302>;index=1.1")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{bgcf: usBGCF(), ibcf: true})
			u := h.fromSCSCF()

			if u.sock.Addr().Addr() != h.ibcf.sock.Addr().Addr() || u.sock.Addr() == h.ibcf.sock.Addr() {
				t.Fatalf("source %s, want the address of the IBCF %s on another port", u.sock.Addr(), h.ibcf.sock.Addr())
			}

			h.hss.answerLIR(userUnknownAnswer())

			invite := u.invite(offNet, func(r *sip.Request) { tt.edit(h, r) })
			u.send(invite)
			h.hss.nextLIR(t)

			req, f := h.ibcf.recv()
			if req.URI.String() != offNet {
				t.Fatalf("Request-URI = %s, want %s", req.URI, offNet)
			}

			h.ibcf.respond(req, f, 486)
			u.ack(invite, u.wantFinal(486))
		})
	}
}

func TestOnNetNumberReachesSubscriber(t *testing.T) {
	h := newHarness(t, harnessOptions{bgcf: usBGCF(), ibcf: true})
	s := h.scscf
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	invite := u.invite("tel:+15551230002", func(r *sip.Request) {
		r.Header.Add("Route", "<"+h.icscfURI()+";"+bgcf.OnNetParam+";lr>")
	})
	u.send(invite)
	h.hss.nextLIR(t)

	req, f := s.recv()
	if got, want := routes(t, req), []string{s.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want only %v", got, want)
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
	h.ibcf.sock.RecvNone(quiet)
}
