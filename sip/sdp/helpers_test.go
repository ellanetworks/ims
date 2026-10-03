package sdp

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestEffectiveDirection(t *testing.T) {
	const head = "v=0\r\nc=IN IP4 10.0.0.1\r\n"

	for _, tc := range []struct {
		name          string
		offer, answer string
		want          Direction
		ok            bool
	}{
		{"plain", "m=audio 1 RTP/AVP 0\r\n", "m=audio 2 RTP/AVP 0\r\n", SendRecv, true},
		{"hold", "m=audio 1 RTP/AVP 0\r\na=sendonly\r\n", "m=audio 2 RTP/AVP 0\r\na=recvonly\r\n", RecvOnly, true},
		{"session-level inactive", "a=inactive\r\nm=audio 1 RTP/AVP 0\r\n", "m=audio 2 RTP/AVP 0\r\n", Inactive, true},
		{"offer port 0", "m=audio 0 RTP/AVP 0\r\n", "m=audio 2 RTP/AVP 0\r\n", "", false},
		{"answer port 0", "m=audio 1 RTP/AVP 0\r\n", "m=audio 0 RTP/AVP 0\r\n", "", false},
		{"answer missing media", "m=audio 1 RTP/AVP 0\r\n", "", "", false},
		{"offer c=0.0.0.0", "m=audio 1 RTP/AVP 0\r\nc=IN IP4 0.0.0.0\r\n", "m=audio 2 RTP/AVP 0\r\n", RecvOnly, true},
		{"answer c=0.0.0.0", "m=audio 1 RTP/AVP 0\r\n", "m=audio 2 RTP/AVP 0\r\nc=IN IP4 0.0.0.0\r\n", SendOnly, true},
	} {
		offer := mustParse(t, head+tc.offer)
		answer := mustParse(t, head+tc.answer)

		got, ok := EffectiveDirection(offer, answer, 0)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: EffectiveDirection = %q, %v, want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}

	offer := mustParse(t, head+"m=audio 1 RTP/AVP 0\r\na=sendonly\r\n")
	if got, ok := EffectiveDirection(offer, nil, 0); got != RecvOnly || !ok {
		t.Errorf("no answer: %q, %v", got, ok)
	}

	if _, ok := EffectiveDirection(offer, nil, 1); ok {
		t.Error("index out of range: ok")
	}
}

func TestEndpoints(t *testing.T) {
	s := mustParse(t, "v=0\r\nc=IN IP6 2001:db8::1\r\n"+
		"m=audio 30634 RTP/AVP 0\r\n"+
		"m=audio 49170 RTP/AVP 0\r\na=rtcp:53020\r\n"+
		"m=audio 49172 RTP/AVP 0\r\na=rtcp:53022 IN IP6 2001:db8::2\r\n"+
		"m=audio 0 RTP/AVP 0\r\n"+
		"m=audio 65535 RTP/AVP 0\r\n"+
		"m=audio 9 RTP/AVP 0\r\nc=IN IP4 host.example.com\r\n"+
		"m=audio 10 RTP/AVP 0\r\na=rtcp:11 IN IP4 host.example.com\r\n")

	for _, tc := range []struct {
		i         int
		rtp, rtcp string
	}{
		{0, "[2001:db8::1]:30634", "[2001:db8::1]:30635"},
		{1, "[2001:db8::1]:49170", "[2001:db8::1]:53020"},
		{2, "[2001:db8::1]:49172", "[2001:db8::2]:53022"},
		{3, "", ""},
		{4, "[2001:db8::1]:65535", ""},
		{5, "", ""},
		{6, "[2001:db8::1]:10", ""},
	} {
		got := ""
		if e, err := s.RTPEndpoint(tc.i); err == nil {
			got = e.String()
		}

		if got != tc.rtp {
			t.Errorf("RTPEndpoint(%d) = %q, want %q", tc.i, got, tc.rtp)
		}

		got = ""
		if e, err := s.RTCPEndpoint(tc.i); err == nil {
			got = e.String()
		}

		if got != tc.rtcp {
			t.Errorf("RTCPEndpoint(%d) = %q, want %q", tc.i, got, tc.rtcp)
		}
	}

	if e, _ := s.RTPEndpoint(0); e.Addr != netip.MustParseAddr("2001:db8::1") {
		t.Errorf("Addr = %v", e.Addr)
	}
}

func TestRTCPMuxed(t *testing.T) {
	mux := mustParse(t, "v=0\r\nm=audio 1 RTP/AVP 0\r\na=rtcp-mux\r\n")
	plain := mustParse(t, "v=0\r\nm=audio 1 RTP/AVP 0\r\n")

	if !RTCPMuxed(mux, mux, 0) || RTCPMuxed(mux, plain, 0) || RTCPMuxed(plain, mux, 0) || RTCPMuxed(mux, nil, 0) || RTCPMuxed(mux, mux, 1) {
		t.Error("RTCPMuxed")
	}
}

func TestCodecs(t *testing.T) {
	s := mustParse(t, "v=0\r\n"+
		"m=audio 1 RTP/AVP 116 0 8 111 99 18\r\n"+
		"a=rtpmap:116 AMR-WB/16000/1\r\n"+
		"a=rtpmap:111 telephone-event/16000\r\n"+
		"a=rtpmap:0 PCMU/8000\r\n"+
		"m=application 9 UDP/BFCP *\r\n"+
		"m=audio 1 RTP/SAVPF 96\r\na=rtpmap:96 amr-wb/16000/1\r\n")

	codecs, err := s.Media[0].Codecs()
	if err != nil {
		t.Fatal(err)
	}

	want := []RTPMap{
		{116, "AMR-WB", 16000, "1"},
		{0, "PCMU", 8000, ""},
		{8, "PCMA", 8000, "1"},
		{111, "telephone-event", 16000, ""},
		{18, "G729", 8000, "1"},
	}
	if !slices.Equal(codecs, want) {
		t.Errorf("Codecs() = %+v, want %+v", codecs, want)
	}

	if pts := s.Media[0].PayloadTypes("amr-wb"); !slices.Equal(pts, []uint8{116}) {
		t.Errorf("PayloadTypes(amr-wb) = %v", pts)
	}

	if pts := s.Media[0].PayloadTypes("TELEPHONE-EVENT"); !slices.Equal(pts, []uint8{111}) {
		t.Errorf("PayloadTypes(TELEPHONE-EVENT) = %v", pts)
	}

	if pts := s.Media[2].PayloadTypes("AMR-WB"); !slices.Equal(pts, []uint8{96}) {
		t.Errorf("PayloadTypes on SAVPF = %v", pts)
	}

	if _, err := s.Media[1].Codecs(); err == nil {
		t.Error("Codecs() on non-RTP media: no error")
	}

	s = mustParse(t, "v=0\r\nm=audio 1 RTP/AVP 97 116\r\na=rtpmap:97 bogus\r\na=rtpmap:116 AMR-WB/16000/1\r\n")

	codecs, err = s.Media[0].Codecs()
	if err == nil || len(codecs) != 1 || codecs[0].Payload != 116 {
		t.Errorf("Codecs() with a bad rtpmap = %+v, %v", codecs, err)
	}

	if pts := s.Media[0].PayloadTypes("AMR-WB"); !slices.Equal(pts, []uint8{116}) {
		t.Errorf("PayloadTypes with a bad rtpmap = %v", pts)
	}
}

func TestCodecData(t *testing.T) {
	s := mustParse(t, offer)

	got := string(s.Media[0].CodecData(Uplink, CodecOffer))

	want := strings.Join([]string{
		"uplink",
		"offer",
		"m=audio 1234 RTP/AVP 116 111",
		"a=rtpmap:116 AMR-WB/16000/1",
		"a=fmtp:116 mode-change-capability=2;max-red=220",
		"a=rtpmap:111 telephone-event/16000",
		"a=fmtp:111 0-15",
		"a=ptime:20",
		"a=maxptime:240",
		"a=curr:qos local none",
		"a=curr:qos remote none",
		"a=des:qos mandatory local sendrecv",
		"a=des:qos mandatory remote sendrecv",
		"a=conf:qos remote sendrecv",
	}, "\n")
	if got != want {
		t.Errorf("CodecData:\n%s\nwant\n%s", got, want)
	}

	s = mustParse(t, "v=0\nm=video 1 RTP/AVP 99\nb=TIAS:64000\nb=AS:80\na=bw-info:99 x\na=inactive\n")
	if got := string(s.Media[0].CodecData(Downlink, CodecAnswer)); got != "downlink\nanswer\nm=video 1 RTP/AVP 99\nb=TIAS:64000" {
		t.Errorf("CodecData = %q", got)
	}

	if l := s.Media[0].Lines[0].String(); l != "m=video 1 RTP/AVP 99" {
		t.Errorf("Line.String() = %q", l)
	}
}

func TestPreconditionInvert(t *testing.T) {
	for _, tc := range []struct{ in, want Precondition }{
		{Precondition{Current, QoS, "", StatusLocal, QoSSend}, Precondition{Current, QoS, "", StatusRemote, QoSRecv}},
		{Precondition{Desired, QoS, StrengthMandatory, StatusRemote, QoSSendRecv}, Precondition{Desired, QoS, StrengthMandatory, StatusLocal, QoSSendRecv}},
		{Precondition{Confirmed, QoS, "", StatusE2E, QoSRecv}, Precondition{Confirmed, QoS, "", StatusE2E, QoSSend}},
		{Precondition{Current, QoS, "", StatusLocal, QoSNone}, Precondition{Current, QoS, "", StatusRemote, QoSNone}},
	} {
		if got := tc.in.Invert(); got != tc.want {
			t.Errorf("%+v.Invert() = %+v, want %+v", tc.in, got, tc.want)
		}

		if got := tc.in.Invert().Invert(); got != tc.in {
			t.Errorf("double Invert of %+v = %+v", tc.in, got)
		}
	}
}

func TestPreconditionsMet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines string
		want  bool
	}{
		{"none reserved", "a=curr:qos local none\na=curr:qos remote none\na=des:qos mandatory local sendrecv\na=des:qos mandatory remote sendrecv\n", false},
		{"local only", "a=curr:qos local sendrecv\na=curr:qos remote none\na=des:qos mandatory local sendrecv\na=des:qos mandatory remote sendrecv\n", false},
		{"both", "a=curr:qos local sendrecv\na=curr:qos remote sendrecv\na=des:qos mandatory local sendrecv\na=des:qos mandatory remote sendrecv\n", true},
		{"remote optional", "a=curr:qos local sendrecv\na=curr:qos remote none\na=des:qos mandatory local sendrecv\na=des:qos optional remote sendrecv\n", true},
		{"half reserved", "a=curr:qos local send\na=des:qos mandatory local sendrecv\n", false},
		{"split mandatory send only", "a=curr:qos local send\na=des:qos mandatory local send\na=des:qos optional local recv\n", true},
		{"e2e", "a=curr:qos e2e sendrecv\na=des:qos mandatory e2e sendrecv\n", true},
		{"segmented curr does not meet e2e", "a=curr:qos local sendrecv\na=curr:qos remote sendrecv\na=des:qos mandatory e2e sendrecv\n", false},
		{"failure", "a=curr:qos local sendrecv\na=des:qos failure local sendrecv\n", false},
		{"no preconditions", "", true},
	} {
		s := mustParse(t, "v=0\nm=audio 1 RTP/AVP 0\n"+tc.lines)

		ps, err := s.Media[0].Preconditions()
		if err != nil {
			t.Fatal(err)
		}

		if got := PreconditionsMet(ps); got != tc.want {
			t.Errorf("%s: PreconditionsMet = %v, want %v", tc.name, got, tc.want)
		}
	}
}
