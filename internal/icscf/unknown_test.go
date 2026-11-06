package icscf

import (
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const offNet = "tel:+16617629239"

func userUnknownAnswer() *hssAnswer {
	return &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorUserUnknown)}
}

func (h *harness) fromSCSCF() *ue {
	return &ue{h: h, sock: h.scscf.sock}
}

func TestUnknownNumberNotRoutedOn(t *testing.T) {
	tests := []struct {
		name   string
		source func(h *harness) *ue
		method string
		target string
		answer *hssAnswer
		want   int
	}{
		{name: "from the S-CSCF", source: (*harness).fromSCSCF, target: offNet, want: 404},
		{name: "SIP URI with user=phone", source: (*harness).fromSCSCF, target: "sip:+16617629239@" + homeDomain + ";user=phone", want: 404},
		{name: "from the trust domain", source: func(h *harness) *ue { return h.newUE(loopback) }, target: offNet, want: 404},
		{name: "from outside the trust domain", source: func(h *harness) *ue { return h.newUE(untrusted) }, target: offNet, want: 404},
		{
			name: "identity not registered", source: (*harness).fromSCSCF, target: offNet, want: 480,
			answer: &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)},
		},
		{name: "MESSAGE", source: (*harness).fromSCSCF, method: "MESSAGE", target: offNet, want: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := tt.source(h)

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
			u.send(req)
			h.hss.nextLIR(t)

			if res := u.wantFinal(tt.want); method == "INVITE" {
				u.ack(req, res)
			}

			h.scscf.sock.RecvNone(quiet)
		})
	}
}

func breakoutRoute(h *harness) func(*sip.Request) {
	return func(r *sip.Request) { r.Header.Add("Route", "<"+h.icscfURI()+";lr;"+BreakoutParam+">") }
}

// TS 24.229 clause 5.3.2.1
func TestUnknownNumberBrokenOut(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{"tel URI", offNet},
		{"SIP URI with user=phone", "sip:+16617629239@" + homeDomain + ";user=phone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.fromSCSCF()
			h.hss.answerLIR(userUnknownAnswer())

			req := u.invite(tt.target, breakoutRoute(h))
			u.send(req)

			if lir := h.hss.nextLIR(t); lir.PublicIdentity != offNet || lir.Originating {
				t.Fatalf("LIR %+v, want a terminating query for %s", lir, offNet)
			}

			got, f := h.ibcf.RecvRequest()

			if routes := routes(t, got); got.URI.String() != offNet || len(routes) != 1 ||
				routes[0] != "sip:"+h.ibcf.Addr().String()+";lr" {
				t.Fatalf("IBCF got %s with Route %v, want %s routed to the IBCF alone", got.URI, routes, offNet)
			}

			res := sip.NewResponse(got, 486, "")
			_ = res.Header.SetToTag(sip.NewTag())
			h.ibcf.Send(f.Transport, f.Remote, res)

			u.ack(req, u.wantFinal(486))
			h.hss.noCx(t)
		})
	}
}

func TestUnknownNumberNotBrokenOut(t *testing.T) {
	tests := []struct {
		name   string
		source func(h *harness) *ue
		method string
		opts   harnessOptions
		answer *hssAnswer
		want   int
	}{
		{name: "from outside the trust domain", source: func(h *harness) *ue { return h.newUE(untrusted) }, want: 404},
		{name: "from a trusted node other than the S-CSCF", source: func(h *harness) *ue { return h.newUE(loopback) }, want: 404},
		{name: "MESSAGE from the S-CSCF", source: (*harness).fromSCSCF, method: "MESSAGE", want: 404},
		{name: "no IBCF", source: (*harness).fromSCSCF, opts: harnessOptions{noIBCF: true}, want: 404},
		{
			name: "identity not registered", source: (*harness).fromSCSCF, want: 480,
			answer: &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)},
		},
		{name: "HSS unreachable", source: (*harness).fromSCSCF, want: 480, answer: &hssAnswer{result: success(diameter.ResultUnableToDeliver)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.opts)
			u := tt.source(h)

			answer := tt.answer
			if answer == nil {
				answer = userUnknownAnswer()
			}

			h.hss.answerLIR(answer)

			method := tt.method
			if method == "" {
				method = "INVITE"
			}

			req := siptest.NewRequest(method, offNet, sip.UDP, u.sock.Addr())
			breakoutRoute(h)(req)
			u.send(req)
			h.hss.nextLIR(t)

			if res := u.wantFinal(tt.want); method == "INVITE" {
				u.ack(req, res)
			}

			h.ibcf.RecvNone(quiet)
			h.scscf.sock.RecvNone(quiet)
		})
	}
}

func TestKnownNumberNotBrokenOut(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)
	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: h.scscf.uri()})

	u.send(u.invite(offNet, breakoutRoute(h)))
	h.hss.nextLIR(t)

	if got, _ := h.scscf.recv(); got.URI.String() != offNet {
		t.Fatalf("S-CSCF got %s, want %s", got.URI, offNet)
	}

	h.ibcf.RecvNone(quiet)
}
