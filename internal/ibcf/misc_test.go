package ibcf

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

func TestExitMethodNotAllowed(t *testing.T) {
	s := newScene(t, false)

	req := sip.NewRequest("MESSAGE", sip.URI{Scheme: "tel", User: carol})
	req.Header.Add("Route", "<sip:"+internalAddr.String()+";lr>")
	req.Header.Add("From", "<sip:alice@"+home+">")
	req.Header.Add("To", "<tel:"+carol+">")

	done := make(chan *sip.Response, 1)

	if err := s.ims.a.Request(context.Background(), req, func(res *sip.Response, _ error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 405 || !res.Header.Has("Allow") {
		t.Fatalf("MESSAGE: %v", res)
	}
}

func TestExitInfo(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	e := s.ims.next("established")
	s.ims.next("answer")
	s.carrier.next("established")

	done := make(chan *sip.Response, 1)
	_ = e.s.Send(context.Background(), "INFO", ua.Change{}, func(res *sip.Response, _ error) { done <- res })

	r := s.carrier.next("request").req
	if r.Message().Method != "INFO" {
		t.Fatalf("trunk got %s", r.Message().Method)
	}

	_ = r.Respond(ua.Reply{Code: 200})

	s.net.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("INFO: %v", res)
	}
}

func TestRouteTargets(t *testing.T) {
	s := newScene(t, false)

	trunks := []settings.Trunk{
		{ID: "a", Addresses: []settings.TrunkAddress{{Host: "sip.carrier.example"}, {Host: "2001:db8::1", Port: 5070, Transport: settings.SIPTransportTCP}}},
		{ID: "b", Trusted: true, Addresses: []settings.TrunkAddress{{Host: "198.51.100.7"}, {Host: "198.51.100.8", Transport: settings.SIPTransportTCP}}},
	}

	var asked [][]sip.Transport

	s.ibcf.cfg.Resolve = func(_ context.Context, u sip.URI, trs []sip.Transport) ([]sip.Server, error) {
		asked = append(asked, trs)

		switch u.String() {
		case "sip:sip.carrier.example":
			return []sip.Server{
				{Transport: sip.UDP, Addr: netip.MustParseAddrPort("[2001:db8::7]:5060")},
				{Transport: sip.UDP, Addr: netip.MustParseAddrPort("198.51.100.1:5080")},
				{Transport: sip.TLS, Addr: netip.MustParseAddrPort("198.51.100.1:5061")},
			}, nil
		default:
			return SystemResolve(context.Background(), u, trs)
		}
	}

	rt := s.ibcf.newRoute(trunks, nil)

	var got []string

	for {
		tg, ok := rt.next(t.Context())
		if !ok {
			break
		}

		got = append(got, tg.trunk.ID+" "+string(tg.server.Transport)+" "+tg.server.Addr.String())
	}

	want := []string{"a UDP 198.51.100.1:5080", "b TCP 198.51.100.7:5060", "b TCP 198.51.100.8:5060"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("targets %q, want %q", got, want)
	}

	if fmt.Sprint(asked[len(asked)-1]) != "[TCP]" {
		t.Errorf("transports of a trusted trunk %v, want TCP only", asked[len(asked)-1])
	}
}

func TestTrunkRequestURIHost(t *testing.T) {
	for domain, want := range map[string]string{
		"sip.carrier.example": "sip.carrier.example",
		"2001:db8::1":         "[2001:db8::1]",
		"198.51.100.1":        "198.51.100.1",
	} {
		if got := host(domain); got != want {
			t.Errorf("host(%s) = %s, want %s", domain, got, want)
		}
	}
}

func TestRouteAbandonedDuringResolution(t *testing.T) {
	s := newScene(t, false)

	resolving := make(chan struct{})

	s.ibcf.cfg.Resolve = func(ctx context.Context, _ sip.URI, _ []sip.Transport) ([]sip.Server, error) {
		close(resolving)
		<-ctx.Done()

		return nil, ctx.Err()
	}

	stop := make(chan struct{})
	rt := s.ibcf.newRoute([]settings.Trunk{{ID: "a", Addresses: []settings.TrunkAddress{{Host: "sip.carrier.example"}}}}, stop)

	go func() {
		<-resolving
		close(stop)
	}()

	start := time.Now()

	if _, ok := rt.next(t.Context()); ok {
		t.Fatal("target of an abandoned route")
	}

	if d := time.Since(start); d >= resolveTimeout {
		t.Fatalf("abandoned after %s, waiting for the lookup", d)
	}
}

func TestRouteBudgetCoversResolution(t *testing.T) {
	s := newScene(t, false)

	s.ibcf.cfg.Resolve = func(context.Context, sip.URI, []sip.Transport) ([]sip.Server, error) {
		t.Error("resolved after the failover budget")
		return nil, nil
	}

	rt := s.ibcf.newRoute([]settings.Trunk{{ID: "a", Addresses: []settings.TrunkAddress{{Host: "sip.carrier.example"}}}}, nil)
	rt.deadline = time.Now()

	if _, ok := rt.next(t.Context()); ok {
		t.Fatal("target after the failover budget")
	}
}
