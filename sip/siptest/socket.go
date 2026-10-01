package siptest

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

const Timeout = 5 * time.Second

type Received struct {
	Msg  sip.Message
	Flow sip.Flow
}

type Socket struct {
	t    testing.TB
	addr netip.AddrPort
	udp  *net.UDPConn
	tcp  net.Listener
	recv chan Received
	done chan struct{}
	once sync.Once

	mu     sync.Mutex
	conns  map[netip.AddrPort]net.Conn
	opened int
	wg     sync.WaitGroup
}

func NewSocket(t testing.TB, addr netip.AddrPort) *Socket {
	t.Helper()

	var (
		tl  net.Listener
		pc  net.PacketConn
		err error
	)

	for range 16 {
		if tl, pc, err = bind(addr); err == nil || addr.Port() != 0 {
			break
		}
	}

	if err != nil {
		t.Fatalf("siptest: listen on %s: %v", addr, err)
	}

	addr = tl.Addr().(*net.TCPAddr).AddrPort()

	s := &Socket{
		t:     t,
		addr:  netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port()),
		udp:   pc.(*net.UDPConn),
		tcp:   tl,
		recv:  make(chan Received, 256),
		done:  make(chan struct{}),
		conns: make(map[netip.AddrPort]net.Conn),
	}

	s.wg.Add(2)

	go s.readUDP()
	go s.accept()

	t.Cleanup(s.Close)

	return s
}

func bind(addr netip.AddrPort) (net.Listener, net.PacketConn, error) {
	ctx := context.Background()
	lc := net.ListenConfig{Control: sockopt.ReusePort}

	tl, err := lc.Listen(ctx, network("tcp", addr.Addr()), addr.String())
	if err != nil {
		return nil, nil, err
	}

	var plain net.ListenConfig

	pc, err := plain.ListenPacket(ctx, network("udp", addr.Addr()), tl.Addr().String())
	if err != nil {
		_ = tl.Close()
		return nil, nil, err
	}

	return tl, pc, nil
}

func (s *Socket) Addr() netip.AddrPort { return s.addr }

func (s *Socket) Close() {
	s.once.Do(func() {
		close(s.done)

		_ = s.udp.Close()
		_ = s.tcp.Close()

		s.mu.Lock()
		for _, c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()

		s.wg.Wait()
	})
}

func (s *Socket) Send(tr sip.Transport, to netip.AddrPort, m sip.Message) {
	s.t.Helper()
	s.SendRaw(tr, to, m.Bytes())
}

func (s *Socket) SendRaw(tr sip.Transport, to netip.AddrPort, b []byte) {
	s.t.Helper()

	switch tr {
	case sip.UDP:
		if _, err := s.udp.WriteToUDPAddrPort(b, to); err != nil {
			s.t.Fatalf("siptest: send UDP from %s to %s: %v", s.addr, to, err)
		}
	case sip.TCP:
		if _, err := s.Conn(to).Write(b); err != nil {
			s.t.Fatalf("siptest: send TCP from %s to %s: %v", s.addr, to, err)
		}
	default:
		s.t.Fatalf("siptest: unsupported transport %q", tr)
	}
}

func (s *Socket) Conn(to netip.AddrPort) net.Conn {
	s.t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if c, ok := s.conns[to]; ok {
		return c
	}

	d := net.Dialer{LocalAddr: net.TCPAddrFromAddrPort(s.addr), Timeout: Timeout, Control: sockopt.ReusePort}

	c, err := d.Dial(network("tcp", to.Addr()), to.String())
	if err != nil {
		s.t.Fatalf("siptest: dial %s from %s: %v", to, s.addr, err)
	}

	s.track(c)

	return c
}

func (s *Socket) Opened() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.opened
}

func (s *Socket) CloseConn(to netip.AddrPort) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c, ok := s.conns[to]; ok {
		_ = c.Close()

		delete(s.conns, to)
	}
}

func (s *Socket) Recv() Received {
	s.t.Helper()

	select {
	case r := <-s.recv:
		return r
	case <-time.After(Timeout):
		s.t.Fatalf("siptest: nothing received on %s", s.addr)
		return Received{}
	}
}

func (s *Socket) RecvRequest() (*sip.Request, sip.Flow) {
	s.t.Helper()

	r := s.Recv()

	req, ok := r.Msg.(*sip.Request)
	if !ok {
		s.t.Fatalf("siptest: got %q on %s, want a request", r.Msg.StartLine(), s.addr)
	}

	return req, r.Flow
}

func (s *Socket) RecvResponse() (*sip.Response, sip.Flow) {
	s.t.Helper()

	r := s.Recv()

	res, ok := r.Msg.(*sip.Response)
	if !ok {
		s.t.Fatalf("siptest: got %q on %s, want a response", r.Msg.StartLine(), s.addr)
	}

	return res, r.Flow
}

func (s *Socket) RecvNone(d time.Duration) {
	s.t.Helper()

	select {
	case r := <-s.recv:
		s.t.Fatalf("siptest: unexpected %q on %s", r.Msg.StartLine(), s.addr)
	case <-time.After(d):
	}
}

func (s *Socket) track(c net.Conn) {
	select {
	case <-s.done:
		_ = c.Close()
		return
	default:
	}

	remote := c.RemoteAddr().(*net.TCPAddr).AddrPort()
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	s.conns[remote] = c
	s.opened++
	s.wg.Add(1)

	go s.readTCP(c, remote)
}

func (s *Socket) accept() {
	defer s.wg.Done()

	for {
		c, err := s.tcp.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		s.track(c)
		s.mu.Unlock()
	}
}

func (s *Socket) readUDP() {
	defer s.wg.Done()

	buf := make([]byte, 65535)

	for {
		n, src, err := s.udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}

		m, err := sip.Parse(buf[:n])
		if err != nil {
			continue
		}

		s.deliver(m, sip.Flow{Transport: sip.UDP, Local: s.addr, Remote: netip.AddrPortFrom(src.Addr().Unmap(), src.Port())})
	}
}

func (s *Socket) readTCP(c net.Conn, remote netip.AddrPort) {
	defer s.wg.Done()

	sr := sip.NewStreamReader(c, 0)

	for {
		m, _, err := sr.Next()

		var perr *sip.ParseError

		switch {
		case err == nil && m != nil:
			s.deliver(m, sip.Flow{Transport: sip.TCP, Local: s.addr, Remote: remote})
		case err == nil, errors.As(err, &perr):
		default:
			return
		}
	}
}

func (s *Socket) deliver(m sip.Message, f sip.Flow) {
	m.Env().Flow = f

	select {
	case s.recv <- Received{Msg: m, Flow: f}:
	case <-s.done:
	}
}

func network(prefix string, a netip.Addr) string {
	if a.Is4() || a.Is4In6() {
		return prefix + "4"
	}

	return prefix + "6"
}
