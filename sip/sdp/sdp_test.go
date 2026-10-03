package sdp

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"
)

const offer = "v=0\r\n" +
	"o=SAMSUNG-IMS-UE 925403981 0 IN IP4 192.168.101.2\r\n" +
	"s=SS VOIP\r\n" +
	"c=IN IP4 192.168.101.2\r\n" +
	"t=0 0\r\n" +
	"m=audio 1234 RTP/AVP 116 111\r\n" +
	"b=AS:41\r\n" +
	"b=RS:512\r\n" +
	"b=RR:1537\r\n" +
	"a=rtpmap:116 AMR-WB/16000/1\r\n" +
	"a=fmtp:116 mode-change-capability=2;max-red=220\r\n" +
	"a=rtpmap:111 telephone-event/16000\r\n" +
	"a=fmtp:111 0-15\r\n" +
	"a=ptime:20\r\n" +
	"a=maxptime:240\r\n" +
	"a=sendrecv\r\n" +
	"a=curr:qos local none\r\n" +
	"a=curr:qos remote none\r\n" +
	"a=des:qos mandatory local sendrecv\r\n" +
	"a=des:qos mandatory remote sendrecv\r\n" +
	"a=conf:qos remote sendrecv\r\n"

func mustParse(t *testing.T, s string) *Session {
	t.Helper()

	sess, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}

	return sess
}

func TestParseRoundTrip(t *testing.T) {
	for _, in := range []string{
		offer,
		"v=0\no=- 1 1 IN IP6 ::1\ns=-\nt=0 0\nm=audio 9 RTP/AVP 0\n",
		"v=0\r\ns=-\nt=0 0\r\nm=audio 9 RTP/AVP 0",
		"v=0\r\ns=-\r\nt=0 0\r\nm=audio 9 RTP/AVP 0\r\n\r\n",
		"v=0\r\ns=-\r\nt=0 0\r\n\n\r\n",
		"v=0\r\nx=unknown line\r\na=tcap:1 RTP/AVPF\r\nm=audio 0 RTP/AVP\r\nb=AS:0\r\nm=video 3227/2 RTP/AVP 31\r\n",
	} {
		sess := mustParse(t, in)
		if got := sess.String(); got != in {
			t.Errorf("round trip:\n%q\n%q", in, got)
		}

		if got := sess.Clone().String(); got != in {
			t.Errorf("clone round trip:\n%q\n%q", in, got)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"\r\n",
		"o=- 1 1 IN IP4 1.2.3.4\r\n",
		"v=0\r\n\r\ns=-\r\n",
		"v=0\r\nfoo\r\n",
		"v=0\r\n1=x\r\n",
		"v=0\r\n=x\r\n",
		"v=0\r\ns=a\x00b\r\n",
		"v=0\r\ns=a\rb\r\n",
		"v=0\r\nm=audio x RTP/AVP 0\r\n",
		"v=0\r\nm=audio 70000 RTP/AVP 0\r\n",
		"v=0\r\nm=audio 9/0 RTP/AVP 0\r\n",
		"v=0\r\nm=audio 9\r\n",
		"v=0\r\nm=\"a\" 9 RTP/AVP 0\r\n",
	} {
		if s, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) = %q, want error", in, s)
		}
	}
}

func TestSessionQueries(t *testing.T) {
	s := mustParse(t, offer)

	o, err := s.Origin()
	if err != nil {
		t.Fatal(err)
	}

	if o.Username != "SAMSUNG-IMS-UE" || o.SessionID != "925403981" || o.SessionVersion != "0" || o.Address != "192.168.101.2" {
		t.Errorf("Origin() = %+v", o)
	}

	if len(s.Media) != 1 {
		t.Fatalf("got %d media, want 1", len(s.Media))
	}

	m := s.Media[0]

	d, err := m.Desc()
	if err != nil {
		t.Fatal(err)
	}

	if d.Type != Audio || d.Port != 1234 || d.Proto != "RTP/AVP" || !slices.Equal(d.Formats, []string{"116", "111"}) {
		t.Errorf("Desc() = %+v", d)
	}

	if bw, ok := m.Bandwidth(BandwidthAS); !ok || bw != 41 {
		t.Errorf("Bandwidth(AS) = %d, %v", bw, ok)
	}

	if bw, ok := m.Bandwidth(BandwidthRR); !ok || bw != 1537 {
		t.Errorf("Bandwidth(RR) = %d, %v", bw, ok)
	}

	if _, ok := m.Bandwidth(BandwidthTIAS); ok {
		t.Error("Bandwidth(TIAS) present")
	}

	maps, err := m.RTPMaps()
	if err != nil {
		t.Fatal(err)
	}

	want := []RTPMap{{116, "AMR-WB", 16000, "1"}, {111, "telephone-event", 16000, ""}}
	if !slices.Equal(maps, want) {
		t.Errorf("RTPMaps() = %+v, want %+v", maps, want)
	}

	if f, ok := m.Fmtp("116"); !ok || f != "mode-change-capability=2;max-red=220" {
		t.Errorf("Fmtp(116) = %q, %v", f, ok)
	}

	if _, ok := m.Fmtp("11"); ok {
		t.Error("Fmtp(11) matched fmtp:111")
	}

	if p, ok := m.Ptime(); !ok || p != 20*time.Millisecond {
		t.Errorf("Ptime() = %v, %v", p, ok)
	}

	if p, ok := m.MaxPtime(); !ok || p != 240*time.Millisecond {
		t.Errorf("MaxPtime() = %v, %v", p, ok)
	}

	if d := s.MediaDirection(0); d != SendRecv {
		t.Errorf("MediaDirection(0) = %q", d)
	}

	c, err := s.MediaConnection(0)
	if err != nil {
		t.Fatal(err)
	}

	if a, ok := c.Addr(); !ok || a != netip.MustParseAddr("192.168.101.2") {
		t.Errorf("MediaConnection(0).Addr() = %v, %v", a, ok)
	}

	if ats, err := s.AddrTypes(); err != nil || !slices.Equal(ats, []string{IP4}) {
		t.Errorf("AddrTypes() = %v, %v", ats, err)
	}

	if _, ok, err := m.RTCP(); ok || err != nil {
		t.Errorf("RTCP() = %v, %v", ok, err)
	}

	if m.RTCPMux() {
		t.Error("RTCPMux() = true")
	}

	pre, err := m.Preconditions()
	if err != nil {
		t.Fatal(err)
	}

	wantPre := []Precondition{
		{Current, QoS, "", StatusLocal, QoSNone},
		{Current, QoS, "", StatusRemote, QoSNone},
		{Desired, QoS, StrengthMandatory, StatusLocal, QoSSendRecv},
		{Desired, QoS, StrengthMandatory, StatusRemote, QoSSendRecv},
		{Confirmed, QoS, "", StatusRemote, QoSSendRecv},
	}
	if !slices.Equal(pre, wantPre) {
		t.Errorf("Preconditions() = %+v, want %+v", pre, wantPre)
	}
}

func TestDirection(t *testing.T) {
	s := mustParse(t, "v=0\r\na=inactive\r\nm=audio 9 RTP/AVP 0\r\nm=audio 10 RTP/AVP 0\r\na=recvonly\r\n")

	if d := s.MediaDirection(0); d != Inactive {
		t.Errorf("session-level direction: got %q, want inactive", d)
	}

	if d := s.MediaDirection(1); d != RecvOnly {
		t.Errorf("media-level direction: got %q, want recvonly", d)
	}

	s.Media[1].SetDirection(SendOnly)
	s.Media[0].SetDirection(SendRecv)
	s.SetDirection(SendRecv)

	want := "v=0\r\na=sendrecv\r\nm=audio 9 RTP/AVP 0\r\na=sendrecv\r\nm=audio 10 RTP/AVP 0\r\na=sendonly\r\n"
	if got := s.String(); got != want {
		t.Errorf("SetDirection:\n%q\n%q", got, want)
	}

	for _, tc := range []struct{ offer, answer, want Direction }{
		{SendRecv, SendRecv, SendRecv},
		{SendRecv, RecvOnly, RecvOnly},
		{SendOnly, RecvOnly, RecvOnly},
		{RecvOnly, SendOnly, SendOnly},
		{SendRecv, Inactive, Inactive},
		{Inactive, SendRecv, Inactive},
		{Inactive, Inactive, Inactive},
	} {
		if got := Effective(tc.offer, tc.answer); got != tc.want {
			t.Errorf("Effective(%s, %s) = %s, want %s", tc.offer, tc.answer, got, tc.want)
		}
	}

	for d, r := range map[Direction]Direction{SendRecv: SendRecv, SendOnly: RecvOnly, RecvOnly: SendOnly, Inactive: Inactive} {
		if got := d.Reverse(); got != r {
			t.Errorf("%s.Reverse() = %s, want %s", d, got, r)
		}
	}

	if !SendOnly.Sends() || SendOnly.Receives() || !RecvOnly.Receives() || Inactive.Sends() || !SendRecv.Receives() {
		t.Error("Sends/Receives")
	}
}

func TestMediaConnectionFallback(t *testing.T) {
	s := mustParse(t, "v=0\r\nc=IN IP4 10.0.0.1\r\nm=audio 9 RTP/AVP 0\r\nm=audio 10 RTP/AVP 0\r\nc=IN IP6 2001:db8::1\r\nm=video 0 RTP/AVP 31\r\nc=IN IP6 2001:db8::2\r\n")

	for i, want := range []string{"10.0.0.1", "2001:db8::1", "2001:db8::2"} {
		c, err := s.MediaConnection(i)
		if err != nil {
			t.Fatal(err)
		}

		if c.Address != want {
			t.Errorf("MediaConnection(%d) = %+v, want %s", i, c, want)
		}
	}

	if ats, err := s.AddrTypes(); err != nil || !slices.Equal(ats, []string{IP4, IP6}) {
		t.Errorf("AddrTypes() = %v, %v", ats, err)
	}

	s = mustParse(t, "v=0\r\nm=audio 9 RTP/AVP 0\r\n")
	if _, err := s.MediaConnection(0); !errors.Is(err, ErrMissing) {
		t.Errorf("MediaConnection without c=: %v", err)
	}

	if _, err := s.AddrTypes(); err == nil {
		t.Error("AddrTypes without c=: no error")
	}
}

func TestConnectionAddr(t *testing.T) {
	for _, tc := range []struct {
		in   string
		addr string
	}{
		{"IN IP4 192.0.2.1", "192.0.2.1"},
		{"IN IP4 224.2.36.42/127", "224.2.36.42"},
		{"IN IP6 2001:db8::1", "2001:db8::1"},
		{"IN IP6 192.0.2.1", ""},
		{"IN IP4 2001:db8::1", ""},
		{"IN IP6 ::ffff:192.0.2.1", ""},
		{"IN IP4 aaa.bbb.ccc.ddd", ""},
		{"IN IP6 fe80::1%eth0", ""},
	} {
		c, err := ParseConnection(tc.in)
		if err != nil {
			t.Fatalf("ParseConnection(%q): %v", tc.in, err)
		}

		got := ""
		if a, ok := c.Addr(); ok {
			got = a.String()
		}

		if got != tc.addr {
			t.Errorf("%q: Addr() = %q, want %q", tc.in, got, tc.addr)
		}
	}

	if c := NewConnection(netip.MustParseAddr("::ffff:10.1.2.3")); c.String() != "IN IP4 10.1.2.3" {
		t.Errorf("NewConnection(v4-mapped) = %s", c)
	}

	if c := NewConnection(netip.MustParseAddr("2001:db8::5")); c.String() != "IN IP6 2001:db8::5" {
		t.Errorf("NewConnection(v6) = %s", c)
	}

	for _, in := range []string{"", "IN IP4", "IN IP4 1.2.3.4 x"} {
		if _, err := ParseConnection(in); err == nil {
			t.Errorf("ParseConnection(%q): no error", in)
		}
	}
}

func TestOrigin(t *testing.T) {
	const in = "- 123 456 IN IP6 2001:db8::1"

	o, err := ParseOrigin(in)
	if err != nil {
		t.Fatal(err)
	}

	if o.String() != in {
		t.Errorf("String() = %q", o)
	}

	for _, bad := range []string{"", "- 1 1 IN IP4", "- x 1 IN IP4 1.2.3.4", "Example_SERVER2 34135268010 IN IP4 server2.example.com"} {
		if _, err := ParseOrigin(bad); err == nil {
			t.Errorf("ParseOrigin(%q): no error", bad)
		}
	}

	s := mustParse(t, "v=0\r\ns=-\r\n")
	if _, err := s.Origin(); !errors.Is(err, ErrMissing) {
		t.Errorf("Origin() without o=: %v", err)
	}

	o.SessionVersion = "457"
	s.SetOrigin(o)

	if got := s.String(); got != "v=0\r\no=- 123 457 IN IP6 2001:db8::1\r\ns=-\r\n" {
		t.Errorf("SetOrigin: %q", got)
	}
}

func TestMediaDesc(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want MediaDesc
	}{
		{"audio 49152 RTP/AVP 97 98", MediaDesc{Audio, 49152, 0, "RTP/AVP", []string{"97", "98"}}},
		{"video 0 RTP/AVP 114 113", MediaDesc{Video, 0, 0, "RTP/AVP", []string{"114", "113"}}},
		{"audio 49170/2 RTP/AVP 31", MediaDesc{Audio, 49170, 2, "RTP/AVP", []string{"31"}}},
		{"application 9 UDP/TLS/RTP/SAVPF", MediaDesc{"application", 9, 0, "UDP/TLS/RTP/SAVPF", nil}},
	} {
		d, err := ParseMediaDesc(tc.in)
		if err != nil {
			t.Fatalf("ParseMediaDesc(%q): %v", tc.in, err)
		}

		if d.Type != tc.want.Type || d.Port != tc.want.Port || d.NumPorts != tc.want.NumPorts || d.Proto != tc.want.Proto || !slices.Equal(d.Formats, tc.want.Formats) {
			t.Errorf("ParseMediaDesc(%q) = %+v, want %+v", tc.in, d, tc.want)
		}

		if d.String() != tc.in {
			t.Errorf("String() = %q, want %q", d, tc.in)
		}
	}
}

func TestDisable(t *testing.T) {
	s := mustParse(t, "v=0\r\nm=audio 49170/2 RTP/AVP 0 8\r\nm=video 3227 RTP/AVP 31\r\nb=AS:300\r\n")
	s.Media[1].Disable()
	s.Media[0].Disable()

	want := "v=0\r\nm=audio 0 RTP/AVP 0 8\r\nm=video 0 RTP/AVP 31\r\nb=AS:300\r\n"
	if got := s.String(); got != want {
		t.Errorf("Disable:\n%q\n%q", got, want)
	}

	if s.Media[1].Port() != 0 || s.Media[1].Type() != Video {
		t.Errorf("Port/Type after Disable: %d %s", s.Media[1].Port(), s.Media[1].Type())
	}

	if ats, err := s.AddrTypes(); err != nil || len(ats) != 0 {
		t.Errorf("AddrTypes() with all media disabled = %v, %v", ats, err)
	}
}

func TestLinesEdit(t *testing.T) {
	s := mustParse(t, "v=0\no=- 1 1 IN IP4 10.0.0.1\ns=-\nt=0 0\nm=audio 9 RTP/AVP 0\na=ptime:20\n")

	s.SetConnection(Connection{NetType: NetTypeIN, AddrType: IP4, Address: "10.0.0.2"})
	s.Add('b', "AS:64")
	s.AddAttr("group", "BUNDLE 0")

	m := s.Media[0]
	m.SetBandwidth(BandwidthAS, 38)
	m.SetBandwidth(BandwidthAS, 41)
	m.AddAttr("rtcp-mux", "")
	m.SetAttr("ptime", "40")
	m.SetPrecondition(Precondition{Current, QoS, "", StatusLocal, QoSNone})
	m.SetPrecondition(Precondition{Current, QoS, "", StatusLocal, QoSSendRecv})
	m.SetPrecondition(Precondition{Desired, QoS, StrengthMandatory, StatusLocal, QoSSendRecv})

	want := "v=0\no=- 1 1 IN IP4 10.0.0.1\ns=-\nc=IN IP4 10.0.0.2\nb=AS:64\nt=0 0\na=group:BUNDLE 0\n" +
		"m=audio 9 RTP/AVP 0\nb=AS:41\na=ptime:40\na=rtcp-mux\na=curr:qos local sendrecv\na=des:qos mandatory local sendrecv\n"
	if got := s.String(); got != want {
		t.Errorf("edits:\n%q\n%q", got, want)
	}

	if !m.RTCPMux() {
		t.Error("RTCPMux() = false")
	}

	if n := m.DelAttr("rtcp-mux"); n != 1 || m.HasAttr("rtcp-mux") {
		t.Errorf("DelAttr = %d", n)
	}

	if n := s.Del('b'); n != 1 {
		t.Errorf("Del(b) = %d", n)
	}

	m.Add('i', "info")

	if m.Lines[1].Type != 'i' {
		t.Errorf("i= inserted at %v", m.Lines)
	}
}

func TestEditAfterBareLastLine(t *testing.T) {
	s := mustParse(t, "v=0\r\ns=-")
	s.AddAttr("sendrecv", "")

	if got := s.String(); got != "v=0\r\ns=-\r\na=sendrecv\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestSetValueSanitized(t *testing.T) {
	s := mustParse(t, "v=0\r\ns=-\r\n")
	s.Set('s', "a\r\nb")

	if got := s.String(); got != "v=0\r\ns=a  b\r\n" {
		t.Errorf("got %q", got)
	}

	if _, err := Parse(s.Bytes()); err != nil {
		t.Error(err)
	}
}

func TestBandwidth(t *testing.T) {
	for _, in := range []string{"AS:41", "TIAS:64000", "RS:0", "X-YZ:1"} {
		b, err := ParseBandwidth(in)
		if err != nil || b.String() != in {
			t.Errorf("ParseBandwidth(%q) = %+v, %v", in, b, err)
		}
	}

	for _, in := range []string{"", "AS", "AS:", ":1", "AS:x", "AS:-1", "AS:99999999999999999999"} {
		if _, err := ParseBandwidth(in); err == nil {
			t.Errorf("ParseBandwidth(%q): no error", in)
		}
	}

	s := mustParse(t, "v=0\r\nm=audio 9 RTP/AVP 0\r\nb=AS:x\r\n")
	if _, err := s.Media[0].Bandwidths(); err == nil {
		t.Error("Bandwidths(): no error")
	}
}

func TestTiming(t *testing.T) {
	tm, err := ParseTiming("0 0")
	if err != nil || tm != (Timing{}) || tm.String() != "0 0" {
		t.Errorf("ParseTiming = %+v, %v", tm, err)
	}

	tm, err = ParseTiming("3034423619 3042462419")
	if err != nil || tm.Start != 3034423619 || tm.String() != "3034423619 3042462419" {
		t.Errorf("ParseTiming = %+v, %v", tm, err)
	}

	for _, in := range []string{"", "0", "0 x", "0 0 0", "99999999999999999999 0"} {
		if _, err := ParseTiming(in); err == nil {
			t.Errorf("ParseTiming(%q): no error", in)
		}
	}
}

func TestRTPMap(t *testing.T) {
	for _, in := range []string{"116 AMR-WB/16000/1", "0 PCMU/8000", "111 telephone-event/16000", "97 EVS/16000/1"} {
		r, err := ParseRTPMap(in)
		if err != nil || r.String() != in {
			t.Errorf("ParseRTPMap(%q) = %+v, %v", in, r, err)
		}
	}

	for _, in := range []string{"", "116", "128 X/8000", "x X/8000", "96 X", "96 X/y", "96 /8000", "96 X/8000/", "-1 X/8000"} {
		if r, err := ParseRTPMap(in); err == nil {
			t.Errorf("ParseRTPMap(%q) = %+v, want error", in, r)
		}
	}
}

func TestRTCP(t *testing.T) {
	s := mustParse(t, "v=0\r\nm=audio 30634 RTP/AVP 0\r\na=rtcp:30635\r\nm=audio 49170 RTP/AVP 0\r\na=rtcp:53020 IN IP6 2001:db8::1\r\nm=audio 9 RTP/AVP 0\r\na=rtcp:x\r\n")

	r, ok, err := s.Media[0].RTCP()
	if err != nil || !ok || r.Port != 30635 || r.Connection != nil || r.String() != "30635" {
		t.Errorf("RTCP() = %+v, %v, %v", r, ok, err)
	}

	r, ok, err = s.Media[1].RTCP()
	if err != nil || !ok || r.Port != 53020 || r.Connection == nil || r.Connection.Address != "2001:db8::1" || r.String() != "53020 IN IP6 2001:db8::1" {
		t.Errorf("RTCP() = %+v, %v, %v", r, ok, err)
	}

	if _, ok, err := s.Media[2].RTCP(); ok || err == nil {
		t.Errorf("RTCP() on invalid value = %v, %v", ok, err)
	}
}

func TestPtime(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want time.Duration
	}{
		{"20", 20 * time.Millisecond},
		{"22.5", 22500 * time.Microsecond},
		{"0", 0},
		{"", 0},
		{"Inf", 0},
		{"NaN", 0},
		{"1e3", 0},
		{"0x10", 0},
		{"-20", 0},
		{"20.", 0},
	} {
		s := mustParse(t, "v=0\r\nm=audio 9 RTP/AVP 0\r\na=ptime:"+tc.v+"\r\n")

		got, ok := s.Media[0].Ptime()
		if got != tc.want || ok != (tc.want != 0) {
			t.Errorf("ptime:%s = %v, %v", tc.v, got, ok)
		}
	}
}

func TestPreconditionErrors(t *testing.T) {
	for _, tc := range []struct{ kind, value string }{
		{"curr", "qos local"},
		{"curr", "qos mandatory local none"},
		{"des", "qos local sendrecv"},
		{"des", "qos sometimes local sendrecv"},
		{"curr", "qos everywhere none"},
		{"conf", "qos remote both"},
		{"conf", "\"q\" remote send"},
		{"rtpmap", "qos local none"},
	} {
		if p, err := ParsePrecondition(tc.kind, tc.value); err == nil {
			t.Errorf("ParsePrecondition(%s, %q) = %+v, want error", tc.kind, tc.value, p)
		}
	}

	s := mustParse(t, "v=0\r\nm=audio 9 RTP/AVP 0\r\na=curr:qos e2e maybe\r\n")
	if _, err := s.Media[0].Preconditions(); err == nil {
		t.Error("Preconditions(): no error")
	}
}
