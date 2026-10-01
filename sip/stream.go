package sip

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// DefaultMaxMessageSize bounds a message read from a stream when no other
// limit is given.
const DefaultMaxMessageSize = 64 << 10

// Keepalive is a CRLF keepalive read from a stream (RFC 5626 §3.5.1).
type Keepalive int

const (
	// KeepaliveNone means a message was read.
	KeepaliveNone Keepalive = iota
	// KeepalivePing is a double CRLF, possibly split across reads. The receiver
	// answers with a single CRLF.
	KeepalivePing
	// KeepalivePong is a single CRLF. A second one before any message makes a Ping.
	KeepalivePong
)

// Errors that end a stream, returned wrapped by StreamReader.Next.
var (
	// ErrMissingContentLength is a message without Content-Length, which
	// cannot be framed on a stream (RFC 3261 §18.3).
	ErrMissingContentLength = errors.New("missing Content-Length on a stream")
	// ErrMessageTooLarge is a message above the reader's size limit. The
	// peer may be told with a 513 before the connection is closed.
	ErrMessageTooLarge = errors.New("message too large")
)

// StreamReader reads messages from a stream transport such as TCP,
// framing them by Content-Length.
type StreamReader struct {
	r       *bufio.Reader
	maxSize int
	head    []byte
	// crlfs counts the CRLFs read since the last message or ping.
	crlfs int
}

// NewStreamReader returns a reader of messages up to maxSize bytes, or
// DefaultMaxMessageSize when maxSize is 0.
func NewStreamReader(r io.Reader, maxSize int) *StreamReader {
	if maxSize <= 0 {
		maxSize = DefaultMaxMessageSize
	}

	return &StreamReader{r: bufio.NewReader(r), maxSize: maxSize}
}

// Next returns the next message, or the next keepalive with a nil message.
//
// A *ParseError means the message was framed but could not be parsed: the
// stream is still in sync and Next may be called again. Any other error,
// io.EOF at a message boundary included, ends the stream.
func (s *StreamReader) Next() (Message, Keepalive, error) {
	n, err := s.skipCRLF()
	if err != nil {
		return nil, KeepaliveNone, err
	}

	// A ping may arrive split across reads: count CRLFs across calls, so
	// that a second lone CRLF completes it.
	if n > 0 {
		s.crlfs += n
		if s.crlfs >= 2 {
			s.crlfs = 0
			return nil, KeepalivePing, nil
		}

		return nil, KeepalivePong, nil
	}

	s.crlfs = 0

	head, err := s.readHead()
	if err != nil {
		return nil, KeepaliveNone, err
	}

	// Framing needs only Content-Length: a malformed field or start line
	// is reported once the body is read, and the stream stays in sync.
	start, fields, ferr := splitHead(string(head))

	if !fields.Has("Content-Length") {
		return nil, KeepaliveNone, fmt.Errorf("sip: %w", ErrMissingContentLength)
	}

	size, err := contentLength(fields)
	if err != nil {
		return nil, KeepaliveNone, fmt.Errorf("sip: unframeable message: %w", err)
	}

	if len(head)+len(crlfcrlf)+size > s.maxSize {
		return nil, KeepaliveNone, fmt.Errorf("sip: %w: above %d bytes", ErrMessageTooLarge, s.maxSize)
	}

	var body []byte

	if size > 0 {
		body = make([]byte, size)
		if _, err := io.ReadFull(s.r, body); err != nil {
			return nil, KeepaliveNone, unexpectedEOF(err)
		}
	}

	msg, err := newMessage(start, fields)
	if err != nil {
		return nil, KeepaliveNone, err
	}

	if ferr != nil {
		ferr.Request = asRequest(msg)
		return nil, KeepaliveNone, ferr
	}

	setMessageBody(msg, body)

	return msg, KeepaliveNone, nil
}

// skipCRLF consumes the CRLFs before a message and returns how many there
// were. It blocks for the first two bytes only, so that a lone CRLF is
// reported as soon as it arrives.
func (s *StreamReader) skipCRLF() (int, error) {
	n := 0

	for n == 0 || s.r.Buffered() >= 2 {
		p, err := s.r.Peek(2)
		if len(p) < 2 {
			if len(p) == 0 && errors.Is(err, io.EOF) {
				return 0, io.EOF
			}

			return 0, unexpectedEOF(err)
		}

		if p[0] != '\r' || p[1] != '\n' {
			break
		}

		_, _ = s.r.Discard(2)
		n++
	}

	return n, nil
}

// readHead reads up to the empty line ending the header section and
// returns the section without it.
func (s *StreamReader) readHead() ([]byte, error) {
	s.head = s.head[:0]

	for {
		line, err := s.r.ReadSlice('\n')
		s.head = append(s.head, line...)

		if len(s.head) > s.maxSize {
			return nil, fmt.Errorf("sip: %w: header section above %d bytes", ErrMessageTooLarge, s.maxSize)
		}

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			return nil, unexpectedEOF(err)
		}

		if bytes.HasSuffix(s.head, crlfcrlf) {
			return s.head[:len(s.head)-len(crlfcrlf)], nil
		}
	}
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}

	return err
}
