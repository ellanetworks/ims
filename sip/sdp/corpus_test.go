package sdp_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/corpus"
	"github.com/ellanetworks/ims/sip/sdp"
)

type body struct {
	name string
	raw  []byte
}

func corpusBodies(t testing.TB) []body {
	t.Helper()

	var out []body

	for _, fx := range corpus.Corpus() {
		m, err := sip.Parse(fx.Raw)
		if err != nil {
			continue
		}

		ct, _, _ := strings.Cut(m.Env().Header.ContentType(), ";")
		if strings.EqualFold(strings.TrimSpace(ct), sdp.ContentType) && len(m.Env().Body) > 0 {
			out = append(out, body{fx.Name, m.Env().Body})
		}
	}

	return out
}

func specBodies(t testing.TB) []body {
	t.Helper()

	paths, err := filepath.Glob("testdata/ts26114/*.sdp")
	if err != nil {
		t.Fatal(err)
	}

	var out []body

	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}

		out = append(out, body{p, raw})
	}

	return out
}

func checkBody(t *testing.T, raw []byte) *sdp.Session {
	t.Helper()

	s, err := sdp.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := s.Bytes(); !bytes.Equal(got, raw) {
		t.Fatalf("not lossless:\n%q\n%q", raw, got)
	}

	if _, err := s.Connection(); err != nil {
		t.Errorf("session c=: %v", err)
	}

	if _, err := s.Bandwidths(); err != nil {
		t.Error(err)
	}

	if len(s.Media) == 0 {
		t.Error("no media")
	}

	for i, m := range s.Media {
		if _, err := m.Desc(); err != nil {
			t.Errorf("media %d: %v", i, err)
		}

		if _, err := s.MediaConnection(i); err != nil {
			t.Errorf("media %d: %v", i, err)
		}

		if _, err := m.Bandwidths(); err != nil {
			t.Errorf("media %d: %v", i, err)
		}

		if _, err := m.RTPMaps(); err != nil {
			t.Errorf("media %d: %v", i, err)
		}

		if _, err := m.Preconditions(); err != nil {
			t.Errorf("media %d: %v", i, err)
		}

		if _, _, err := m.RTCP(); err != nil {
			t.Errorf("media %d: %v", i, err)
		}
	}

	return s
}

func TestCorpusSDP(t *testing.T) {
	bodies := corpusBodies(t)
	if len(bodies) < 50 {
		t.Fatalf("found %d SDP bodies in the corpus", len(bodies))
	}

	for _, b := range bodies {
		t.Run(b.name, func(t *testing.T) {
			s := checkBody(t, b.raw)

			if _, err := s.Origin(); err != nil {
				t.Error(err)
			}

			if _, err := s.AddrTypes(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestSpecSDP(t *testing.T) {
	bodies := specBodies(t)
	if len(bodies) == 0 {
		t.Fatal("no TS 26.114 examples")
	}

	for _, b := range bodies {
		t.Run(filepath.Base(b.name), func(t *testing.T) {
			s := checkBody(t, b.raw)

			_, err := s.Origin()
			if strings.Contains(b.name, "a8-1-answer") {
				if err == nil {
					t.Error("Origin() accepted the five-field o= line of Table A.8.1")
				}
			} else if err != nil {
				t.Error(err)
			}
		})
	}
}

func TestCorpusCall(t *testing.T) {
	var raw []byte

	for _, fx := range corpus.Corpus() {
		if fx.Name == "open5gs/ipsec_to_ipsec_call/013-183-INVITE.sip" {
			m, err := sip.Parse(fx.Raw)
			if err != nil {
				t.Fatal(err)
			}

			raw = m.Env().Body
		}
	}

	if raw == nil {
		t.Fatal("fixture not found")
	}

	s, err := sdp.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	m := s.Media[0]
	if m.Type() != sdp.Audio || m.Port() != 1234 {
		t.Errorf("m= %s %d", m.Type(), m.Port())
	}

	if s.MediaDirection(0) != sdp.SendRecv {
		t.Errorf("direction %s", s.MediaDirection(0))
	}

	pre, err := m.Preconditions()
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, p := range pre {
		got = append(got, p.Kind+":"+p.String())
	}

	want := []string{
		"curr:qos local none", "curr:qos remote none",
		"des:qos mandatory local sendrecv", "des:qos mandatory remote sendrecv",
		"conf:qos remote sendrecv",
	}
	if !slices.Equal(got, want) {
		t.Errorf("preconditions %q, want %q", got, want)
	}
}

func TestCorpusVideo(t *testing.T) {
	var video int

	for _, b := range append(corpusBodies(t), specBodies(t)...) {
		s, err := sdp.Parse(b.raw)
		if err != nil {
			t.Fatal(err)
		}

		for _, m := range s.Media {
			if m.Type() == sdp.Video {
				video++
			}
		}
	}

	if video == 0 {
		t.Error("no video media in the corpus")
	}
}

func FuzzSDP(f *testing.F) {
	for _, b := range append(corpusBodies(f), specBodies(f)...) {
		f.Add(b.raw)
	}

	f.Add([]byte("v=0\nm=audio 9 RTP/AVP 0\na=rtcp:9 IN IP4 1.2.3.4\na=ptime:22.5\na=curr:qos e2e send"))
	f.Add([]byte("v=0\r\n\r\n\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := sdp.Parse(data)
		if err != nil {
			return
		}

		if got := s.Bytes(); !bytes.Equal(got, data) {
			t.Fatalf("not lossless:\n%q\n%q", data, got)
		}

		roundTrip(t, s)

		c := s.Clone()
		for _, m := range c.Media {
			m.Disable()
			m.SetDirection(sdp.Inactive)
			m.SetBandwidth(sdp.BandwidthAS, 0)
		}

		c.AddAttr("x", "y")

		again, err := sdp.Parse(c.Bytes())
		if err != nil {
			t.Fatalf("edited %q does not parse: %v", c.Bytes(), err)
		}

		if !bytes.Equal(again.Bytes(), c.Bytes()) {
			t.Fatalf("edited session unstable:\n%q\n%q", c.Bytes(), again.Bytes())
		}

		for i, m := range again.Media {
			if m.Port() != 0 || again.MediaDirection(i) != sdp.Inactive {
				t.Fatalf("media %d: port %d direction %s after edits", i, m.Port(), again.MediaDirection(i))
			}
		}

		if !bytes.Equal(s.Bytes(), data) {
			t.Fatal("editing the clone changed the original")
		}
	})
}

func roundTrip(t *testing.T, s *sdp.Session) {
	t.Helper()

	if o, err := s.Origin(); err == nil {
		if again, err := sdp.ParseOrigin(o.String()); err != nil || again != o {
			t.Fatalf("origin %q does not round-trip: %v", o, err)
		}
	}

	for _, ls := range append([]sdp.Lines{s.Lines}, mediaLines(s)...) {
		if c, err := ls.Connection(); err == nil {
			if again, err := sdp.ParseConnection(c.String()); err != nil || again != c {
				t.Fatalf("connection %q does not round-trip: %v", c, err)
			}
		}

		if bs, err := ls.Bandwidths(); err == nil {
			for _, b := range bs {
				if again, err := sdp.ParseBandwidth(b.String()); err != nil || again != b {
					t.Fatalf("bandwidth %q does not round-trip: %v", b, err)
				}
			}
		}

		if ps, err := ls.Preconditions(); err == nil {
			for _, p := range ps {
				if again, err := sdp.ParsePrecondition(p.Kind, p.String()); err != nil || again != p {
					t.Fatalf("precondition %q does not round-trip: %v", p, err)
				}
			}
		}
	}

	for i, m := range s.Media {
		d, err := m.Desc()
		if err != nil {
			t.Fatalf("media %d: parsed m= line fails: %v", i, err)
		}

		if again, err := sdp.ParseMediaDesc(d.String()); err != nil || again.String() != d.String() {
			t.Fatalf("m=%q does not round-trip: %v", d, err)
		}

		if rs, err := m.RTPMaps(); err == nil {
			for _, r := range rs {
				if again, err := sdp.ParseRTPMap(r.String()); err != nil || again != r {
					t.Fatalf("rtpmap %q does not round-trip: %v", r, err)
				}
			}
		}

		if r, ok, _ := m.RTCP(); ok {
			if again, err := sdp.ParseRTCP(r.String()); err != nil || again.String() != r.String() {
				t.Fatalf("rtcp %q does not round-trip: %v", r, err)
			}
		}

		_, _ = m.Ptime()
		_, _ = s.MediaConnection(i)
		_ = s.MediaDirection(i)
		_, _ = sdp.EffectiveDirection(s, s, i)
		_, _ = s.RTPEndpoint(i)
		_, _ = s.RTCPEndpoint(i)
		_ = m.PayloadTypes("AMR-WB")
		_ = sdp.RTCPMuxed(s, s, i)

		if ps, err := m.Preconditions(); err == nil {
			_ = sdp.PreconditionsMet(ps)
		}

		cd := string(m.CodecData(sdp.Uplink, sdp.CodecOffer))
		if strings.ContainsAny(cd, "\r\x00") || !strings.HasPrefix(cd, "uplink\noffer\nm=") {
			t.Fatalf("media %d: bad Codec-Data %q", i, cd)
		}
	}

	_, _ = s.AddrTypes()
}

func mediaLines(s *sdp.Session) []sdp.Lines {
	var out []sdp.Lines

	for _, m := range s.Media {
		out = append(out, m.Lines)
	}

	return out
}
