package sip

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

const mcpttSDP = "v=0\r\no=- 1 1 IN IP6 5555::aaa:bbb:ccc:eee\r\ns=-\r\nc=IN IP6 5555::aaa:bbb:ccc:eee\r\nt=0 0\r\n" +
	"m=audio 3456 RTP/AVP 97\r\na=rtpmap:97 AMR\r\ni=speech\r\n" +
	"m=application 3457 udp MCPTT\r\na=fmtp:MCPTT mc_queueing;mc_implicit_request\r\n"

const mcpttInfo = `<?xml version="1.0"?>` + "\r\n" +
	`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params><session-type>prearranged</session-type>` +
	`</mcptt-Params></mcpttinfo>`

// TS 24.379 table A.1.3-1, with the part separators and line ends of RFC 2046 §5.1.1.
var mcpttInvite = "--boundary1\r\n" +
	"Content-Type: application/sdp\r\n\r\n" + mcpttSDP + "\r\n" +
	"--boundary1\r\n" +
	"Content-Type: application/vnd.3gpp.mcptt-info+xml\r\n\r\n" + mcpttInfo + "\r\n" +
	"--boundary1--\r\n"

func TestParseMultipart(t *testing.T) {
	m, err := ParseMultipart(`multipart/mixed;boundary="boundary1"`, []byte(mcpttInvite))
	if err != nil {
		t.Fatal(err)
	}

	if m.Subtype != "mixed" || m.Boundary != "boundary1" || len(m.Parts) != 2 {
		t.Fatalf("got %s %q with %d parts", m.Subtype, m.Boundary, len(m.Parts))
	}

	for i, want := range []struct{ mediaType, body string }{
		{"application/sdp", mcpttSDP},
		{"application/vnd.3gpp.mcptt-info+xml", mcpttInfo},
	} {
		p := m.Parts[i]
		if p.MediaType() != want.mediaType || string(p.Body) != want.body {
			t.Errorf("part %d: %s %q, want %s %q", i, p.MediaType(), p.Body, want.mediaType, want.body)
		}
	}

	if got := m.Bytes(); string(got) != mcpttInvite {
		t.Errorf("serialization changed:\n%q\nwant\n%q", got, mcpttInvite)
	}
}

func TestParseMultipartWireForms(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		want                    []string
	}{
		{
			name:        "preamble and epilogue",
			contentType: "multipart/mixed; boundary=b",
			body:        "preamble\r\n--b\r\nContent-Type: text/plain\r\n\r\none\r\n--b--\r\nepilogue",
			want:        []string{"one"},
		},
		{
			name:        "transport padding after delimiters",
			contentType: "multipart/mixed;boundary=b",
			body:        "--b  \r\nContent-Type: text/plain\r\n\r\none\r\n--b\t\r\n\r\ntwo\r\n--b--",
			want:        []string{"one", "two"},
		},
		{
			name:        "part without header fields",
			contentType: "multipart/mixed;boundary=b",
			body:        "--b\r\n\r\nplain\r\n--b--",
			want:        []string{"plain"},
		},
		{
			name:        "empty part body",
			contentType: "multipart/mixed;boundary=b",
			body:        "--b\r\nContent-Type: text/plain\r\n\r\n\r\n--b--",
			want:        []string{""},
		},
		{
			name:        "body with CRLF and dashes",
			contentType: "multipart/mixed;boundary=b",
			body:        "--b\r\nContent-Type: text/plain\r\n\r\nline\r\n-b\r\n--\r\n\r\n--b--",
			want:        []string{"line\r\n-b\r\n--\r\n"},
		},
		{
			name:        "boundary with quoted characters",
			contentType: `Multipart/Related ; boundary="a b:c=d"`,
			body:        "--a b:c=d\r\nContent-Type: text/plain\r\n\r\none\r\n--a b:c=d--",
			want:        []string{"one"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseMultipart(tc.contentType, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}

			var got []string
			for _, p := range m.Parts {
				got = append(got, string(p.Body))
			}

			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("parts %q, want %q", got, tc.want)
			}

			again, err := ParseMultipart(m.ContentType(), m.Bytes())
			if err != nil {
				t.Fatalf("serialization does not parse: %v\n%q", err, m.Bytes())
			}

			if len(again.Parts) != len(m.Parts) {
				t.Fatalf("round trip: %d parts, want %d", len(again.Parts), len(m.Parts))
			}

			for i := range m.Parts {
				if !bytes.Equal(again.Parts[i].Body, m.Parts[i].Body) {
					t.Errorf("round trip: part %d %q, want %q", i, again.Parts[i].Body, m.Parts[i].Body)
				}
			}
		})
	}
}

func TestParseMultipartErrors(t *testing.T) {
	many := strings.Repeat("--b\r\n\r\nx\r\n", maxBodyParts+1) + "--b--"

	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"not multipart", "application/sdp", mcpttInvite},
		{"malformed media type", "multipart", mcpttInvite},
		{"no boundary", "multipart/mixed", mcpttInvite},
		{"empty boundary", `multipart/mixed;boundary=""`, "----\r\n"},
		{"boundary too long", "multipart/mixed;boundary=" + strings.Repeat("b", 71), "--b--"},
		{"boundary ending with a space", `multipart/mixed;boundary="b "`, "--b --"},
		{"no delimiter", "multipart/mixed;boundary=b", "nothing here"},
		{"no close delimiter", "multipart/mixed;boundary=b", "--b\r\n\r\nx"},
		{"text after a delimiter", "multipart/mixed;boundary=b", "--bx\r\n\r\nx\r\n--b--"},
		{"line starting with the boundary in a body", "multipart/mixed;boundary=b", "--b\r\n\r\nx\r\n--bx\r\n--b--"},
		{"no part", "multipart/mixed;boundary=b", "--b--"},
		{"malformed part header", "multipart/mixed;boundary=b", "--b\r\nno colon\r\n\r\nx\r\n--b--"},
		{"bare LF in a part header", "multipart/mixed;boundary=b", "--b\r\nA: 1\nB: 2\r\n\r\nx\r\n--b--"},
		{"too many parts", "multipart/mixed;boundary=b", many},
	} {
		if m, err := ParseMultipart(tc.contentType, []byte(tc.body)); err == nil {
			t.Errorf("%s: got %d parts, want error", tc.name, len(m.Parts))
		}
	}
}

func TestParseMediaType(t *testing.T) {
	for _, tc := range []struct {
		in, want, boundary string
	}{
		{"application/sdp", "application/sdp", ""},
		{" Application/SDP ", "application/sdp", ""},
		{`multipart/mixed;boundary="b 1"`, "multipart/mixed", `"b 1"`},
		{"multipart/mixed ; boundary = b1 ; x", "multipart/mixed", "b1"},
		{"application/vnd.3gpp.mcptt-info+xml", "application/vnd.3gpp.mcptt-info+xml", ""},
	} {
		mt, ps, err := ParseMediaType(tc.in)
		if err != nil {
			t.Errorf("ParseMediaType(%q): %v", tc.in, err)
			continue
		}

		b, _ := ps.Get("boundary")
		if mt != tc.want || b != tc.boundary {
			t.Errorf("ParseMediaType(%q) = %q, boundary %q; want %q, %q", tc.in, mt, b, tc.want, tc.boundary)
		}
	}

	for _, in := range []string{"", "sdp", "/sdp", "application/", "a/b c", "a/b;x=\"y"} {
		if mt, _, err := ParseMediaType(in); err == nil {
			t.Errorf("ParseMediaType(%q) = %q, want error", in, mt)
		}
	}
}

func TestNewMultipart(t *testing.T) {
	info := NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte(mcpttInfo))
	sdp := NewBodyPart("application/sdp", []byte(mcpttSDP))
	lists := NewBodyPart("application/resource-lists+xml", []byte("<resource-lists/>"))

	m := NewMultipart(info, lists, sdp)

	var got []string
	for _, p := range m.Parts {
		got = append(got, p.MediaType())
	}

	want := "application/sdp application/vnd.3gpp.mcptt-info+xml application/resource-lists+xml"
	if strings.Join(got, " ") != want {
		t.Errorf("order %q, want %s (SDP first, TS 24.379 §6.5)", got, want)
	}

	if !validBoundary(m.Boundary) || !strings.HasPrefix(m.ContentType(), "multipart/mixed;boundary=") {
		t.Errorf("Content-Type %q", m.ContentType())
	}

	again, err := ParseMultipart(m.ContentType(), m.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	for i, p := range again.Parts {
		if !bytes.Equal(p.Body, m.Parts[i].Body) || p.MediaType() != m.Parts[i].MediaType() {
			t.Errorf("part %d: %s %q, want %s %q", i, p.MediaType(), p.Body, m.Parts[i].MediaType(), m.Parts[i].Body)
		}
	}
}

func TestNewMultipartAvoidsBoundaryInBodies(t *testing.T) {
	m := NewMultipart(NewBodyPart("text/plain", []byte("a")), NewBodyPart("text/plain", []byte("b")))

	clash := NewBodyPart("text/plain", []byte("\r\n--"+m.Boundary+"\r\n"))
	if b := newBoundary([]BodyPart{clash}); b == m.Boundary {
		t.Errorf("boundary %q appears in a body", b)
	}
}

func TestByContentID(t *testing.T) {
	inner := NewMultipart(NewBodyPart("application/sdp", []byte(mcpttSDP)),
		NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte(mcpttInfo)))

	section := NewBodyPart(inner.ContentType(), inner.Bytes())
	section.SetContentID("bodies@ims-op.net")

	lists := NewBodyPart("application/resource-lists+xml", []byte("<resource-lists/>"))
	lists.SetContentID("cn35t8jf02@ims-op.net")

	m := NewMultipart(lists, section)

	for _, id := range []string{"cid:cn35t8jf02@ims-op.net", "CID:cn35t8jf02%40ims-op.net", "<cn35t8jf02@ims-op.net>", "cn35t8jf02@ims-op.net"} {
		p, ok := m.ByContentID(id)
		if !ok || p.MediaType() != "application/resource-lists+xml" {
			t.Errorf("ByContentID(%q) = %s, %v", id, p.MediaType(), ok)
		}
	}

	// TS 24.379 §17.2.2.2.1: a cid URL may refer to a MIME body section holding several bodies.
	p, ok := m.ByContentID("cid:bodies@ims-op.net")
	if !ok || p.MediaType() != "multipart/mixed" {
		t.Fatalf("body section: %s, %v", p.MediaType(), ok)
	}

	nested, err := p.Multipart()
	if err != nil || len(nested.Parts) != 2 {
		t.Fatalf("body section: %v", err)
	}

	if sdp, ok := m.Find("Application/SDP"); !ok || string(sdp.Body) != mcpttSDP {
		t.Errorf("Find in a nested body: %q, %v", sdp.Body, ok)
	}

	for _, id := range []string{"", "cid:", "cid:%zz", "cid:unknown@ims-op.net", "<>"} {
		if p, ok := m.ByContentID(id); ok {
			t.Errorf("ByContentID(%q) = %s", id, p.MediaType())
		}
	}
}

func TestBodyPartDisposition(t *testing.T) {
	for _, tc := range []struct {
		contentType, disposition string
		want                     string
		optional                 bool
	}{
		{"application/sdp", "", DispositionSession, false},
		{"application/vnd.3gpp.mcptt-info+xml", "", DispositionRender, false},
		{"application/sdp", "Render", DispositionRender, false},
		{"application/vnd.3gpp.mcptt-floor-request+xml", "Info-Package", DispositionInfoPackage, false},
		{"application/vnd.3gpp.mcptt-info+xml", "render;handling=optional", DispositionRender, true},
		{"application/vnd.3gpp.mcptt-info+xml", "render;handling=required", DispositionRender, false},
	} {
		p := NewBodyPart(tc.contentType, nil)
		if tc.disposition != "" {
			p.Header.Add("Content-Disposition", tc.disposition)
		}

		d, _, err := p.Disposition()
		if err != nil || d != tc.want || p.Optional() != tc.optional {
			t.Errorf("%s %q: %q, optional %v, %v; want %q, %v", tc.contentType, tc.disposition, d, p.Optional(), err, tc.want, tc.optional)
		}
	}

	bad := NewBodyPart("text/plain", nil)
	bad.Header.Add("Content-Disposition", "<render>")

	if _, _, err := bad.Disposition(); err == nil || bad.Optional() {
		t.Error("malformed Content-Disposition accepted")
	}

	if got := (BodyPart{}).MediaType(); got != "text/plain" {
		t.Errorf("default media type %q (RFC 2045 §5.2)", got)
	}
}

func TestEnvelopeBodies(t *testing.T) {
	var e Envelope

	e.Header.Add("Call-ID", "a")
	e.Header.Add("Content-Disposition", "render")
	e.Header.Add("Content-ID", "<stale@host>")
	e.SetBody("application/sdp", []byte(mcpttSDP))

	if e.Header.Has("Content-Disposition") || e.Header.Has("Content-ID") || !e.Header.Has("Call-ID") {
		t.Errorf("SetBody kept the Content-* fields of the old body: %v", e.Header)
	}

	bodies, err := e.Bodies()
	if err != nil || len(bodies) != 1 || bodies[0].MediaType() != "application/sdp" {
		t.Fatalf("single body: %v, %v", bodies, err)
	}

	info := NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte(mcpttInfo))
	info.SetContentID("info@host")
	info.Header.Add("Content-Disposition", "render;handling=optional")

	e.SetBodies(info, NewBodyPart("application/sdp", []byte(mcpttSDP)))

	mt, _, _ := ParseMediaType(e.Header.ContentType())
	if mt != "multipart/mixed" || e.Header.Get("Content-Length") != strconv.Itoa(len(e.Body)) {
		t.Fatalf("Content-Type %q, Content-Length %q for %d octets", e.Header.ContentType(), e.Header.Get("Content-Length"), len(e.Body))
	}

	if e.Header.Has("Content-ID") || e.Header.Has("Content-Disposition") {
		t.Errorf("part fields leaked into the message: %v", e.Header)
	}

	bodies, err = e.Bodies()
	if err != nil || len(bodies) != 2 || bodies[0].MediaType() != "application/sdp" || bodies[1].ContentID() != "info@host" {
		t.Fatalf("multipart bodies: %v, %v", bodies, err)
	}

	sdp, ok, err := e.FindBody("application/sdp")
	if err != nil || !ok || string(sdp.Body) != mcpttSDP {
		t.Errorf("FindBody(sdp) = %q, %v, %v", sdp.Body, ok, err)
	}

	if _, ok, err := e.FindBody("application/pidf+xml"); ok || err != nil {
		t.Errorf("FindBody(pidf) = %v, %v", ok, err)
	}

	e.SetBodies(info)

	if e.Header.ContentType() != "application/vnd.3gpp.mcptt-info+xml" || e.Header.Get("Content-ID") != "<info@host>" ||
		e.Header.Get("Content-Disposition") != "render;handling=optional" || string(e.Body) != mcpttInfo {
		t.Errorf("single part: %v %q", e.Header, e.Body)
	}

	if _, ok, _ := e.FindBody("application/sdp"); ok {
		t.Error("FindBody(sdp) found the mcptt-info body")
	}

	e.SetBodies()

	if e.Body != nil || e.Header.Has("Content-Type") || e.Header.Has("Content-ID") || e.Header.Get("Content-Length") != "0" {
		t.Errorf("no body: %v %q", e.Header, e.Body)
	}

	if bodies, err := e.Bodies(); bodies != nil || err != nil {
		t.Errorf("Bodies() of no body = %v, %v", bodies, err)
	}

	e.SetBody("multipart/mixed;boundary=b", []byte("garbage"))

	if _, err := e.Bodies(); err == nil {
		t.Error("Bodies() accepted a malformed multipart body")
	}

	if _, _, err := e.FindBody("application/sdp"); err == nil {
		t.Error("FindBody() accepted a malformed multipart body")
	}
}

func TestMultipartMessageRoundTrip(t *testing.T) {
	req := NewRequest("INVITE", URI{Scheme: "sip", Host: "pf-a1.ims-op.net"})
	req.Header.Add("Call-ID", "a")
	req.SetBodies(NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte(mcpttInfo)),
		NewBodyPart("application/sdp", []byte(mcpttSDP)))

	m, err := Parse(req.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	sdp, ok, err := m.Env().FindBody("application/sdp")
	if err != nil || !ok || string(sdp.Body) != mcpttSDP {
		t.Errorf("SDP after a round trip: %q, %v, %v", sdp.Body, ok, err)
	}
}
