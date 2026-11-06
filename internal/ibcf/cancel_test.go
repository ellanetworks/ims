package ibcf

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

func resolveBlocking(s *scene, blocked netip.Addr) <-chan struct{} {
	resolving := make(chan struct{}, 1)

	s.ibcf.cfg.Resolve = func(ctx context.Context, u sip.URI, trs []sip.Transport) ([]sip.Server, error) {
		if a, err := netip.ParseAddr(u.Host); err == nil && a != blocked {
			return SystemResolve(ctx, u, trs)
		}

		resolving <- struct{}{}

		<-ctx.Done()

		return nil, ctx.Err()
	}

	return resolving
}

func awaitLookup(t *testing.T, resolving <-chan struct{}) {
	t.Helper()

	select {
	case <-resolving:
	case <-time.After(wait):
		t.Fatal("no lookup")
	}
}

func TestExitCancelDuringFailoverLookup(t *testing.T) {
	s := newScene(t, false)
	withSecondTrunk(s, false)

	resolving := resolveBlocking(s, carrier2Addr.Addr())

	o := s.call("offer-1")

	_ = s.carrier.next("invite").req.Reject(503)

	awaitLookup(t, resolving)

	_ = o.Cancel(context.Background(), ua.Change{})

	if e := s.ims.until("failed"); e.res == nil || e.res.StatusCode != 487 {
		t.Fatalf("got %v, want 487", e.res)
	}

	s.settle(s.ims, s.carrier)
}

func TestExitCancelDuringFirstLookup(t *testing.T) {
	s := newScene(t, false)

	tr := s.trunk
	tr.Addresses = []settings.TrunkAddress{{Host: "sip.carrier.example"}}
	s.setTrunk(tr)

	resolving := resolveBlocking(s, netip.Addr{})

	o := s.call("offer-1")

	awaitLookup(t, resolving)

	_ = o.Cancel(context.Background(), ua.Change{})

	if e := s.ims.until("failed"); e.res == nil || e.res.StatusCode != 487 {
		t.Fatalf("got %v, want 487", e.res)
	}

	s.carrier.none()
}

func TestRouteAddressesDuringLookup(t *testing.T) {
	s := newScene(t, false)

	resolving := make(chan struct{})

	s.ibcf.cfg.Resolve = func(ctx context.Context, _ sip.URI, _ []sip.Transport) ([]sip.Server, error) {
		close(resolving)
		<-ctx.Done()

		return nil, ctx.Err()
	}

	stop := make(chan struct{})
	rt := s.ibcf.newRoute([]settings.Trunk{{ID: "a", Addresses: []settings.TrunkAddress{{Host: "sip.carrier.example"}}}}, stop)

	done := make(chan struct{})

	go func() {
		defer close(done)

		_, _ = rt.next(t.Context())
	}()

	<-resolving

	got := make(chan struct{})

	go func() {
		rt.addresses("a")
		close(got)
	}()

	select {
	case <-got:
	case <-time.After(resolveTimeout / 2):
		t.Error("addresses blocked by the lookup")
	}

	close(stop)
	<-done
	<-got
}

func TestExitStrayCancelKeepsFailover(t *testing.T) {
	s := newScene(t, false)
	second := withSecondTrunk(s, false)

	o := s.call("offer-1")
	in := s.carrier.next("invite").req

	stray, err := sip.NewCancel(o.Request())
	if err != nil {
		t.Fatal(err)
	}

	via, _ := stray.Header.TopVia()
	via.Params.Set("branch", sip.NewBranch())
	_ = stray.Header.SetTopVia(via)
	stray.Flow = sip.Flow{Transport: sip.UDP, Local: internalAddr, Remote: imsAddr}

	if err := s.ibcf.Filter(stray); err != nil {
		t.Fatal(err)
	}

	l := s.ibcf.legs.received(stray)
	if l == nil {
		t.Fatal("no call of the CANCEL")
	}

	if l.call.cancelled() {
		t.Fatal("call cancelled by a CANCEL of another transaction")
	}

	select {
	case <-l.call.abandoned:
		t.Fatal("route abandoned by a CANCEL of another transaction")
	default:
	}

	_ = in.Reject(503)

	_ = second.next("invite").req.Accept(sdpOf("answer-1")...)

	s.ims.until("established")
}

func TestInvalidCancelKeepsCall(t *testing.T) {
	s := newScene(t, false)

	o := s.call("offer-1")
	in := s.carrier.next("invite").req

	invalid, err := sip.NewCancel(o.Request())
	if err != nil {
		t.Fatal(err)
	}

	via, _ := invalid.Header.TopVia()
	via.Params.Set("branch", "old")
	_ = invalid.Header.SetTopVia(via)
	invalid.Flow = sip.Flow{Transport: sip.UDP, Local: internalAddr, Remote: imsAddr}

	var serr *sip.StatusError
	if err := s.ibcf.Filter(invalid); !errors.As(err, &serr) || serr.StatusCode != 400 {
		t.Fatalf("Filter = %v, want 400", err)
	}

	l := s.ibcf.legs.received(invalid)
	if l == nil {
		t.Fatal("no call of the CANCEL")
	}

	if l.call.cancelled() {
		t.Fatal("call cancelled by an invalid CANCEL")
	}

	_ = in.Accept(sdpOf("answer-1")...)

	s.ims.until("established")
}
