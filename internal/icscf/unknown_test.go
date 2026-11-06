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

func TestServes(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		answer  *hssAnswer
		want    bool
		failed  bool
		queried string
	}{
		{name: "subscriber", target: "tel:+15551230002", answer: &hssAnswer{result: success(diameter.ResultSuccess)}, want: true, queried: "tel:+15551230002"},
		{
			name: "not registered", target: "tel:+15551230002", want: true, queried: "tel:+15551230002",
			answer: &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)},
		},
		{name: "unknown", target: offNet, answer: userUnknownAnswer(), queried: offNet},
		{name: "SIP URI with user=phone", target: "sip:+16617629239@" + homeDomain + ";user=phone", answer: userUnknownAnswer(), queried: offNet},
		{name: "HSS unreachable", target: offNet, answer: &hssAnswer{result: success(diameter.ResultUnableToDeliver)}, failed: true, queried: offNet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			h.hss.answerLIR(tt.answer)

			u, err := sip.ParseURI(tt.target)
			if err != nil {
				t.Fatal(err)
			}

			got, err := h.ic.Serves(t.Context(), u)

			if lir := h.hss.nextLIR(t); lir.PublicIdentity != tt.queried || lir.Originating {
				t.Fatalf("LIR %+v, want a terminating query for %s", lir, tt.queried)
			}

			if got != tt.want || (err != nil) != tt.failed {
				t.Fatalf("Serves = %v, %v, want %v, failed %v", got, err, tt.want, tt.failed)
			}
		})
	}
}
