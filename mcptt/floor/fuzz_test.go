package floor

import (
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// FuzzServer drives a server and its members by the bytes of the input, and checks after each input that at most one
// member may talk, that only its media is forwarded, and that every message sent is one TS 24.380 §8.2 allows.
func FuzzServer(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})
	f.Add([]byte{3, 3, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 6})

	f.Fuzz(func(t *testing.T, in []byte) {
		s, out, c := newServer(t)
		out.to = func(m *Member, msg floorctl.Message) {
			if _, err := msg.App(); err != nil {
				t.Fatalf("message to %s: %v", m.cfg.ID, err)
			}
		}

		var ms []*Member

		for i, b := range in {
			var m *Member
			if len(ms) > 0 {
				m = ms[int(b>>4)%len(ms)]
			}

			switch op := b & 0xf; {
			case op == 0 || m == nil:
				ms = append(ms, s.Join(MemberConfig{ID: strconv.Itoa(i), AudioSSRC: uint32(i)}, b&0x10 != 0))
			case op == 1:
				s.Start(b&0x10 != 0)
			case op == 2 || op == 3:
				m.Receive(floorctl.Message{Type: floorctl.Request, Priority: ptr(b)})
			case op == 4:
				m.Receive(floorctl.Message{Type: floorctl.Release, AckRequired: b&0x10 != 0})
			case op == 5 || op == 6:
				m.Media()
			case op == 7:
				m.Leave()
			case op == 8:
				c.advance(time.Duration(b>>4)*time.Second, nil)
			case op == 9:
				c.advance(time.Second, nil)
				m.Media()
			case op == 10:
				s.Release()
			default:
				m.Receive(floorctl.Message{Type: floorctl.Ack})
			}

			check(t, s, out)
		}
	})
}

func check(t *testing.T, s *Server, out *serverOut) {
	t.Helper()

	talking := 0

	for _, m := range s.members {
		on := m.state == uPermitted || m.state == uPendingRevoke
		if on {
			talking++
		}

		if on != (s.granted == m) {
			t.Fatalf("%s in %d, granted %v", m.cfg.ID, m.state, s.granted == m)
		}

		if out.forward[m.cfg.ID] != (on && s.state != gReleasing) {
			t.Fatalf("%s in %d forwarded %v", m.cfg.ID, m.state, out.forward[m.cfg.ID])
		}
	}

	if talking > 1 || (s.granted != nil) != (s.state == gTaken || s.state == gPendingRevoke) {
		t.Fatalf("%d talking, general %d, granted %v", talking, s.state, s.granted != nil)
	}
}
