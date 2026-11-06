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

// RFC 5626 section 4.4.1
var ErrNoPong = errors.New("sip/transport: keep-alive not answered")

type pongs struct {
	mu  sync.Mutex
	tcp map[*conn][]chan struct{}
	udp map[[12]byte]chan struct{}
}

func newPongs() *pongs {
	return &pongs{tcp: map[*conn][]chan struct{}{}, udp: map[[12]byte]chan struct{}{}}
}

// RFC 5626 section 4.4.1
// RFC 5626 section 4.4.2
// RFC 5389 section 7.1
func (t *Transport) Ping(ctx context.Context, f sip.Flow) error {
	switch f.Transport {
	case sip.TCP, sip.TLS:
		return t.pingTCP(ctx, f)
	case sip.UDP:
		return t.pingUDP(ctx, f)
	}

	return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, ErrUnsupportedTransport)
}

func (t *Transport) pingTCP(ctx context.Context, f sip.Flow) error {
	c, err := t.conn(ctx, f, false, false)
	if err != nil {
		return fmt.Errorf("sip/transport: keep-alive on %s: %w", f, err)
	}

	return t.pingConn(ctx, c, f)
}

func (t *Transport) pingConn(ctx context.Context, c *conn, f sip.Flow) error {
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

func (t *Transport) tcpPong(c *conn) {
	c.ponged.Store(true)

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
