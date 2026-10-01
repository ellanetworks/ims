package transaction_test

import (
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

func TestInvalidRequests(t *testing.T) {
	tests := []struct {
		name    string
		start   string
		replace [2]string
		want    int
	}{
		{"wrong version", "OPTIONS sip:p@x SIP/7.0", [2]string{}, 505},
		{"unsupported scheme", "OPTIONS http://x SIP/2.0", [2]string{}, 416},
		{"CSeq mismatch", "OPTIONS sip:p@x SIP/2.0", [2]string{"CSeq: 1 OPTIONS", "CSeq: 1 BYE"}, 400},
		{"missing Call-ID", "OPTIONS sip:p@x SIP/2.0", [2]string{"Call-ID: c\r\n", ""}, 400},
		{"RFC 2543 branch", "OPTIONS sip:p@x SIP/2.0", [2]string{"branch=z9hG4bKv1", "branch=1"}, 400},
		{"malformed Request-URI", "OPTIONS sip:[1 SIP/2.0", [2]string{}, 400},
		{"malformed header", "OPTIONS sip:p@x SIP/2.0", [2]string{"Max-Forwards: 70", "Max-Forwards 70"}, 400},
		{"invalid ACK", "ACK sip:p@x SIP/2.0", [2]string{"CSeq: 1 ACK", "CSeq: 1 BYE"}, 0},
		{"malformed ACK", "ACK sip:[1 SIP/2.0", [2]string{}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			method, _, _ := strings.Cut(tt.start, " ")
			msg := tt.start + "\r\n" +
				"Via: SIP/2.0/UDP " + h.peer.Addr().String() + ";branch=z9hG4bKv1" + sip.NewTag() + ";rport\r\n" +
				"Max-Forwards: 70\r\n" +
				"From: <sip:ue@x>;tag=1\r\n" +
				"To: <sip:p@x>\r\n" +
				"Call-ID: c\r\n" +
				"CSeq: 1 " + method + "\r\n" +
				"Content-Length: 0\r\n\r\n"

			if tt.replace[0] != "" {
				msg = strings.Replace(msg, tt.replace[0], tt.replace[1], 1)
			}

			h.peer.SendRaw(sip.UDP, h.local, []byte(msg))

			if tt.want != 0 {
				res := h.wantResponse(tt.want)
				if to, _ := res.Header.To(); to.Tag() == "" {
					t.Errorf("%d without a To tag", tt.want)
				}
			}

			h.serverBarrier()
			h.peer.RecvNone(0)
			h.tu.None(0)
		})
	}
}
