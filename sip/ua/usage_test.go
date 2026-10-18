package ua_test

import (
	"fmt"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// RFC 5057 §5.1–§5.3: the failure of a request within the dialog of a session affects what Table 2 says, for the
// usage the request belongs to: INFO belongs to the INVITE session, MESSAGE to none.
func TestFailureEffects(t *testing.T) {
	for _, c := range []struct {
		method string
		code   int
		ends   bool
		bye    bool
	}{
		{"INFO", 481, true, false},
		{"INFO", 480, true, true},
		{"INFO", 404, true, false},
		{"INFO", 405, false, false},
		{"INFO", 500, false, false},
		{"MESSAGE", 481, false, false},
		{"MESSAGE", 410, true, false},
	} {
		t.Run(fmt.Sprintf("%s %d", c.method, c.code), func(t *testing.T) {
			bob := newPeer(t, "bob", nil, ua.SessionTimer{})
			r := newRaw(t)

			invite := r.invite(bob, realSDP(4000, 1))
			in := bob.next("invite").req
			_ = in.Accept(ua.SDP([]byte(realSDP(5000, 1))))
			ok := r.response()
			r.ack(invite, ok)

			bs := bob.next("established").s

			done, f := results()

			if err := bs.Send(c.method, ua.Change{}, f); err != nil {
				t.Fatal(err)
			}

			req, fl := r.sock.RecvRequest()
			r.sock.Send(sip.UDP, fl.Remote, sip.NewResponse(req, c.code, ""))

			if res := recv(t, done); res.code() != c.code {
				t.Fatalf("%s %d", c.method, res.code())
			}

			if !c.ends {
				bob.none()

				if bs.State() != ua.Confirmed {
					t.Errorf("state %s", bs.State())
				}

				return
			}

			if e := bob.next("ended"); e.end.Cause != ua.EndLost {
				t.Errorf("ended %+v", e.end)
			}

			if c.bye {
				if req, _ := r.sock.RecvRequest(); req.Method != "BYE" {
					t.Errorf("got %s, want the BYE", req.Method)
				}
			} else {
				r.sock.RecvNone(0)
			}
		})
	}
}
