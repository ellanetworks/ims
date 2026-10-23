package controlling

import (
	"testing"

	"github.com/ellanetworks/ims/sip"
)

// TS 24.229 §5.7.1.9: the Feature-Caps of the ICSI goes in target refresh requests and their 1xx or 2xx responses.
func TestOfTargetRefresh(t *testing.T) {
	req := func(method string) *sip.Request {
		r := sip.NewRequest(method, sip.URI{Scheme: "sip", User: "u", Host: "example.com"})
		r.Header.Set("CSeq", "1 "+method)

		return r
	}

	cases := []struct {
		msg  sip.Message
		want bool
	}{
		{req("INVITE"), true},
		{req("UPDATE"), true},
		{req("BYE"), false},
		{sip.NewResponse(req("INVITE"), 100, ""), false},
		{sip.NewResponse(req("INVITE"), 183, ""), true},
		{sip.NewResponse(req("INVITE"), 200, ""), true},
		{sip.NewResponse(req("UPDATE"), 200, ""), true},
		{sip.NewResponse(req("INVITE"), 486, ""), false},
		{sip.NewResponse(req("BYE"), 200, ""), false},
		{sip.NewResponse(req("MESSAGE"), 200, ""), false},
	}

	for _, c := range cases {
		if got := ofTargetRefresh(c.msg); got != c.want {
			t.Errorf("%s: %v", c.msg.StartLine(), got)
		}
	}
}
