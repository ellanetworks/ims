//go:build linux && (amd64 || arm64)

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/testue"
)

const (
	offNet        = "+33612345678"
	carrierDomain = "sip.carrier.example"
)

var (
	ibcfExternal = netip.MustParseAddrPort("127.0.0.1:5060")
	carrierSIP   = netip.MustParseAddrPort("127.0.0.2:5060")
	strangerSIP  = netip.MustParseAddrPort("127.0.0.3:5060")
)

const carrierOffer = "v=0\r\n" +
	"o=- 1 1 IN IP4 127.0.0.2\r\n" +
	"s=-\r\n" +
	"c=IN IP4 127.0.0.2\r\n" +
	"t=0 0\r\n" +
	"m=audio 40000 RTP/AVP 116 100\r\n" +
	"a=rtpmap:116 AMR-WB/16000/1\r\n" +
	"a=fmtp:116 mode-change-capability=2;max-red=0\r\n" +
	"a=rtpmap:100 telephone-event/16000\r\n" +
	"a=ptime:20\r\n" +
	"a=sendrecv\r\n"

type carrierEvent struct {
	kind  string
	s     *ua.Session
	req   *ua.Request
	res   *sip.Response
	msg   sip.Message
	offer *ua.Offer
}

type carrier struct {
	t       *testing.T
	a       *ua.Agent
	addr    netip.AddrPort
	tr      sip.Transport
	options atomic.Int32
	events  chan carrierEvent
}

func newCarrier(t *testing.T, addr netip.AddrPort) *carrier {
	t.Helper()

	c := &carrier{t: t, addr: addr, tr: sip.UDP, events: make(chan carrierEvent, 64)}
	c.options.Store(200)

	a, err := ua.New(ua.Config{
		Prepare: c.prepare,
		Contact: sip.Address{URI: sip.URI{Scheme: "sip", Host: addr.Addr().String(), Port: addr.Port()}},
		Policy:  ua.ClientPolicy,
		OnRequest: func(_ context.Context, r *ua.Request) {
			_ = r.Respond(ua.Reply{Code: int(c.options.Load())})
		},
		OnInvite: func(_ context.Context, r *ua.Request) ua.Handler {
			c.events <- carrierEvent{kind: "invite", req: r, msg: r.Message()}
			return c.handler()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Layer().Listen(t.Context(), addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = a.Shutdown(ctx)
	})

	c.a = a

	return c
}

func (c *carrier) prepare(req *sip.Request) error {
	target := req.URI
	if r, err := req.Header.TopRoute(); err == nil {
		target = r.URI
	}

	tr, dest, err := sip.Destination(target)
	if err != nil {
		return err
	}

	req.Flow = sip.Flow{Transport: tr, Local: c.addr, Remote: dest}
	req.Header.Prepend("Via", sip.NewVia(tr, c.addr).String())

	return nil
}

func (c *carrier) handler() ua.Handler {
	return ua.Handler{
		Progress: func(_ context.Context, s *ua.Session, res *sip.Response) {
			if res.StatusCode > 100 {
				c.events <- carrierEvent{kind: "progress", s: s, res: res}
			}
		},
		Established: func(_ context.Context, s *ua.Session, m sip.Message) {
			c.events <- carrierEvent{kind: "established", s: s, msg: m}
		},
		Offer: func(_ context.Context, s *ua.Session, o *ua.Offer) {
			c.events <- carrierEvent{kind: "offer", s: s, offer: o}
		},
		Request: func(_ context.Context, s *ua.Session, r *ua.Request) {
			c.events <- carrierEvent{kind: "request", s: s, req: r, msg: r.Message()}
		},
		Bye: func(_ context.Context, s *ua.Session, r *ua.Request) {
			c.events <- carrierEvent{kind: "bye", s: s, req: r, msg: r.Message()}

			_ = r.Respond(ua.Reply{Code: 200})
		},
		Ended: func(_ context.Context, s *ua.Session, _ ua.End) { c.events <- carrierEvent{kind: "ended", s: s} },
		Failed: func(_ context.Context, _ *ua.Outgoing, res *sip.Response, _ error) {
			c.events <- carrierEvent{kind: "failed", res: res}
		},
	}
}

func (c *carrier) next(kinds ...string) carrierEvent {
	c.t.Helper()

	deadline := time.After(15 * time.Second)

	for {
		select {
		case e := <-c.events:
			for _, k := range kinds {
				if e.kind == k {
					return e
				}
			}

			c.t.Logf("carrier: skipped a %s event", e.kind)
		case <-deadline:
			c.t.Fatalf("carrier: no %v event", kinds)
			return carrierEvent{}
		}
	}
}

func (c *carrier) none(d time.Duration) {
	c.t.Helper()

	select {
	case e := <-c.events:
		c.t.Fatalf("carrier: unexpected %s event", e.kind)
	case <-time.After(d):
	}
}

func (c *carrier) invite(number string) *ua.Outgoing {
	c.t.Helper()

	uri := sip.URI{Scheme: "sip", User: number, Host: ibcfExternal.Addr().String(), Port: ibcfExternal.Port()}
	uri.Params.Set("user", "phone")

	if c.tr != sip.UDP {
		uri.Params.Set("transport", strings.ToLower(string(c.tr)))
	}

	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", `"Carol" <sip:`+offNet+"@"+carrierDomain+";user=phone>")
	req.Header.Add("To", "<sip:"+number+"@"+carrierDomain+";user=phone>")
	req.Header.Add("P-Asserted-Identity", "<sip:"+offNet+"@"+carrierDomain+";user=phone>")
	req.SetBodies(ua.SDP([]byte(carrierOffer)))

	o, err := c.a.Invite(context.Background(), req, c.handler())
	if err != nil {
		c.t.Fatal(err)
	}

	return o
}

func newIBCFScene(t *testing.T) *scene {
	t.Helper()

	return newMediaScene(t, time.Minute)
}

func newMediaScene(t *testing.T, timeout time.Duration) *scene {
	t.Helper()

	return newSceneWith(t, func(srv *server.Server) {
		srv.Config.IBCF = &config.IBCF{
			Port:      5085,
			External:  config.IBCFExternal{Addresses: []netip.Addr{ibcfExternal.Addr()}, Port: int(ibcfExternal.Port())},
			Keepalive: config.IBCFKeepalive{Failures: 1},
		}
		srv.Config.TrGW = &config.TrGW{
			PhoneAddresses: []config.TrGWAddress{{Address: imsAddrs[0].Addr()}, {Address: imsAddrs[1].Addr()}},
			TrunkAddresses: []config.TrGWAddress{{Address: ibcfExternal.Addr()}},
			MinPort:        10000, MaxPort: 10999, MediaTimeout: &timeout,
		}
	})
}

func newTrunkScene(t *testing.T, trusted bool) (*scene, *carrier) {
	t.Helper()

	s := newIBCFScene(t)
	c := newCarrier(t, carrierSIP)

	if trusted {
		c.tr = sip.TCP
	}

	id := s.addTrunk("Carrier", c, trusted, 0)

	s.post("/api/v1/sip-routes", map[string]any{"prefix": "+33", "trunks": []string{id}}, nil)
	s.post("/api/v1/sip-routes", map[string]any{"prefix": "+1555", "trunks": []string{id}}, nil)

	return s, c
}

func (s *scene) addTrunk(name string, c *carrier, trusted bool, keepalive int) string {
	s.t.Helper()

	var trunk struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}

	s.post("/api/v1/sip-trunks", map[string]any{
		"name":               name,
		"addresses":          []map[string]any{{"host": c.addr.Addr().String(), "port": c.addr.Port(), "transport": strings.ToLower(string(c.tr))}},
		"inbound_cidrs":      []string{c.addr.Addr().String() + "/32"},
		"domain":             strings.ToLower(name) + "." + carrierDomain,
		"trusted":            trusted,
		"keepalive_interval": keepalive,
		"enabled":            true,
	}, &trunk)

	return trunk.Result.ID
}

func (s *scene) post(path string, body, out any) {
	s.t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		s.t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodPost, "http://"+s.srv.APIAddr().String()+path, bytes.NewReader(raw))
	if err != nil {
		s.t.Fatal(err)
	}

	res, err := apitest.Client(s.srv.APIAddr().String()).Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusCreated {
		s.t.Fatalf("POST %s: %d", path, res.StatusCode)
	}

	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			s.t.Fatal(err)
		}
	}
}

func answerWithOffer(t *testing.T, in *ua.Request) {
	t.Helper()

	offer, ok := in.Offer()
	if !ok {
		t.Fatalf("carrier INVITE without an offer: %s", in.Message())
	}

	if _, err := sdp.Parse(offer); err != nil {
		t.Fatal(err)
	}

	if err := in.Accept(ua.SDP([]byte(carrierOffer))); err != nil {
		t.Fatal(err)
	}
}

func TestTrunkOutgoingCall(t *testing.T) {
	s, c := newTrunkScene(t, false)
	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	t.Run("caller hangs up", func(t *testing.T) {
		ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

		in := c.next("invite").req
		m := in.Message()

		if want := "sip:" + offNet + "@carrier." + carrierDomain + ";user=phone"; m.URI.String() != want {
			t.Errorf("Request-URI %s, want %s", m.URI, want)
		}

		if want := "<sip:+15550001@" + domain + ";user=phone>"; values(m, "P-Asserted-Identity") != want {
			t.Errorf("P-Asserted-Identity %q, want %q", values(m, "P-Asserted-Identity"), want)
		}

		for _, name := range []string{"P-Charging-Vector", "P-Access-Network-Info", "P-Visited-Network-ID", "Record-Route", "Route"} {
			if m.Header.Has(name) {
				t.Errorf("%s %q sent to the trunk", name, values(m, name))
			}
		}

		if m.Flow.Remote != ibcfExternal {
			t.Errorf("trunk INVITE from %s, want %s", m.Flow.Remote, ibcfExternal)
		}

		_ = in.Progress(ua.Reply{Code: 180})
		answerWithOffer(t, in)

		if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
			t.Fatalf("Wait = %v, %v", res, err)
		}

		c.next("established")

		if err := ac.Bye(ctx); err != nil {
			t.Fatalf("Bye: %v", err)
		}

		c.next("bye")
		ended(t, ac, testue.LocalBye)
	})

	t.Run("carrier hangs up", func(t *testing.T) {
		ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

		in := c.next("invite").req
		answerWithOffer(t, in)

		if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
			t.Fatalf("Wait = %v, %v", res, err)
		}

		e := c.next("established")

		if err := e.s.Bye(ctx, ua.Change{}, nil); err != nil {
			t.Fatal(err)
		}

		ended(t, ac, testue.RemoteBye)
	})

	t.Run("caller cancels", func(t *testing.T) {
		ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

		in := c.next("invite").req
		_ = in.Progress(ua.Reply{Code: 180})

		await(t, ac, "", "180 INVITE")

		if err := ac.Cancel(ctx); err != nil {
			t.Fatalf("Cancel: %v", err)
		}

		c.next("ended")
		ended(t, ac, testue.Cancelled)
	})

	t.Run("no route", func(t *testing.T) {
		ac := invite(t, a, "tel:+4930123456", testue.CallOptions{})
		failed(t, ctx, ac, 404)
		c.none(time.Second)
	})
}

func TestTrunkPrivacy(t *testing.T) {
	s, c := newTrunkScene(t, false)
	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	ac := invite(t, a, "tel:"+offNet, testue.CallOptions{Headers: []sip.Field{{Name: "Privacy", Value: "id"}}})

	m := c.next("invite").req

	if m.Message().Header.Has("P-Asserted-Identity") {
		t.Errorf("P-Asserted-Identity %q sent to an untrusted trunk with Privacy: id", values(m.Message(), "P-Asserted-Identity"))
	}

	if from, _ := m.Message().Header.From(); from.URI.String() != "sip:anonymous@anonymous.invalid" {
		t.Errorf("From %s", from)
	}

	_ = m.Reject(486)

	failed(t, ctx, ac, 486)
}

func TestTrunkIncomingCall(t *testing.T) {
	s, c := newTrunkScene(t, false)
	b := s.caller(1, false, testue.Config{})
	ctx := s.ctx()

	t.Run("answered", func(t *testing.T) {
		c.invite("+15550002")

		bc := <-b.MMTel().Calls()
		m := bc.Invite()

		for _, a := range m.Header.Values("P-Asserted-Identity") {
			if strings.Contains(a, offNet) {
				t.Errorf("P-Asserted-Identity %q of an untrusted trunk reached the phone", a)
			}
		}

		if err := bc.Ring(ctx); err != nil {
			t.Fatalf("Ring: %v", err)
		}

		if err := bc.Answer(ctx); err != nil {
			t.Fatalf("Answer: %v", err)
		}

		e := c.next("established")

		if err := e.s.Bye(ctx, ua.Change{}, nil); err != nil {
			t.Fatal(err)
		}

		ended(t, bc, testue.RemoteBye)
	})

	t.Run("unknown number routed back to the trunk", func(t *testing.T) {
		c.invite("+15559999")

		if e := c.next("failed"); e.res == nil || e.res.StatusCode != 404 {
			t.Fatalf("got %v, want 404", e.res)
		}

		c.none(time.Second)
	})

	t.Run("unknown source", func(t *testing.T) {
		x := newCarrier(t, strangerSIP)
		x.invite("+15550002")
		x.none(2 * time.Second)
	})
}

func TestTrunkIncomingTrusted(t *testing.T) {
	s, c := newTrunkScene(t, true)
	b := s.caller(1, false, testue.Config{})

	o := c.invite("+15550002")

	bc := <-b.MMTel().Calls()

	if got := strings.Join(bc.Invite().Header.Values("P-Asserted-Identity"), ","); !strings.Contains(got, offNet) {
		t.Errorf("P-Asserted-Identity %q, want the trusted trunk's", got)
	}

	_ = o.Cancel(context.Background(), ua.Change{})

	select {
	case <-bc.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the phone's call did not end")
	}

	if e := c.next("failed"); e.res == nil || e.res.StatusCode != 487 {
		t.Fatalf("got %v, want 487", e.res)
	}
}

func values(m sip.Message, name string) string {
	return strings.Join(m.Env().Header.Values(name), ", ")
}

var carrier2SIP = netip.MustParseAddrPort("127.0.0.4:5060")

func (s *scene) trunkState(id string) string {
	s.t.Helper()

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodGet, "http://"+s.srv.APIAddr().String()+"/api/v1/sip-trunks/"+id, nil)
	if err != nil {
		s.t.Fatal(err)
	}

	res, err := apitest.Client(s.srv.APIAddr().String()).Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	var got struct {
		Result struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}

	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		s.t.Fatal(err)
	}

	return got.Result.Status.State
}

func TestTrunkFailover(t *testing.T) {
	s := newIBCFScene(t)
	first, second := newCarrier(t, carrierSIP), newCarrier(t, carrier2SIP)
	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	id1 := s.addTrunk("First", first, false, 0)
	id2 := s.addTrunk("Second", second, false, 0)
	s.post("/api/v1/sip-routes", map[string]any{"prefix": "+33", "trunks": []string{id1, id2}}, nil)

	t.Run("503 then answered", func(t *testing.T) {
		ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

		_ = first.next("invite").req.Reject(503)

		in := second.next("invite").req
		if want := "second." + carrierDomain; in.Message().URI.Host != want {
			t.Errorf("Request-URI %s, want the host %s", in.Message().URI, want)
		}

		answerWithOffer(t, in)

		if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
			t.Fatalf("Wait = %v, %v", res, err)
		}

		second.next("established")

		if err := ac.Bye(ctx); err != nil {
			t.Fatal(err)
		}

		second.next("bye")
	})
}

func TestTrunkDownSkipped(t *testing.T) {
	s := newIBCFScene(t)
	first, second := newCarrier(t, carrierSIP), newCarrier(t, carrier2SIP)
	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	first.options.Store(503)

	id1 := s.addTrunk("First", first, false, 5)
	id2 := s.addTrunk("Second", second, false, 0)
	s.post("/api/v1/sip-routes", map[string]any{"prefix": "+33", "trunks": []string{id1, id2}}, nil)

	deadline := time.Now().Add(20 * time.Second)

	for s.trunkState(id1) != "down" {
		if time.Now().After(deadline) {
			t.Fatalf("the first trunk is %s, want down", s.trunkState(id1))
		}

		time.Sleep(100 * time.Millisecond)
	}

	ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

	_ = second.next("invite").req.Reject(486)

	failed(t, ctx, ac, 486)
	first.none(time.Second)
}

func TestTrunkInboundCIDRInTrustDomain(t *testing.T) {
	s := newIBCFScene(t)

	raw, _ := json.Marshal(map[string]any{
		"name": "Inside", "addresses": []map[string]any{{"host": "127.0.0.9"}}, "inbound_cidrs": []string{"10.0.0.0/24"},
		"domain": carrierDomain, "enabled": true,
	})

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodPost, "http://"+s.srv.APIAddr().String()+"/api/v1/sip-trunks", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	res, err := apitest.Client(s.srv.APIAddr().String()).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST a trunk whose inbound CIDR holds sip.addresses: %d, want 400", res.StatusCode)
	}
}
