package sip

import (
	"strings"
	"testing"
)

// TS 24.379 §10.1.2.2.2.1: the uri of an <entry> carries Accept-Contact header fields and, in the hname "body", the
// SDP offer and the mcptt-info of the call.
func TestURIEmbeddedMCPTTEntry(t *testing.T) {
	var e Envelope

	e.Header.Add("Accept-Contact", "*;+g.3gpp.mcptt;require;explicit")
	e.Header.Add("Accept-Contact", `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt";require;explicit`)
	e.SetBodies(NewBodyPart("application/sdp", []byte(mcpttSDP)),
		NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte(mcpttInfo)))

	u := URI{Scheme: "sip", User: "mcptt-id-b", Host: "mcptt-op.gov"}
	u.SetEmbedded(e)

	if strings.ContainsAny(u.Headers, " \r\n<>\";") || strings.Contains(u.Headers, "Content-Length") {
		t.Fatalf("headers component not escaped: %q", u.Headers)
	}

	parsed, err := ParseURI(u.String())
	if err != nil {
		t.Fatal(err)
	}

	got, err := parsed.Embedded()
	if err != nil {
		t.Fatal(err)
	}

	contacts := got.Header.Values("Accept-Contact")
	if len(contacts) != 2 || contacts[1] != e.Header.Values("Accept-Contact")[1] {
		t.Errorf("Accept-Contact %q, want %q", contacts, e.Header.Values("Accept-Contact"))
	}

	if sdp, ok, err := got.Content().Find("application/sdp"); err != nil || !ok || string(sdp.Body) != mcpttSDP {
		t.Errorf("SDP %q, %v, %v", sdp.Body, ok, err)
	}

	if info, ok, err := got.Content().Find("application/vnd.3gpp.mcptt-info+xml"); err != nil || !ok || string(info.Body) != mcpttInfo {
		t.Errorf("mcptt-info %q, %v, %v", info.Body, ok, err)
	}
}

func TestURIEmbeddedEscaping(t *testing.T) {
	for _, tc := range []struct {
		name, value, escaped string
	}{
		{"Subject", "project x", "Subject=project%20x"},
		{"Priority", "urgent", "Priority=urgent"},
		{"To", "sip:bob@biloxi.com", "To=sip:bob%40biloxi.com"},
		{"X", `a;b=c,"d"&e%f`, "X=a%3Bb%3Dc%2C%22d%22%26e%25f"},
		{"X", "[]/?:+$-_.!~*'()", "X=[]/?:+$-_.!~*'()"},
		{"X", "", "X="},
	} {
		var e Envelope

		e.Header.Add(tc.name, tc.value)

		var u URI

		u.SetEmbedded(e)

		if u.Headers != tc.escaped {
			t.Errorf("%s: %q, want %q", tc.value, u.Headers, tc.escaped)
		}

		got, err := u.Embedded()
		if err != nil || got.Header.Get(tc.name) != tc.value {
			t.Errorf("%s: unescaped to %q, %v", tc.escaped, got.Header.Get(tc.name), err)
		}
	}

	var e Envelope

	e.SetBody("text/plain", []byte("line\r\n\x00binary\xff"))

	var u URI

	u.SetEmbedded(e)

	got, err := u.Embedded()
	if err != nil || string(got.Body) != string(e.Body) || got.Header.ContentType() != "text/plain" {
		t.Errorf("body %q (%v), %v", got.Body, got.Header, err)
	}

	if (URI{}).Headers != "" {
		t.Fatal("zero URI with headers")
	}

	if got, err := (URI{Scheme: "sip", Host: "h"}).Embedded(); err != nil || got.Header != nil || got.Body != nil {
		t.Errorf("no headers component: %v, %v", got, err)
	}
}

func TestURIHeadersSyntax(t *testing.T) {
	for _, s := range []string{
		"sip:a@b?x", "sip:a@b?=v", "sip:a@b?x=1&", "sip:a@b?x=1&&y=2", "sip:a@b?x=%zz", "sip:a@b?x=a{b",
		"sip:a@b?x=a=b", "sip:a@b?x%=1",
	} {
		if u, err := ParseURI(s); err == nil {
			t.Errorf("ParseURI(%q) = %q, want error (RFC 3261 §25.1)", s, u)
		}
	}

	for _, s := range []string{"sip:a@b?%20x=1", "sip:a@b?x=1&body=a&body=b", "sip:a@b?Subject=a%0D%0Ab"} {
		u, err := ParseURI(s)
		if err != nil {
			t.Fatalf("ParseURI(%q): %v", s, err)
		}

		if e, err := u.Embedded(); err == nil {
			t.Errorf("%s: Embedded() = %v, want error", s, e)
		}
	}
}

func TestFormRequest(t *testing.T) {
	u, err := ParseURI("sip:bob@biloxi.com;transport=tcp;method=REFER" +
		"?Subject=hello&From=sip:mallory%40evil.com&Call-ID=x&CSeq=1%20INVITE&Via=SIP/2.0/UDP%20evil.com" +
		"&Record-Route=%3Csip:evil.com%3E&Route=%3Csip:evil.com%3E&Contact=%3Csip:evil.com%3E&Supported=evil" +
		"&Allow=INVITE&User-Agent=evil&Accept=x/y&Content-Length=99&Content-Type=text/plain&body=hi")
	if err != nil {
		t.Fatal(err)
	}

	req, err := FormRequest(u, "INVITE")
	if err != nil {
		t.Fatal(err)
	}

	if req.Method != "REFER" || req.URI.String() != "sip:bob@biloxi.com;transport=tcp" {
		t.Errorf("request line %s, want REFER to the URI without method and headers (RFC 3261 §19.1.5)", req.StartLine())
	}

	for _, name := range unhonoured {
		if name != "Content-Length" && req.Header.Has(name) {
			t.Errorf("%s honoured: %q", name, req.Header.Get(name))
		}
	}

	if req.Header.Get("Subject") != "hello" || req.Header.ContentType() != "text/plain" || string(req.Body) != "hi" ||
		req.Header.Get("Content-Length") != "2" {
		t.Errorf("request %q", req.Bytes())
	}

	plain, err := FormRequest(URI{Scheme: "sip", Host: "h"}, "INVITE")
	if err != nil || plain.Method != "INVITE" || plain.Body != nil {
		t.Errorf("without method parameter: %v, %v", plain, err)
	}

	for _, s := range []string{"sip:h?body=x", "sip:h?Content-Type=multipart/mixed%3Bboundary%3Db&body=x", "sip:h;method=%40", "tel:+1"} {
		u, err := ParseURI(s)
		if err != nil {
			t.Fatal(err)
		}

		if req, err := FormRequest(u, "INVITE"); err == nil {
			t.Errorf("FormRequest(%s) = %q, want error", s, req.Bytes())
		}
	}
}

// TS 24.379 §10.1.2.2.2.1: the Refer-To of a REFER for a pre-established session is a cid URL to a resource-lists
// body of the REFER.
func TestReferToContentID(t *testing.T) {
	lists := NewBodyPart("application/resource-lists+xml", []byte("<resource-lists/>"))
	lists.SetContentID("cn35t8jf02@ims-op.net")

	refer := NewRequest("REFER", URI{Scheme: "sip", Host: "pf-a1.ims-op.net"})
	refer.Header.Add("r", "<cid:cn35t8jf02@ims-op.net>")
	refer.SetBodies(NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte(mcpttInfo)), lists)

	to, err := refer.Header.ReferTo()
	if err != nil || to.URI.Scheme != "cid" {
		t.Fatalf("Refer-To %v, %v", to, err)
	}

	p, ok, err := refer.Content().ByContentID(to.URI.String())
	if err != nil || !ok || p.MediaType() != "application/resource-lists+xml" {
		t.Errorf("cid target %s, %v", p.MediaType(), ok)
	}

	single := NewRequest("REFER", URI{Scheme: "sip", Host: "h"})
	single.SetBodies(lists)

	if p, ok, err := single.Content().ByContentID("cid:cn35t8jf02@ims-op.net"); err != nil || !ok || string(p.Body) != "<resource-lists/>" {
		t.Errorf("cid target of a single body: %q, %v", p.Body, ok)
	}

	if _, ok, _ := single.Content().ByContentID("cid:other@ims-op.net"); ok {
		t.Error("unknown cid found")
	}

	if _, err := (Header{}).ReferTo(); err == nil {
		t.Error("ReferTo() without Refer-To")
	}
}
