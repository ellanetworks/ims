package sip

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

const (
	streamOptions = "OPTIONS sip:a@b SIP/2.0\r\nVia: SIP/2.0/TCP h;branch=z9hG4bK1\r\nContent-Length: 3\r\n\r\nabc"
	streamOK      = "SIP/2.0 200 OK\r\nVia: SIP/2.0/TCP h;branch=z9hG4bK1\r\nl: 0\r\n\r\n"
)

type event struct {
	start string
	ka    Keepalive
	err   string
}

func readAll(r io.Reader, maxSize int) []event {
	s := NewStreamReader(r, maxSize)

	var out []event

	for {
		m, ka, err := s.Next()

		var perr *ParseError

		switch {
		case errors.As(err, &perr):
			out = append(out, event{err: "parse"})
			continue
		case errors.Is(err, io.EOF):
			return out
		case err != nil:
			return append(out, event{err: err.Error()})
		case m != nil:
			out = append(out, event{start: m.StartLine()})
		default:
			out = append(out, event{ka: ka})
		}
	}
}

func TestStreamReader(t *testing.T) {
	in := "\r\n\r\n" + streamOptions + streamOK + "\r\n" + streamOptions +
		"BAD<METHOD sip:a@b SIP/2.0\r\nl: 1\r\n\r\nx" +
		"OPTIONS sip:a@b SIP/2.0\r\nNo colon\r\nl: 1\r\n\r\nx" + streamOK

	msgs := []event{
		{start: "OPTIONS sip:a@b SIP/2.0"},
		{start: "SIP/2.0 200 OK"},
		{ka: KeepalivePong},
		{start: "OPTIONS sip:a@b SIP/2.0"},
		{err: "parse"},
		{err: "parse"},
		{start: "SIP/2.0 200 OK"},
	}

	for _, tc := range []struct {
		name string
		r    io.Reader
		want []event
	}{
		{"whole", strings.NewReader(in), append([]event{{ka: KeepalivePing}}, msgs...)},

		{"byte by byte", iotest.OneByteReader(strings.NewReader(in)), append([]event{{ka: KeepalivePong}, {ka: KeepalivePing}}, msgs...)},
	} {
		if got := readAll(tc.r, 0); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStreamBody(t *testing.T) {
	m, _, err := NewStreamReader(strings.NewReader(streamOptions), 0).Next()
	if err != nil {
		t.Fatal(err)
	}

	if got := string(m.(*Request).Body); got != "abc" {
		t.Errorf("body = %q", got)
	}

	if got := string(m.Bytes()); got != streamOptions {
		t.Errorf("Bytes() = %q", got)
	}
}

func TestStreamFatal(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		maxSize  int
		err      error
	}{
		{"no Content-Length", "OPTIONS sip:a@b SIP/2.0\r\nVia: x\r\n\r\n", 0, ErrMissingContentLength},
		{"truncated head", "OPTIONS sip:a@b SIP/2.0\r\nVia: x\r\n", 0, io.ErrUnexpectedEOF},
		{"truncated body", "OPTIONS sip:a@b SIP/2.0\r\nl: 10\r\n\r\nabc", 0, io.ErrUnexpectedEOF},
		{"lone CR", "\r", 0, io.ErrUnexpectedEOF},
		{"bad Content-Length", "OPTIONS sip:a@b SIP/2.0\r\nl: -1\r\n\r\n", 0, nil},
		{"header line without colon, no Content-Length", "OPTIONS sip:a@b SIP/2.0\r\nVia\r\n\r\n", 0, ErrMissingContentLength},
		{"head too large", streamOptions, 20, nil},
		{"body too large", streamOptions, len(streamOptions) - 1, nil},
	} {
		_, _, err := NewStreamReader(strings.NewReader(tc.in), tc.maxSize).Next()

		var perr *ParseError
		if err == nil || errors.Is(err, io.EOF) || errors.As(err, &perr) {
			t.Errorf("%s: err = %v, want a fatal error", tc.name, err)
		}

		if tc.err != nil && !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.err)
		}
	}
}

func TestStreamMaxSize(t *testing.T) {
	if _, _, err := NewStreamReader(strings.NewReader(streamOptions), len(streamOptions)).Next(); err != nil {
		t.Errorf("message of exactly maxSize: %v", err)
	}
}

func TestStreamTooLargeKeepsRequest(t *testing.T) {
	_, _, err := NewStreamReader(strings.NewReader(streamOptions), len(streamOptions)-1).Next()

	var terr *TooLargeError
	if !errors.As(err, &terr) || !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want a *TooLargeError", err)
	}

	if terr.Request == nil || terr.Request.Method != "OPTIONS" || terr.Request.Header.Get("Via") == "" {
		t.Errorf("Request = %v", terr.Request)
	}

	_, _, err = NewStreamReader(strings.NewReader(streamOptions), 20).Next()
	if !errors.As(err, &terr) || terr.Request != nil {
		t.Errorf("head too large: err = %v", err)
	}
}
