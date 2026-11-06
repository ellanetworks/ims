package scscf

import (
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/icscf"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

const (
	quiet     = 50 * time.Millisecond
	offNetTel = "tel:+16617629239"
)

func icscfRoute(t *testing.T, sh *sessionHarness, req *sip.Request) sip.URI {
	t.Helper()

	routes, err := req.Header.Routes()
	if err != nil || len(routes) != 1 || routes[0].URI.HostPort() != sh.icscf.Addr().String() || !routes[0].URI.IsLooseRouter() {
		t.Fatalf("Route = %v, %v, want the I-CSCF alone", req.Header.Values("Route"), err)
	}

	return routes[0].URI
}

func TestBreakoutAllowed(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{"tel URI", offNetTel + ";cpc=ordinary"},
		{"SIP URI with user=phone", "sip:+16617629239@" + homeDomain + ";user=phone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := newSessionHarness(t)

			req := sh.originating("INVITE", tt.target, "<"+testMSISDN+">")
			req.Header.Add("P-Charging-Vector", `icid-value=1234;access-network-charging-info="x";orig-ioi=home`)
			sh.orig.Send(sip.UDP, sh.scscf, req)

			got, f := sh.icscf.RecvRequest()

			switch rr, _ := got.Header.RecordRoutes(); {
			case got.URI.String() != tt.target:
				t.Fatalf("Request-URI = %s, want %s unmodified", got.URI, tt.target)
			case !icscfRoute(t, sh, got).Params.Has(icscf.BreakoutParam):
				t.Fatalf("Route = %v, want the I-CSCF allowed to break out", got.Header.Values("Route"))
			case len(rr) != 1 || rr[0].URI.User != rrOriginating:
				t.Fatalf("Record-Route = %v, want the S-CSCF's", got.Header.Values("Record-Route"))
			case got.Header.Get("P-Charging-Vector") != "icid-value=1234;orig-ioi=home":
				t.Fatalf("P-Charging-Vector = %q, want it without access-network-charging-info", got.Header.Get("P-Charging-Vector"))
			case got.Header.Has("P-Served-User"):
				t.Fatal("P-Served-User forwarded to the I-CSCF")
			}

			reply(t, sh.icscf, got, f, 486, "carrier")

			if res := final(t, sh.orig); res.StatusCode != 486 {
				t.Fatalf("caller got %q, want 486", res.StartLine())
			}
		})
	}
}

func TestBreakoutNotAllowed(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		anci   bool
	}{
		{name: "short number under a country code", target: "tel:+1911"},
		{name: "SIP URI", target: "sip:+16617629239@" + homeDomain, anci: true},
		{name: "MESSAGE", method: "MESSAGE", target: offNetTel},
		{name: "SUBSCRIBE", method: "SUBSCRIBE", target: offNetTel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := newSessionHarness(t)

			method := tt.method
			if method == "" {
				method = "INVITE"
			}

			req := sh.originating(method, tt.target, "<"+testMSISDN+">")
			req.Header.Add("P-Charging-Vector", `icid-value=1234;access-network-charging-info="x"`)
			sh.orig.Send(sip.UDP, sh.scscf, req)

			got, _ := sh.icscf.RecvRequest()
			if got.Method != method {
				t.Fatalf("I-CSCF got %s, want %s", got.Method, method)
			}

			if icscfRoute(t, sh, got).Params.Has(icscf.BreakoutParam) {
				t.Fatalf("Route = %v, want no breakout", got.Header.Values("Route"))
			}

			if anci := strings.Contains(got.Header.Get("P-Charging-Vector"), "access-network-charging-info"); anci != tt.anci {
				t.Fatalf("P-Charging-Vector = %q, want access-network-charging-info %v", got.Header.Get("P-Charging-Vector"), tt.anci)
			}
		})
	}
}

// TS 24.229 clause 5.4.3.3
func TestRetargetedBreakout(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		term := cx.SessionCaseTerminatingRegistered

		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued,
			methodSPT("INVITE"), cx.ServicePointTrigger{SessionCase: &term})}
	})

	req := sh.terminating("INVITE", testTel)
	req.Header.Add("P-Charging-Vector", `icid-value=1234;access-network-charging-info="x"`)
	sh.icscf.Send(sip.UDP, sh.scscf, req)

	got, _ := as.RecvRequest()

	diverted := proxied(got, as, false)
	diverted.URI = sip.URI{Scheme: "tel", User: "+16617629239"}
	as.Send(sip.UDP, sh.scscf, diverted)

	if res, _ := sh.icscf.RecvResponse(); res.StatusCode != 100 {
		t.Fatalf("I-CSCF got %q, want 100", res.StartLine())
	}

	out, _ := sh.icscf.RecvRequest()

	switch {
	case out.Method != "INVITE" || out.URI.String() != offNetTel:
		t.Fatalf("I-CSCF got:\n%s\nwant the retargeted INVITE", out)
	case !icscfRoute(t, sh, out).Params.Has(icscf.BreakoutParam):
		t.Fatalf("Route = %v, want the I-CSCF allowed to break out", out.Header.Values("Route"))
	case strings.Contains(out.Header.Get("P-Charging-Vector"), "access-network-charging-info"):
		t.Fatalf("P-Charging-Vector = %q, want it without access-network-charging-info", out.Header.Get("P-Charging-Vector"))
	}

	sh.term.RecvNone(quiet)
}

func TestWithoutAccessNetworkChargingInfo(t *testing.T) {
	for in, want := range map[string]string{
		"icid-value=1;access-network-charging-info=x;orig-ioi=a": "icid-value=1;orig-ioi=a",
		"icid-value=1": "icid-value=1",
		"icid-value=1; Access-Network-Charging-Info=\"a;b\"": "icid-value=1",
	} {
		var h sip.Header
		h.Set("P-Charging-Vector", in)
		withoutAccessNetworkChargingInfo(&h)

		if got := h.Get("P-Charging-Vector"); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
