package transport

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ellanetworks/ims/sip"
)

// ErrNoPong is the error of a keep-alive the other end did not answer (RFC 5626 §4.4.1): the flow failed.
var ErrNoPong = errors.New("sip/transport: keep-alive not answered")

// pongs are the keep-alives waiting for their answers: on a TCP connection, the next CRLF on it; on UDP, the STUN
// Binding response of the request's transaction ID.
type pongs struct {
	mu  sync.Mutex
	tcp map[*conn][]chan struct{}
	udp map[[12]byte]chan struct{}
}

func newPongs() *pongs {
	return &pongs{tcp: map[*conn][]chan struct{}{}, udp: map[[12]byte]chan struct{}{}}
}

// Ping sends a keep-alive on flow f and waits for its answer until ctx is done (RFC 5626 §4.4.1, §4.4.2): a double
// CRLF on its TCP connection, answered by a CRLF; on UDP, a STUN Binding request from its local address (RFC 5389
// §7.1), answered by its response. It fails with ErrNoPong without one.
func (t *Transport) Ping(ctx context.Context, f sip.Flow) error {
	switch f.Transport {
	case sip.TCP:
		return t.pingTCP(ctx, f)
	case sip.UDP:
		return t.pingUDP(ctx, f)
	}

	return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, ErrUnsupportedTransport)
}

func (t *Transport) pingTCP(ctx context.Context, f sip.Flow) error {
	c, err := t.conn(ctx, f, false)
	if err != nil {
		return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, err)
	}

	ch := make(chan struct{}, 1)

	t.pongs.mu.Lock()
	t.pongs.tcp[c] = append(t.pongs.tcp[c], ch)
	t.pongs.mu.Unlock()

	defer func() {
		t.pongs.mu.Lock()
		defer t.pongs.mu.Unlock()

		waiting := t.pongs.tcp[c]
		for i, w := range waiting {
			if w == ch {
				waiting = append(waiting[:i], waiting[i+1:]...)
				break
			}
		}

		if len(waiting) == 0 {
			delete(t.pongs.tcp, c)
		} else {
			t.pongs.tcp[c] = waiting
		}
	}()

	if err := t.write(c, []byte("\r\n\r\n")); err != nil {
		return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, err)
	}

	return awaitPong(ctx, ch, f)
}

// tcpPong is a CRLF on c: the answer to its keep-alives.
func (t *Transport) tcpPong(c *conn) {
	t.pongs.mu.Lock()
	defer t.pongs.mu.Unlock()

	for _, ch := range t.pongs.tcp[c] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (t *Transport) pingUDP(ctx context.Context, f sip.Flow) error {
	t.mu.Lock()
	l, ok := t.listeners[unmap(f.Local)]
	t.mu.Unlock()

	if !ok {
		return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, ErrNoListener)
	}

	var id [12]byte

	_, _ = rand.Read(id[:])

	ch := make(chan struct{}, 1)

	t.pongs.mu.Lock()
	t.pongs.udp[id] = ch
	t.pongs.mu.Unlock()

	defer func() {
		t.pongs.mu.Lock()
		defer t.pongs.mu.Unlock()

		delete(t.pongs.udp, id)
	}()

	req := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(req[0:2], stunBindingRequest)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], id[:])

	if _, err := l.udp.WriteToUDPAddrPort(req, f.Remote); err != nil {
		return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, err)
	}

	return awaitPong(ctx, ch, f)
}

// udpPong is a STUN message received: the answer to a keep-alive, if a Binding response of one's transaction ID.
func (t *Transport) udpPong(data []byte) {
	if len(data) < stunHeaderSize || binary.BigEndian.Uint16(data[0:2]) != stunBindingResponse ||
		binary.BigEndian.Uint32(data[4:8]) != stunMagicCookie {
		return
	}

	var id [12]byte

	copy(id[:], data[8:20])

	t.pongs.mu.Lock()
	defer t.pongs.mu.Unlock()

	if ch, ok := t.pongs.udp[id]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func awaitPong(ctx context.Context, ch chan struct{}, f sip.Flow) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, ErrNoPong)
	}
}
