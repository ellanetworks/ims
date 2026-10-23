package participating

import (
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
)

// testConfig is bob, a member of fire.
func testConfig() settings.Settings {
	return settings.Settings{
		System: settings.System{
			ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com", ControllingPSI: "sip:mcptt-controlling@mcptt.example.com",
		},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    "mcptt.example.com",
			OnNetwork: settings.OnNetwork{FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)}},
		},
		Users: []settings.User{{MCPTTUserID: "sip:bob@mcptt.example.com", IMPUs: []string{"sip:bob@ims.example.com"}}},
		Groups: []settings.Group{{
			URI: "sip:fire@mcptt.example.com", InviteMembers: true, TNG3: 10 * time.Minute,
			List: settings.List{Entries: []settings.Entry{{URI: "sip:bob@mcptt.example.com"}}},
		}},
	}
}

type source struct{ s *settings.Snapshot }

func (s source) Current() *settings.Snapshot { return s.s }

type bindings []registrar.Binding

func (b bindings) Resolve(*sip.Request, []sip.URI, string) (registrar.Binding, bool) {
	return registrar.Binding{}, false
}

func (b bindings) Bindings(sip.URI) []registrar.Binding { return b }

// affiliated has the clients of the IDs given affiliated to every group.
type affiliated map[string]bool

func (a affiliated) Affiliated(_ time.Time, _ sip.URI, client string, _ sip.URI) bool {
	return a[client]
}

type clock struct{ t time.Time }

func (c clock) Now() time.Time { return c.t }

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// TS 24.379 §10.1.1.3.2 steps 2, 4, 5, §6.3.2.2.5.2: the INVITE of the controlling function without isfocus gets a 403
// with the warning text 104, one for a user without a binding at a client affiliated to the calling group a 404; one
// for a user bound goes to the contact of each of its affiliated clients (§9.2.2.2.11, §7.3.2 step 4a), by client ID,
// with Answer-Mode Auto, its mcptt-info and the session identity of the controlling function as received, the 183
// Unconfirmed of automatic commencement.
func TestTerminate(t *testing.T) {
	snap, err := settings.New(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1e9, 0)
	bound := bindings{
		{
			MCPTTID: mustURI(t, "sip:bob@mcptt.example.com"), ClientID: "c2", IMPU: mustURI(t, "sip:bob@ims.example.com"),
			Expires: now.Add(time.Hour), Contacts: []sip.Address{{URI: mustURI(t, "sip:bob@192.0.2.2:5060")}},
		},
		{
			MCPTTID: mustURI(t, "sip:bob@mcptt.example.com"), ClientID: "c1", IMPU: mustURI(t, "sip:bob@ims.example.com"),
			Expires: now.Add(time.Hour), Contacts: []sip.Address{{URI: mustURI(t, "sip:bob@192.0.2.1:5060")}},
		},
	}

	info, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{
		SessionType: mcpttinfo.SessionPrearranged, RequestURI: mcpttinfo.URI("sip:bob@mcptt.example.com"),
		CallingGroupID: mcpttinfo.URI("sip:fire@mcptt.example.com"),
	}})
	if err != nil {
		t.Fatal(err)
	}

	session := identity.WithFeatures(sip.Address{URI: mustURI(t, "sip:mcptt-controlling@mcptt.example.com;gr=x")}, identity.Features(true))

	invite := func(contact sip.Address) *sip.Request {
		req := sip.NewRequest("INVITE", snap.ParticipatingPSI())
		req.Header.Set("From", "<sip:mcptt-controlling@mcptt.example.com>;tag=1")
		req.Header.Set("Contact", contact.String())
		req.SetBodies(sip.NewBodyPart(mcpttinfo.ContentType, info))

		return req
	}

	for _, tc := range []struct {
		name     string
		contact  sip.Address
		bound    bindings
		affs     affiliated
		code     int
		warning  string
		reaching []string
	}{
		{
			name: "no isfocus", contact: sip.Address{URI: session.URI}, bound: bound, affs: affiliated{"c1": true},
			code: 403, warning: identity.WarnFocusNotAssigned,
		},
		{name: "not bound", contact: session, affs: affiliated{"c1": true}, code: 404},
		{name: "not affiliated", contact: session, bound: bound, affs: affiliated{}, code: 404},
		{name: "both affiliated", contact: session, bound: bound, affs: affiliated{"c1": true, "c2": true}, reaching: []string{"sip:bob@192.0.2.1:5060", "sip:bob@192.0.2.2:5060"}},
		{name: "affiliated", contact: session, bound: bound, affs: affiliated{"c2": true}, reaching: []string{"sip:bob@192.0.2.2:5060"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(Config{Source: source{snap}, Bindings: tc.bound, Affiliations: tc.affs, Router: Direct{}, Clock: clock{now}})
			term := p.Terminate(invite(tc.contact))

			if tc.code != 0 {
				ws, _ := term.Header.Warnings()
				if len(term.Invites) != 0 || term.Code != tc.code || tc.warning != "" && (len(ws) != 1 || ws[0].Text != tc.warning) {
					t.Fatalf("termination %+v", term)
				}

				return
			}

			if len(term.Invites) != len(tc.reaching) || !term.Unconfirmed {
				t.Fatalf("termination %+v", term)
			}

			for i, out := range term.Invites {
				if out.URI.String() != tc.reaching[i] || out.Header.Get("Answer-Mode") != sip.AnswerModeAuto ||
					out.Header.Get("Contact") != session.String() || out.Header.Get("To") != "<sip:bob@ims.example.com>" {
					t.Fatalf("INVITE %v", out)
				}

				if part, ok, _ := out.Content().Find(mcpttinfo.ContentType); !ok || string(part.Body) != string(info) {
					t.Errorf("mcptt-info not copied: %s", part.Body)
				}
			}
		})
	}
}

// TS 24.229 §5.7.3, §5.4.1.7 d; TS 24.379 §6.3.2.2.5.2 step 4: through the IMS, the INVITE to a client goes to the
// IMPU of its binding, routed loosely to the S-CSCF of its third-party REGISTER, without "orig"; one INVITE per
// implicit registration set, whose IMPUs share the registered contacts (TS 24.229 §5.4.1.2.2 step 2a, §5.4.3.3 step
// 8 d), to the IMPU of the binding that expires last; a binding without an S-CSCF is unreachable.
func TestIMSRoute(t *testing.T) {
	snap, err := settings.New(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	r := IMS{Source: source{snap}}
	scscf := mustURI(t, "sip:192.0.2.9:6060;transport=tcp")
	now := time.Unix(1e9, 0)
	sipURI, tel, other := mustURI(t, "sip:bob@ims.example.com"), mustURI(t, "tel:+15555550102"), mustURI(t, "sip:bob5@ims.example.com")
	bs := []registrar.Binding{
		{ClientID: "c1", IMPU: sipURI, IMPUs: []sip.URI{sipURI, tel}, SCSCF: scscf, Expires: now.Add(time.Minute)},
		{ClientID: "c2", IMPU: mustURI(t, "sip:bob@IMS.example.com"), IMPUs: []sip.URI{sipURI}, SCSCF: scscf, Expires: now},
		{ClientID: "c3", IMPU: tel, IMPUs: []sip.URI{tel, sipURI}, SCSCF: scscf, Expires: now.Add(time.Hour)},
		{ClientID: "c4", IMPU: mustURI(t, "sip:bob4@ims.example.com")},
		{ClientID: "c5", IMPU: other, IMPUs: []sip.URI{other}, SCSCF: scscf, Expires: now},
	}

	req := sip.NewRequest("INVITE", snap.ParticipatingPSI())
	req.Header.Set("To", "<sip:bob@mcptt.example.com>")

	out := r.Route(req, bs)
	if len(out) != 2 {
		t.Fatalf("%d INVITEs", len(out))
	}

	for i, want := range []string{"tel:+15555550102", "sip:bob5@ims.example.com"} {
		o := out[i]

		routes, err := o.Header.Routes()
		if err != nil || len(routes) != 1 || routes[0].URI.String() != "sip:192.0.2.9:6060;transport=tcp;lr" ||
			o.URI.String() != want || o.Header.Get("To") != "<"+want+">" {
			t.Errorf("INVITE to %s, To %s, Route %v", o.URI, o.Header.Get("To"), o.Header.Values("Route"))
		}

		if v := o.Header.Get("P-Charging-Vector"); o.Header.ICID() == "" || !strings.HasSuffix(v, ";orig-ioi=mcptt.example.com") {
			t.Errorf("P-Charging-Vector %q", v)
		}

		if s := o.Header.Get("P-Asserted-Service"); s != identity.ICSI {
			t.Errorf("P-Asserted-Service %q", s)
		}
	}

	if out[0].Header.ICID() == out[1].Header.ICID() {
		t.Error("one icid-value for two INVITEs")
	}

	if req.Header.Has("Route") || req.URI.String() != snap.ParticipatingPSI().String() {
		t.Error("the request routed changed")
	}
}

// TS 24.229 §5.7.2, §5.7.3, §5.7.5.1: through the IMS, a response of the server has the icid-value and orig-ioi of
// the request and a type 3 term-ioi, a subsequent request the icid-value of the dialog and a type 3 orig-ioi; in
// direct mode, nothing.
func TestCharged(t *testing.T) {
	c := identity.Charging{ICID: "4a2f", OrigIOI: "ims.example.com"}

	for _, tc := range []struct {
		c        identity.Charging
		response bool
		want     string
	}{
		{c, true, "icid-value=4a2f;orig-ioi=ims.example.com;term-ioi=mcptt.example.com"},
		{identity.Charging{ICID: "4a2f"}, true, "icid-value=4a2f;term-ioi=mcptt.example.com"},
		{c, false, "icid-value=4a2f;orig-ioi=mcptt.example.com"},
		{identity.Charging{}, true, ""},
	} {
		var h sip.Header

		Charged(&h, tc.c, "mcptt.example.com", tc.response)

		if got := h.Get("P-Charging-Vector"); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc, got, tc.want)
		}
	}

	h := sip.Header{{Name: "P-Charging-Vector", Value: "icid-value=1"}}
	Charged(&h, c, "mcptt.example.com", true)

	if h.Get("P-Charging-Vector") != "icid-value=1" {
		t.Error("a P-Charging-Vector replaced")
	}
}
