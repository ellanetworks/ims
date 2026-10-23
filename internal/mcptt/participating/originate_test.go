package participating

import (
	"context"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// bound resolves every originator to one binding, if set.
type bound struct{ b *registrar.Binding }

func (r bound) Resolve(*sip.Request, []sip.URI, string) (registrar.Binding, bool) {
	if r.b == nil {
		return registrar.Binding{}, false
	}

	return *r.b, true
}

func (bound) Bindings(sip.URI) []registrar.Binding { return nil }

// controlling takes the originations, refusing them with a 480 whose P-Asserted-Identity is the controlling PSI, and
// has the user in the group calls of calls.
type controlling struct {
	got   *Origination
	calls []sip.URI
}

func (c *controlling) Group(o *Origination) {
	c.got = o
	_ = o.Request.Respond(o.Reply(ua.Reply{Code: 480, Header: sip.Header{{Name: "P-Asserted-Identity", Value: "<sip:mcptt-controlling@mcptt.example.com>"}}}))
}

func (c *controlling) GroupCalls(sip.URI) []sip.URI { return c.calls }

const speechOffer = "v=0\r\no=- 1 1 IN IP4 192.0.2.1\r\ns=-\r\nc=IN IP4 192.0.2.1\r\nt=0 0\r\n" +
	"m=audio 30000 RTP/AVP 96\r\na=rtpmap:96 AMR-WB/16000\r\n"

// TS 24.379 §10.1.1.3.1.1, §6.3.2.1.3, §6.3.2.1.5: the INVITE of a client to the participating PSI. Refused: of
// another session type with a 403, of a user without a binding with a 404 and 141 (step 2), without speech with a
// 488 (step 4), beyond N6 with a 486 and 103 (step 5), of a group not configured with a 404 and 142 (steps 6, 7).
// Passed on: to the controlling PSI, with the calling user ID set (step 5), the Accept-Contact and Session-Expires as
// received, no Answer-Mode or Priv-Answer-Mode (step 4), the participating PSI asserted, its Contact with the MCPTT
// feature tag, the ICSI asserted, the SDP offer as received; the controlling function's response relayed with the
// participating PSI asserted. Each response has the P-Charging-Vector of TS 24.229 §5.7.2: the icid-value and
// orig-ioi of the INVITE, a type 3 term-ioi.
func TestOriginate(t *testing.T) {
	snap, err := settings.New(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	limited := testConfig()
	limited.Users[0].MaxSimultaneousCallsN6 = new(1)

	limitedSnap, err := settings.New(limited)
	if err != nil {
		t.Fatal(err)
	}

	n := siptest.NewNet(t)
	addr := n.Addr()
	now := n.Clock().Now()
	bob := &registrar.Binding{
		MCPTTID: mustURI(t, "sip:bob@mcptt.example.com"), ClientID: "c1", IMPU: mustURI(t, "sip:bob@ims.example.com"),
		Expires: now.Add(time.Hour),
	}

	var p *Participating

	a, err := ua.New(ua.Config{
		Transaction: n.Config(transaction.Config{}),
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: addr.Addr().String(), Port: addr.Port()}},
		Prepare: func(req *sip.Request) error {
			tr, to, err := sip.NextHop(req)
			if err != nil {
				return err
			}

			req.Flow = sip.Flow{Transport: tr, Local: addr, Remote: to}
			req.Header.Prepend("Via", sip.NewVia(tr, addr).String())

			return nil
		},
		OnInvite: func(r *ua.Request) ua.Handler {
			p.Originate(r, []sip.URI{bob.IMPU})
			return ua.Handler{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Layer().Serve(addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { n.Await(func() { _ = a.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1) })

	peer := n.Peer()

	invite := func(sessionType mcpttinfo.SessionType, group, offer string) *sip.Request {
		info, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{
			SessionType: sessionType, RequestURI: mcpttinfo.URI(group), ClientID: mcpttinfo.Text("c1"),
		}})
		if err != nil {
			t.Fatal(err)
		}

		req := siptest.NewRequest("INVITE", "sip:mcptt-participating@mcptt.example.com", sip.UDP, peer.Addr())
		req.Header.Set("From", "<sip:bob@ims.example.com>;tag="+sip.NewTag())
		req.Header.Set("To", "<sip:mcptt-participating@mcptt.example.com>")
		req.Header.Set("Call-ID", sip.NewTag()+"@peer")
		req.Header.Set("CSeq", "1 INVITE")
		req.Header.Set("Contact", "<sip:bob@"+peer.Addr().String()+">")
		req.Header.Set("Accept-Contact", "*;+g.3gpp.mcptt;require;explicit")
		req.Header.Set("Session-Expires", "1800")
		req.Header.Set("Answer-Mode", "Auto")
		req.Header.Set("Priv-Answer-Mode", "Auto")
		req.Header.Set("P-Charging-Vector", "icid-value=4a2f;orig-ioi=ims.example.com")
		req.SetBodies(ua.SDP([]byte(offer)), sip.NewBodyPart(mcpttinfo.ContentType, info))

		return req
	}

	final := func(req *sip.Request) *sip.Response {
		peer.Send(sip.UDP, addr, req)

		for {
			res, _ := peer.RecvResponse()
			if res.StatusCode >= 200 {
				ack := siptest.NewRequest("ACK", req.URI.String(), sip.UDP, peer.Addr())
				ack.Header.Set("Via", req.Header.Get("Via"))
				ack.Header.Set("From", req.Header.Get("From"))
				ack.Header.Set("To", res.Header.Get("To"))
				ack.Header.Set("Call-ID", req.Header.Get("Call-ID"))
				ack.Header.Set("CSeq", "1 ACK")
				peer.Send(sip.UDP, addr, ack)

				return res
			}
		}
	}

	const fire = "sip:fire@mcptt.example.com"

	for _, tc := range []struct {
		name         string
		snap         *settings.Snapshot
		binding      *registrar.Binding
		calls        []string
		sessionType  mcpttinfo.SessionType
		group, offer string
		code         int
		warning      string
	}{
		{name: "private", snap: snap, binding: bob, sessionType: mcpttinfo.SessionPrivate, group: fire, offer: speechOffer, code: 403},
		{name: "unbound", snap: snap, sessionType: mcpttinfo.SessionPrearranged, group: fire, offer: speechOffer, code: 404, warning: identity.WarnUserUnknown},
		{
			name: "no speech", snap: snap, binding: bob, sessionType: mcpttinfo.SessionPrearranged, group: fire,
			offer: "v=0\r\no=- 1 1 IN IP4 192.0.2.1\r\ns=-\r\nc=IN IP4 192.0.2.1\r\nt=0 0\r\nm=application 30002 udp MCPTT\r\n", code: 488,
		},
		{
			name: "N6", snap: limitedSnap, binding: bob, calls: []string{"sip:other@mcptt.example.com"}, sessionType: mcpttinfo.SessionPrearranged,
			group: fire, offer: speechOffer, code: 486, warning: identity.WarnTooManyCalls,
		},
		{
			name: "unknown group", snap: snap, binding: bob, sessionType: mcpttinfo.SessionPrearranged, group: "sip:none@mcptt.example.com",
			offer: speechOffer, code: 404, warning: identity.WarnNoControlling,
		},
		{name: "passed on", snap: snap, binding: bob, sessionType: mcpttinfo.SessionPrearranged, group: fire, offer: speechOffer, code: 480},
		{
			name: "N6, the same group", snap: limitedSnap, binding: bob, calls: []string{fire}, sessionType: mcpttinfo.SessionPrearranged,
			group: fire, offer: speechOffer, code: 480,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &controlling{}
			for _, g := range tc.calls {
				c.calls = append(c.calls, mustURI(t, g))
			}

			p = New(Config{Source: source{tc.snap}, Bindings: bound{tc.binding}, Controlling: c, Clock: n.Clock()})

			res := final(invite(tc.sessionType, tc.group, tc.offer))
			if res.StatusCode != tc.code {
				t.Fatalf("response %d", res.StatusCode)
			}

			if v := res.Header.Get("P-Charging-Vector"); v != "icid-value=4a2f;orig-ioi=ims.example.com;term-ioi=mcptt.example.com" {
				t.Errorf("P-Charging-Vector %q", v)
			}

			if tc.warning != "" {
				if ws, _ := res.Header.Warnings(); len(ws) != 1 || ws[0].Text != tc.warning {
					t.Errorf("warnings %v", res.Header.Values("Warning"))
				}
			}

			if tc.code != 480 {
				if c.got != nil {
					t.Error("passed on")
				}

				return
			}

			if pai := res.Header.Values("P-Asserted-Identity"); len(pai) != 1 || pai[0] != "<sip:mcptt-participating@mcptt.example.com>" {
				t.Errorf("response P-Asserted-Identity %v", pai)
			}

			out := c.got.Invite
			if out.URI.String() != "sip:mcptt-controlling@mcptt.example.com" || out.Header.Get("To") != "<sip:mcptt-controlling@mcptt.example.com>" ||
				out.Header.Get("Accept-Contact") != "*;+g.3gpp.mcptt;require;explicit" || out.Header.Get("Session-Expires") != "1800" ||
				out.Header.Get("Answer-Mode") != "" || out.Header.Get("Priv-Answer-Mode") != "" ||
				out.Header.Get("P-Asserted-Identity") != "<sip:mcptt-participating@mcptt.example.com>" ||
				out.Header.Get("P-Asserted-Service") != identity.ICSI {
				t.Errorf("INVITE to the controlling function:\n%s", out)
			}

			if cs, err := out.Header.Contacts(); err != nil || len(cs) != 1 {
				t.Errorf("Contact %v", out.Header.Values("Contact"))
			} else if fs, _ := cs[0].Features(); !fs.Has("g.3gpp.mcptt") {
				t.Errorf("Contact %s", cs[0])
			}

			if b, ok := sessionSDP(out); !ok || string(b) != speechOffer {
				t.Errorf("SDP %q", b)
			}

			info, _, ok := infoOf(out)
			if v, _ := info.Params.CallingUserID.URI(); !ok || v != "sip:bob@mcptt.example.com" {
				t.Errorf("calling user ID %q", v)
			}
		})
	}
}
