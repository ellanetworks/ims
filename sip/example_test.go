package sip_test

import (
	"fmt"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

const register = "REGISTER sip:ims.example.org SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.5:5060;branch=z9hG4bK1;rport\r\n" +
	"Max-Forwards: 70\r\n" +
	"From: <sip:alice@ims.example.org>;tag=a1\r\n" +
	"To: <sip:alice@ims.example.org>\r\n" +
	"Call-ID: c1\r\n" +
	"CSeq: 1 REGISTER\r\n" +
	"Contact: <sip:alice@10.0.0.5:5060>;expires=600\r\n" +
	"Content-Length: 0\r\n\r\n"

func Example() {
	msg, err := sip.Parse([]byte(register))
	if err != nil {
		fmt.Println(err)
		return
	}

	req := msg.(*sip.Request)
	if err := req.Validate(); err != nil {
		fmt.Println(err)
		return
	}

	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag("t1")
	res.Header.Add("Expires", "600")

	fmt.Print(strings.ReplaceAll(res.String(), "\r\n", "\n"))
	// Output:
	// SIP/2.0 200 OK
	// Via: SIP/2.0/UDP 10.0.0.5:5060;branch=z9hG4bK1;rport
	// From: <sip:alice@ims.example.org>;tag=a1
	// To: <sip:alice@ims.example.org>;tag=t1
	// Call-ID: c1
	// CSeq: 1 REGISTER
	// Content-Length: 0
	// Expires: 600
}

func ExampleHeader_PopFirst() {
	h := sip.Header{
		{Name: "Route", Value: "<sip:pcscf.ims.example.org;lr>, <sip:scscf.ims.example.org;lr>"},
	}

	top, _ := h.PopFirst("Route")
	fmt.Println(top)
	fmt.Println(h.Get("Route"))
	// Output:
	// <sip:pcscf.ims.example.org;lr>
	// <sip:scscf.ims.example.org;lr>
}

func ExampleStreamReader_Next() {
	r := sip.NewStreamReader(strings.NewReader("\r\n\r\n"+register), 0)

	for {
		msg, ka, err := r.Next()
		if err != nil {
			return
		}

		switch {
		case ka == sip.KeepalivePing:
			fmt.Println("ping: answer with CRLF")
		case msg != nil:
			fmt.Println(msg.StartLine())
		}
	}
	// Output:
	// ping: answer with CRLF
	// REGISTER sip:ims.example.org SIP/2.0
}
