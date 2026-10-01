package proxy_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

type ua struct {
	t     *testing.T
	l     *transaction.Layer
	tu    *siptest.TU
	local netip.AddrPort
	tr    sip.Transport
}

func newUA(t *testing.T, addr netip.Addr, tr sip.Transport) *ua {
	t.Helper()

	l, tu := siptest.NewLayer(t, transaction.Config{})

	return &ua{t: t, l: l, tu: tu, local: siptest.ListenLayer(t, l, addr), tr: tr}
}

func (u *ua) contact() string {
	return "<sip:ue@" + u.local.String() + uriParam(u.tr) + ">"
}

func (u *ua) route(req *sip.Request) {
	u.t.Helper()

	tr, to, err := sip.NextHop(req)
	if err != nil {
		u.t.Fatal(err)
	}

	if !req.Header.Has("Via") {
		req.Header.Prepend("Via", sip.NewVia(tr, u.local).String())
	}

	req.Flow = sip.Flow{Transport: tr, Local: u.local, Remote: to}
}

func (u *ua) send(req *sip.Request) *siptest.Responses {
	u.t.Helper()

	u.route(req)

	rs := siptest.NewResponses(u.t)
	if _, err := u.l.Request(req, rs); err != nil {
		u.t.Fatal(err)
	}

	return rs
}

func (u *ua) respond(sr siptest.ServerRequest, code int, extra ...string) *sip.Response {
	u.t.Helper()

	res := sip.NewResponse(sr.Req, code, "")
	_ = res.Header.SetToTag(sr.Tx.ToTag())

	for i := 0; i+1 < len(extra); i += 2 {
		res.Header.Add(extra[i], extra[i+1])
	}

	return res
}

func next(t *testing.T, rs *siptest.Responses, code int) *sip.Response {
	t.Helper()

	for {
		res := rs.Next()
		if res.StatusCode == 100 && code != 100 {
			continue
		}

		if res.StatusCode != code {
			t.Fatalf("got %q, want %d", res.StartLine(), code)
		}

		return res
	}
}

func want(t *testing.T, m sip.Message, method string, vias, recordRoutes, maxForwards int) {
	t.Helper()

	if req, ok := m.(*sip.Request); ok && req.Method != method {
		t.Fatalf("got %s, want %s", req.Method, method)
	}

	h := m.Env().Header
	mf, _ := h.MaxForwards()
	rs, _ := h.Routes()
	rrs, _ := h.RecordRoutes()

	if viaCount(m) != vias || len(rs) != 0 || len(rrs) != recordRoutes || (maxForwards >= 0 && mf != maxForwards) {
		t.Errorf("%s: %d Via, %d Route, %d Record-Route, Max-Forwards %d; want %d, 0, %d, %d:\n%s",
			m.StartLine(), viaCount(m), len(rs), len(rrs), mf, vias, recordRoutes, maxForwards, m)
	}
}

func TestCallThroughTwoProxies(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		forEachTransport(t, func(t *testing.T, tr sip.Transport) {
			p2 := newRouter(t, addr, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}})
			p1 := newRouter(t, addr, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, next: p2.uri() + uriParam(tr) + ";lr"})
			alice := newUA(t, addr, tr)
			bob := newUA(t, addr, tr)

			invite := siptest.NewRequest("INVITE", "sip:bob@"+bob.local.String()+uriParam(tr), tr, alice.local)
			invite.Header.Prepend("Route", "<"+p1.uri()+uriParam(tr)+";lr>")
			invite.Header.Set("Contact", alice.contact())
			invite.Header.Add("Supported", "100rel")
			invite.Header.Del("Via")
			calls := alice.send(invite)

			in := bob.tu.NextRequest()
			want(t, in.Req, "INVITE", 3, 2, 68)

			progress := bob.respond(in, 183, "Require", "100rel", "RSeq", "1", "Contact", bob.contact())

			bd, err := dialog.NewUAS(in.Req, progress)
			if err != nil {
				t.Fatal(err)
			}

			if err := in.Tx.Respond(progress); err != nil {
				t.Fatal(err)
			}

			early := next(t, calls, 183)
			want(t, early, "", 1, 2, -1)

			ad, err := dialog.NewUAC(invite, early)
			if err != nil {
				t.Fatal(err)
			}

			prack, err := ad.NewPrack(early)
			if err != nil {
				t.Fatal(err)
			}

			pracks := alice.send(prack)

			pr := bob.tu.NextRequest()
			want(t, pr.Req, "PRACK", 3, 2, 68)

			if err := bd.ReceiveRequest(pr.Req); err != nil {
				t.Fatal(err)
			}

			if rack, _ := pr.Req.Header.RAck(); rack.RSeq != 1 || rack.Method != "INVITE" {
				t.Errorf("RAck = %+v", rack)
			}

			_ = pr.Tx.Respond(bob.respond(pr, 200))

			want(t, next(t, pracks, 200), "", 1, 0, -1)

			update, err := ad.NewRequest("UPDATE")
			if err != nil {
				t.Fatal(err)
			}

			update.Header.Add("Contact", alice.contact())
			updates := alice.send(update)

			up := bob.tu.NextRequest()
			want(t, up.Req, "UPDATE", 3, 2, 68)

			if err := bd.ReceiveRequest(up.Req); err != nil {
				t.Fatal(err)
			}

			_ = up.Tx.Respond(bob.respond(up, 200, "Contact", bob.contact()))

			if err := ad.ReceiveResponse(next(t, updates, 200)); err != nil {
				t.Fatal(err)
			}

			ok := bob.respond(in, 200, "Contact", bob.contact())
			bd.PrepareResponse(in.Req, ok)

			if err := in.Tx.Respond(ok); err != nil {
				t.Fatal(err)
			}

			answer := next(t, calls, 200)
			want(t, answer, "", 1, 2, -1)

			if err := ad.ReceiveResponse(answer); err != nil {
				t.Fatal(err)
			}

			if ad.State() != dialog.Confirmed || bd.State() != dialog.Confirmed {
				t.Fatalf("dialog states %v and %v", ad.State(), bd.State())
			}

			ack, err := ad.NewAck(invite)
			if err != nil {
				t.Fatal(err)
			}

			alice.route(ack)

			if err := alice.l.SendAck(context.Background(), ack); err != nil {
				t.Fatal(err)
			}

			want(t, bob.tu.NextAck(), "ACK", 3, 0, 68)

			bye, err := bd.NewRequest("BYE")
			if err != nil {
				t.Fatal(err)
			}

			byes := bob.send(bye)

			hangup := alice.tu.NextRequest()
			want(t, hangup.Req, "BYE", 3, 2, 68)

			if err := ad.ReceiveRequest(hangup.Req); err != nil {
				t.Fatal(err)
			}

			_ = hangup.Tx.Respond(alice.respond(hangup, 200))

			want(t, next(t, byes, 200), "", 1, 0, -1)

			if len(ad.RouteSet()) != 2 || len(bd.RouteSet()) != 2 {
				t.Errorf("route sets changed in the dialog: %q and %q", ad.RouteSet(), bd.RouteSet())
			}

			calls.None(quiet)
			alice.tu.None(quiet)
			bob.tu.None(quiet)
		})
	})
}
