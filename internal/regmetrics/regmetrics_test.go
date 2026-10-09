package regmetrics

import (
	"maps"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/prometheus/client_golang/prometheus"
)

func register(contact, expires string) *sip.Request {
	req := siptest.NewRequest("REGISTER", "sip:ims.example.org", sip.UDP, netip.MustParseAddrPort("127.0.0.2:6000"))
	if contact == "" {
		req.Header.Del("Contact")
	} else {
		req.Header.Set("Contact", contact)
	}

	if expires != "" {
		req.Header.Set("Expires", expires)
	}

	return req
}

func TestDeregistration(t *testing.T) {
	for _, tt := range []struct {
		name             string
		contact, expires string
		want             bool
	}{
		{"star", "*", "0", true},
		{"Expires 0", "<sip:ue@127.0.0.2>", "0", true},
		{"contact expires 0", "<sip:ue@127.0.0.2>;expires=0", "600", true},
		{"one contact kept", "<sip:a@127.0.0.2>;expires=0, <sip:b@127.0.0.2>", "600", false},
		{"Expires 600", "<sip:ue@127.0.0.2>", "600", false},
		{"no expiry", "<sip:ue@127.0.0.2>", "", false},
		{"no contact", "", "0", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := deregistration(register(tt.contact, tt.expires)); got != tt.want {
				t.Fatalf("deregistration = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestAnswered(t *testing.T) {
	r := New()

	req := register("<sip:ue@127.0.0.2>", "600")
	for _, code := range []int{100, 200, 401, 403, 480} {
		r.Answered(req, sip.NewResponse(req, code, ""), false)
	}

	r.Answered(req, sip.NewResponse(req, 403, ""), true)

	// Neither a de-registration nor another method is an attempt to register.
	dereg := register("*", "0")
	r.Answered(dereg, sip.NewResponse(dereg, 500, ""), false)

	invite := siptest.NewRequest("INVITE", "sip:bob@ims.example.org", sip.UDP, netip.MustParseAddrPort("127.0.0.2:6000"))
	r.Answered(invite, sip.NewResponse(invite, 500, ""), false)

	var nilRegistrations *Registrations
	nilRegistrations.Answered(req, sip.NewResponse(req, 200, ""), false)

	if got, want := values(t, r), map[string]float64{Accept: 1, AuthFailure: 1, Reject: 2}; !maps.Equal(got, want) {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
}

func values(t *testing.T, r *Registrations) map[string]float64 {
	t.Helper()

	reg := prometheus.NewRegistry()
	reg.MustRegister(r.Collectors()...)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]float64{}
	for _, m := range families[0].GetMetric() {
		got[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
	}

	return got
}
