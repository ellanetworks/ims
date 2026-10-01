package sip

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

const DefaultMaxMessageSize = 64 << 10

type Keepalive int

const (
	KeepaliveNone Keepalive = iota

	KeepalivePing

	KeepalivePong
)

var (
	ErrMissingContentLength = errors.New("missing Content-Length on a stream")

	ErrMessageTooLarge = errors.New("message too large")
)

type TooLargeError struct {
	Limit int

	Request *Request
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("sip: %v: above %d bytes", ErrMessageTooLarge, e.Limit)
}

func (e *TooLargeError) Is(target error) bool { return target == ErrMessageTooLarge }

type StreamReader struct {
	r       *bufio.Reader
	maxSize int
	head    []byte

	crlfs int
}

func NewStreamReader(r io.Reader, maxSize int) *StreamReader {
	if maxSize <= 0 {
		maxSize = DefaultMaxMessageSize
	}

	return &StreamReader{r: bufio.NewReader(r), maxSize: maxSize}
}

func (s *StreamReader) Next() (Message, Keepalive, error) {
	n, err := s.skipCRLF()
	if err != nil {
		return nil, KeepaliveNone, err
	}

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

	start, fields, ferr := splitHead(string(head))

	if !fields.Has("Content-Length") {
		return nil, KeepaliveNone, fmt.Errorf("sip: %w", ErrMissingContentLength)
	}

	size, err := contentLength(fields)
	if err != nil {
		return nil, KeepaliveNone, fmt.Errorf("sip: unframeable message: %w", err)
	}

	if len(head)+len(crlfcrlf)+size > s.maxSize {
		msg, _ := newMessage(start, fields)
		return nil, KeepaliveNone, &TooLargeError{Limit: s.maxSize, Request: asRequest(msg)}
	}

	var body []byte

	if size > 0 {
		body = make([]byte, size)
		if _, err := io.ReadFull(s.r, body); err != nil {
			return nil, KeepaliveNone, unexpectedEOF(err)
		}
	}

	msg, err := assemble(start, fields, ferr)
	if err != nil {
		return nil, KeepaliveNone, err
	}

	setMessageBody(msg, body)

	return msg, KeepaliveNone, nil
}

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

func (s *StreamReader) readHead() ([]byte, error) {
	s.head = s.head[:0]

	for {
		line, err := s.r.ReadSlice('\n')
		s.head = append(s.head, line...)

		if len(s.head) > s.maxSize {
			return nil, &TooLargeError{Limit: s.maxSize}
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
