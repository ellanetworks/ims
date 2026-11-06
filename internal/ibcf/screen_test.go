package ibcf

import (
	"errors"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

func TestScreen(t *testing.T) {
	sdp := sip.NewBodyPart("application/sdp", []byte("v=0\r\n"))

	part := func(mediaType string, fields ...sip.Field) sip.BodyPart {
		p := sip.NewBodyPart(mediaType, []byte("x"))
		for _, f := range fields {
			p.Header.Set(f.Name, f.Value)
		}

		return p
	}

	gzip := sip.Field{Name: "Content-Encoding", Value: "gzip"}

	tests := []struct {
		name     string
		parts    []sip.BodyPart
		code     int
		encoding bool
	}{
		{name: "SDP", parts: []sip.BodyPart{sdp}},
		{name: "SDP and DTMF", parts: []sip.BodyPart{sdp, part("application/dtmf-relay")}},
		{name: "required unknown type", parts: []sip.BodyPart{sdp, part("application/x-unknown")}, code: 415},
		{
			name:  "optional unknown type",
			parts: []sip.BodyPart{sdp, part("application/x-unknown", sip.Field{Name: "Content-Disposition", Value: "render;handling=optional"})},
		},
		{name: "required unknown encoding", parts: []sip.BodyPart{sdp, part("application/dtmf-relay", gzip)}, code: 415, encoding: true},
		{name: "unknown encoding of the only body", parts: []sip.BodyPart{part("application/sdp", gzip)}, code: 415, encoding: true},
		{
			name: "optional unknown encoding",
			parts: []sip.BodyPart{sdp, part("application/x-unknown", gzip,
				sip.Field{Name: "Content-Disposition", Value: "render;handling=optional"})},
		},
		{name: "required 3GPP IM CN subsystem XML body", parts: []sip.BodyPart{sdp, part("application/3gpp-ims+xml")}, code: 415},
		{
			name:  "optional 3GPP IM CN subsystem XML body",
			parts: []sip.BodyPart{sdp, part("application/3gpp-ims+xml", sip.Field{Name: "Content-Disposition", Value: "render;handling=optional"})},
		},
		{
			name:  "required early session",
			parts: []sip.BodyPart{sdp, part("application/sdp", sip.Field{Name: "Content-Disposition", Value: "early-session"})},
			code:  415,
		},
		{
			name:  "optional early session",
			parts: []sip.BodyPart{sdp, part("application/sdp", sip.Field{Name: "Content-Disposition", Value: "early-session;handling=optional"})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := sip.NewRequest("INVITE", sip.URI{Scheme: "sip", Host: "example.com"})
			req.SetBodies(tt.parts...)

			err := screen(req)

			var serr *sip.StatusError

			switch {
			case tt.code == 0 && err != nil:
				t.Fatalf("screen = %v, want nil", err)
			case tt.code == 0:
			case !errors.As(err, &serr) || serr.StatusCode != tt.code:
				t.Fatalf("screen = %v, want %d", err, tt.code)
			case tt.encoding && serr.Header.Get("Accept-Encoding") != "identity":
				t.Fatalf("415 without Accept-Encoding: %v", serr.Header)
			case !tt.encoding && serr.Header.Get("Accept") == "":
				t.Fatalf("415 without Accept: %v", serr.Header)
			}
		})
	}
}
