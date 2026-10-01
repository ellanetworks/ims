package sip

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const invite = "INVITE sip:bob@[2001:db8::2]:5060;transport=tcp SIP/2.0\r\n" +
	"v: SIP/2.0/TCP [2001:db8::1]:5060;branch=z9hG4bKa;rport\r\n" +
	"Via: SIP/2.0/UDP p1.example.com;branch=z9hG4bKb, SIP/2.0/UDP p2.example.com;branch=z9hG4bKc\r\n" +
	"Max-Forwards: 70\r\n" +
	"f: \"Alice, A\" <sip:alice@example.com>;tag=a1\r\n" +
	"To: tel:+15551230002\r\n" +
	"Call-ID: c1@example.com\r\n" +
	"CSeq: 7 INVITE\r\n" +
	"Contact: <sip:alice@[2001:db8::1]:5060;transport=tcp>;+sip.instance=\"<urn:gsma:imei:35-1-2>\"\r\n" +
	"Supported:\r\n 100rel,\r\n\ttimer\r\n" +
	"c: application/sdp\r\n" +
	"l: 4\r\n" +
	"\r\n" +
	"v=0\r\n"

func mustParse(t *testing.T, raw string) Message {
	t.Helper()

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	return m
}

func TestParseRequest(t *testing.T) {
	req := mustParse(t, invite).(*Request)

	if req.Method != "INVITE" || req.URI.Host != "[2001:db8::2]" || req.URI.Port != 5060 {
		t.Errorf("start line: %s %+v", req.Method, req.URI)
	}

	if got := req.Header.Get("Supported"); got != "100rel, timer" {
		t.Errorf("unfolded Supported = %q", got)
	}

	if got := req.Header.Elements("Via"); len(got) != 3 {
		t.Errorf("Via elements = %q", got)
	}

	if got := req.Header.ContentType(); got != "application/sdp" {
		t.Errorf("ContentType = %q", got)
	}

	if string(req.Body) != "v=0\r" {
		t.Errorf("body = %q", req.Body)
	}

	if got := string(req.Bytes()); got != invite[:len(invite)-1] {
		t.Errorf("Bytes() =\n%q\nwant\n%q", got, invite[:len(invite)-1])
	}

	if err := req.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestParseDatagramBody(t *testing.T) {
	const head = "OPTIONS sip:a@b SIP/2.0\r\nVia: SIP/2.0/UDP h;branch=z9hG4bK1\r\n"

	for _, tc := range []struct {
		name, raw, body string
		err             bool
	}{
		{"no Content-Length runs to the end", head + "\r\nabc", "abc", false},
		{"extra octets ignored", head + "l: 1\r\n\r\nabc", "a", false},
		{"short body", head + "l: 4\r\n\r\nabc", "", true},
		{"negative", head + "Content-Length: -1\r\n\r\n", "", true},
		{"two Content-Length", head + "l: 0\r\nContent-Length: 0\r\n\r\n", "", true},
		{"leading CRLF", "\r\n\r\n" + head + "\r\n", "", false},
	} {
		m, err := Parse([]byte(tc.raw))
		if (err != nil) != tc.err {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}

		if err == nil && string(m.(*Request).Body) != tc.body {
			t.Errorf("%s: body = %q", tc.name, m.(*Request).Body)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		raw         string
		withRequest bool
	}{
		{"INVITE sip:a@b SIP/2.0\r\nVia: x\r\n", false},
		{" INVITE sip:a@b SIP/2.0\r\n\r\n", false},
		{"INVITE sip:a@b SIP/2.0\r\nNoColon\r\nCall-ID: x\r\n\r\n", true},
		{"INVITE sip:a@b SIP/2.0\r\nBad Name: x\r\nCall-ID: x\r\n\r\n", true},
		{"INV<ITE sip:a@b SIP/2.0\r\n\r\n", false},
		{"SIP/2.0 2000 OK\r\n\r\n", false},
		{"SIP/2.0 099 Low\r\n\r\n", false},
		{"SIP 200 OK\r\nCall-ID: x\r\n\r\n", true},
		{"INVITE <sip:a@b> SIP/2.0\r\nCall-ID: x\r\n\r\n", true},
		{"INVITE sip:a@b; lr SIP/2.0\r\nCall-ID: x\r\n\r\n", true},
		{"INVITE sip:a@b SIP/2.0 \r\nCall-ID: x\r\n\r\n", true},
		{"INVITE sip:a@b SIP/2.0\r\nCall-ID: x\r\nl: 9\r\n\r\nabc", true},
		{"INVITE sip:a@b SIP/2.0\r\nCall-ID: x\r\nl: 1\r\nl: 1\r\n\r\na", true},
	} {
		_, err := Parse([]byte(tc.raw))

		var perr *ParseError
		if !errors.As(err, &perr) {
			t.Errorf("%q: err = %v, want a *ParseError", tc.raw, err)
			continue
		}

		if (perr.Request != nil) != tc.withRequest {
			t.Errorf("%q: Request = %v, want present=%v", tc.raw, perr.Request, tc.withRequest)
		}

		if perr.Request != nil && perr.Request.Header.CallID() != "x" {
			t.Errorf("%q: Request fields = %v", tc.raw, perr.Request.Header)
		}
	}
}

func TestStartLineLossless(t *testing.T) {
	for _, start := range []string{
		"INVITE sip:a@b:05060 SIP/2.0",
		"INVITE sip:a@b:0 SIP/2.0",
		"INVITE sip:a:@b;lr= SIP/2.0",
		"INVITE tel:+1-555;phone-context= SIP/2.0",
		"SIP/2.0 200",
	} {
		raw := start + "\r\nCall-ID: x\r\n\r\n"
		if got := string(mustParse(t, raw).Bytes()); got != raw {
			t.Errorf("Bytes() = %q, want %q", got, raw)
		}
	}

	req := mustParse(t, "INVITE sip:a@b:05060 SIP/2.0\r\n\r\n").(*Request)
	req.URI.Params.Set("lr", "")

	if got := req.StartLine(); got != "INVITE sip:a@b:5060;lr SIP/2.0" {
		t.Errorf("StartLine() after a change = %q", got)
	}
}

func TestParseResponse(t *testing.T) {
	res := mustParse(t, "SIP/2.0 183 \r\nVia: SIP/2.0/UDP h;branch=z9hG4bK1\r\n\r\n").(*Response)

	if res.StatusCode != 183 || res.Reason != "" || !res.IsProvisional() || res.IsSuccess() {
		t.Errorf("response = %d %q", res.StatusCode, res.Reason)
	}

	if got := res.StartLine(); got != "SIP/2.0 183 " {
		t.Errorf("StartLine = %q", got)
	}
}

func TestFieldWireForm(t *testing.T) {
	raw := "MESSAGE sip:a@b SIP/2.0\r\n" +
		"TO :\r\n sip:x@y ;  lr\r\n" +
		"Subject:\tfolded\r\n  twice\r\n" +
		"Call-ID:id\r\n" +
		"\r\n"

	req := mustParse(t, raw).(*Request)

	if got := string(req.Bytes()); got != raw {
		t.Fatalf("Bytes() = %q", got)
	}

	if got := req.Header.Values("to"); !slices.Equal(got, []string{"sip:x@y ;  lr"}) {
		t.Errorf("To = %q", got)
	}

	if got := req.Header.Get("subject"); got != "folded twice" {
		t.Errorf("Subject = %q", got)
	}

	if err := req.Header.SetToTag("2"); err != nil {
		t.Fatal(err)
	}

	req.Header[1].Value = "changed"

	want := "MESSAGE sip:a@b SIP/2.0\r\nTO: <sip:x@y>;lr;tag=2\r\nSubject: changed\r\nCall-ID:id\r\n\r\n"
	if got := string(req.Bytes()); got != want {
		t.Errorf("Bytes() after changes = %q, want %q", got, want)
	}
}

func TestIsKeepalive(t *testing.T) {
	for raw, want := range map[string]bool{"\r\n\r\n": true, "\r\n": true, "": false, "\r\nx": false} {
		if got := IsKeepalive([]byte(raw)); got != want {
			t.Errorf("IsKeepalive(%q) = %v", raw, got)
		}
	}
}

func TestParseLongHeader(t *testing.T) {
	long := strings.Repeat("a", 10000)
	raw := "OPTIONS sip:a@b SIP/2.0\r\nX-Long: " + long + "\r\n\r\n"

	if got := mustParse(t, raw).Env().Header.Get("x-long"); got != long {
		t.Errorf("long header lost: %d bytes", len(got))
	}
}

func TestRejectHeaderInjection(t *testing.T) {
	const head = "OPTIONS sip:a@b SIP/2.0\r\nVia: SIP/2.0/UDP h;branch=z9hG4bK1\r\n"

	for _, field := range []string{
		"Subject: hi\nVia: SIP/2.0/UDP evil;branch=z9hG4bKevil",
		"Subject: hi\rRoute: <sip:evil;lr>",
		"Subject: a\x00b",
	} {
		if _, err := Parse([]byte(head + field + "\r\n\r\n")); err == nil {
			t.Errorf("accepted %q", field)
		}
	}

	if _, err := Parse([]byte("SIP/2.0 200 O\x00K\r\n\r\n")); err == nil {
		t.Error("accepted a NUL in the Reason-Phrase")
	}

	ok := head + "Subject: a\tb\r\n\tc\r\nTo: \"NUL:\\\x00\" <sip:a@b>\r\n\r\n"
	if m := mustParse(t, ok); string(m.Bytes()) != ok {
		t.Errorf("Bytes() = %q", m.Bytes())
	}
}
