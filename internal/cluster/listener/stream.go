package listener

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

type Stream struct {
	l       *Listener
	alpn    string
	addr    string
	accepts chan net.Conn
	done    chan struct{}
	once    sync.Once
}

var _ raft.StreamLayer = (*Stream)(nil)

func (l *Listener) Stream(alpn, advertise string) *Stream {
	s := &Stream{l: l, alpn: alpn, addr: advertise, accepts: make(chan net.Conn), done: make(chan struct{})}

	l.Handle(alpn, func(c net.Conn) {
		select {
		case s.accepts <- c:
		case <-s.done:
			_ = c.Close()
		}
	})

	return s
}

func (s *Stream) Accept() (net.Conn, error) {
	select {
	case c := <-s.accepts:
		return c, nil
	case <-s.done:
		return nil, net.ErrClosed
	}
}

func (s *Stream) Close() error {
	s.once.Do(func() { close(s.done) })

	return nil
}

type advertised string

func (a advertised) Network() string { return "tcp" }

func (a advertised) String() string { return string(a) }

func (s *Stream) Addr() net.Addr { return advertised(s.addr) }

func (s *Stream) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case <-s.done:
		return nil, errors.New("the cluster stream is closed")
	default:
	}

	c, _, err := s.l.Dial(ctx, string(address), s.alpn, nil)
	if err != nil {
		return nil, err
	}

	return c, nil
}
