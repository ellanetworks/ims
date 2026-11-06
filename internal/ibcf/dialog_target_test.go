package ibcf

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// SIPconnect 2.0 section 17.1.1.2
func TestExitDialogTargetResolvedOnce(t *testing.T) {
	s := newScene(t, false)

	var lookups atomic.Int32

	s.ibcf.cfg.Resolve = func(ctx context.Context, u sip.URI, trs []sip.Transport) ([]sip.Server, error) {
		if u.User == "carrier" {
			lookups.Add(1)
		}

		return SystemResolve(ctx, u, trs)
	}

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	for range 3 {
		done := make(chan *sip.Response, 1)
		_ = e.s.Send(context.Background(), "INFO", ua.Change{}, func(res *sip.Response, _ error) { done <- res })

		_ = s.carrier.next("request").req.Respond(ua.Reply{Code: 200})

		s.net.Quiesce()

		if res := <-done; res == nil || res.StatusCode != 200 {
			t.Fatalf("INFO: %v", res)
		}
	}

	if n := lookups.Load(); n != 1 {
		t.Errorf("%d lookups of the carrier's dialog target, want 1", n)
	}
}
