package sip

import (
	"net/netip"
	"strings"
	"testing"
)

func TestNewResponse(t *testing.T) {
	req := mustParse(t, "INVITE sip:b@x SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP p;branch=z9hG4bK2\r\n"+
		"v: SIP/2.0/UDP ue;branch=z9hG4bK1;rport=5060;received=10.0.0.1\r\n"+
		"Record-Route: <sip:p;lr>\r\n"+
		"From: <sip:a@x>;tag=1\r\n"+
		"To:\r\n <sip:b@x>\r\n"+
		"Call-ID: c\r\n"+
		"CSeq: 1 INVITE\r\n"+
		"Timestamp: 54\r\n"+
		"Content-Length: 0\r\n\r\n").(*Request)
	req.Flow = Flow{Transport: UDP, Local: netip.MustParseAddrPort("10.0.0.2:5060"), Remote: netip.MustParseAddrPort("10.0.0.1:5060")}

	trying := NewResponse(req, 100, "")
	if trying.Flow != req.Flow || trying.Header.Get("Timestamp") != "54" || trying.Reason != "Trying" {
		t.Errorf("100 = %s", trying)
	}

	res := NewResponse(req, 486, "")
	if err := res.Header.SetToTag("t9"); err != nil {
		t.Fatal(err)
	}

	want := "SIP/2.0 486 Busy Here\r\n" +
		"Via: SIP/2.0/UDP p;branch=z9hG4bK2\r\n" +
		"v: SIP/2.0/UDP ue;branch=z9hG4bK1;rport=5060;received=10.0.0.1\r\n" +
		"From: <sip:a@x>;tag=1\r\n" +
		"To: <sip:b@x>;tag=t9\r\n" +
		"Call-ID: c\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Content-Length: 0\r\n\r\n"
	if got := res.String(); got != want {
		t.Errorf("486 =\n%s\nwant\n%s", got, want)
	}

	if got, _ := req.Header.To(); got.Tag() != "" {
		t.Error("SetToTag changed the request")
	}

	if err := res.Header.SetToTag("other"); err != nil || res.Header.Get("To") != "<sip:b@x>;tag=t9" {
		t.Errorf("SetToTag replaced the tag: %s", res.Header.Get("To"))
	}

	if err := res.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestNewCancel(t *testing.T) {
	inv := mustParse(t, "INVITE sip:b@x;transport=tcp SIP/2.0\r\n"+
		"Via: SIP/2.0/TCP ue;branch=z9hG4bK1;rport, SIP/2.0/UDP old;branch=z9hG4bK0\r\n"+
		"Route: <sip:p1;lr>\r\n"+
		"Route: <sip:p2;lr>\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:a@x>;tag=1\r\n"+
		"To: <sip:b@x>\r\n"+
		"Call-ID: c\r\n"+
		"CSeq: 42 INVITE\r\n"+
		"Contact: <sip:a@ue>\r\n"+
		"Content-Length: 0\r\n\r\n").(*Request)

	c, err := NewCancel(inv)
	if err != nil {
		t.Fatal(err)
	}

	want := "CANCEL sip:b@x;transport=tcp SIP/2.0\r\n" +
		"Via: SIP/2.0/TCP ue;branch=z9hG4bK1;rport\r\n" +
		"Route: <sip:p1;lr>\r\n" +
		"Route: <sip:p2;lr>\r\n" +
		"Max-Forwards: 70\r\n" +
		"From: <sip:a@x>;tag=1\r\n" +
		"To: <sip:b@x>\r\n" +
		"Call-ID: c\r\n" +
		"CSeq: 42 CANCEL\r\n" +
		"Content-Length: 0\r\n\r\n"
	if got := c.String(); got != want {
		t.Errorf("CANCEL =\n%s\nwant\n%s", got, want)
	}

	if err := c.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	c.URI.Params.Set("transport", "udp")

	if inv.URI.String() != "sip:b@x;transport=tcp" {
		t.Error("CANCEL shares the INVITE's Request-URI parameters")
	}

	inv.Method = "BYE"
	if _, err := NewCancel(inv); err == nil {
		t.Error("NewCancel accepted a BYE")
	}
}

func TestNewAck(t *testing.T) {
	inv := mustParse(t, "INVITE sip:b@x SIP/2.0\r\n"+
		"v: SIP/2.0/UDP ue;branch=z9hG4bK1;rport=5060;received=192.0.2.1, SIP/2.0/UDP old;branch=z9hG4bK0\r\n"+
		"Route: <sip:p1;lr>, <sip:p2;lr>\r\n"+
		"Max-Forwards: 69\r\n"+
		"f: <sip:a@x>;tag=1\r\n"+
		"t: <sip:b@x>\r\n"+
		"i: c\r\n"+
		"CSeq: 42 INVITE\r\n"+
		"Content-Type: application/sdp\r\n"+
		"Content-Length: 3\r\n\r\nv=0").(*Request)

	res := NewResponse(inv, 486, "")
	if err := res.Header.SetToTag("b"); err != nil {
		t.Fatal(err)
	}

	ack, err := NewAck(inv, res)
	if err != nil {
		t.Fatal(err)
	}

	want := "ACK sip:b@x SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP ue;branch=z9hG4bK1;rport=5060;received=192.0.2.1\r\n" +
		"Route: <sip:p1;lr>, <sip:p2;lr>\r\n" +
		"Max-Forwards: 70\r\n" +
		"From: <sip:a@x>;tag=1\r\n" +
		"To: <sip:b@x>;tag=b\r\n" +
		"Call-ID: c\r\n" +
		"CSeq: 42 ACK\r\n" +
		"Content-Length: 0\r\n\r\n"
	if got := ack.String(); got != want {
		t.Errorf("ACK =\n%s\nwant\n%s", got, want)
	}

	if err := ack.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	if _, err := NewAck(inv, NewResponse(inv, 200, "")); err == nil {
		t.Error("NewAck accepted a 2xx")
	}

	inv.Method = "BYE"
	if _, err := NewAck(inv, res); err == nil {
		t.Error("NewAck accepted a BYE")
	}
}

func TestNewBranchAndTag(t *testing.T) {
	b1, b2 := NewBranch(), NewBranch()
	if b1 == b2 || !strings.HasPrefix(b1, MagicCookie) || len(b1) < len(MagicCookie)+16 {
		t.Errorf("branches %q %q", b1, b2)
	}

	if t1, t2 := NewTag(), NewTag(); t1 == t2 || !isToken(t1) {
		t.Error("tags")
	}
}

func TestStatelessTag(t *testing.T) {
	tag := NewStatelessTag()
	if !IsStatelessTag(tag) || !isToken(tag) || tag == NewStatelessTag() {
		t.Errorf("stateless tag %q", tag)
	}

	if IsStatelessTag(NewTag()) {
		t.Error("NewTag is recognized as stateless")
	}
}

func TestReasonPhrase(t *testing.T) {
	if ReasonPhrase(494) != "Security Agreement Required" || ReasonPhrase(299) != "Status 299" {
		t.Error("ReasonPhrase")
	}
}

func TestClone(t *testing.T) {
	req := mustParse(t, invite).(*Request)
	c := req.Clone()

	c.Header[0].Value = "x"
	c.Body[0] = 'X'
	c.URI.Params[0].Value = "udp"

	if req.Header[0].Value == "x" || req.Body[0] == 'X' || req.URI.Params[0].Value == "udp" {
		t.Error("Clone shares storage")
	}
}

func TestSetBody(t *testing.T) {
	req := NewRequest("MESSAGE", URI{Scheme: "sip", Host: "x"})
	if got := req.String(); got != "MESSAGE sip:x SIP/2.0\r\nContent-Length: 0\r\n\r\n" {
		t.Errorf("NewRequest = %q", got)
	}

	res := NewResponse(req, 200, "")
	res.SetBody("application/sdp", []byte("v=0\r\n"))

	if res.Header.ContentType() != "application/sdp" || res.Header.Get("Content-Length") != "5" || res.Header.Count("Content-Length") != 1 {
		t.Errorf("after SetBody: %s", res)
	}

	m, _, err := NewStreamReader(strings.NewReader(res.String()+res.String()), 0).Next()
	if err != nil || string(m.(*Response).Body) != "v=0\r\n" {
		t.Errorf("framed body = %v, %v", m, err)
	}

	res.SetBody("", nil)

	if res.Header.Has("Content-Type") || res.Header.Get("Content-Length") != "0" || res.Body != nil {
		t.Errorf("after clearing: %s", res)
	}
}
