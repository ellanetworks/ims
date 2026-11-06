package ibcf

import (
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var carrier2Addr = netip.MustParseAddrPort("198.51.100.2:5060")

func withSecondTrunk(s *scene, sameTrunk bool) *party {
	second := newParty(s.t, s.net, "carrier2", carrier2Addr)
	addr := settings.TrunkAddress{Host: carrier2Addr.Addr().String(), Port: int(carrier2Addr.Port()), Transport: settings.SIPTransportUDP}

	s.mu.Lock()
	defer s.mu.Unlock()

	if sameTrunk {
		s.st.Trunks[0].Addresses = append(s.st.Trunks[0].Addresses, addr)
		return second
	}

	t := settings.Trunk{
		ID: "second", Name: "Second", Addresses: []settings.TrunkAddress{addr}, Domain: "second.example", Enabled: true,
		InboundCIDRs: []netip.Prefix{netip.MustParsePrefix("198.51.101.0/24")},
	}
	s.st.Trunks = append(s.st.Trunks, t)
	s.st.SIPRoutes[0].Trunks = append(s.st.SIPRoutes[0].Trunks, t.ID)

	return second
}

func TestExitFailoverToNextTrunk(t *testing.T) {
	s := newScene(t, false)
	s.ibcf.cfg.Metrics = NewMetrics()
	second := withSecondTrunk(s, false)

	s.call("offer-1")

	r := s.carrier.next("invite").req
	first := r.Message()
	_ = r.Reject(503)

	in := second.next("invite").req
	m := in.Message()

	if m.URI.Host != "second.example" || m.Header.CallID() == first.Header.CallID() {
		t.Errorf("INVITE to the second trunk %s with Call-ID %s", m.URI, m.Header.CallID())
	}

	_ = in.Accept(sdpOf("answer-1")...)

	s.ims.until("established")

	if got := testutil.ToFloat64(s.ibcf.cfg.Metrics.failovers.WithLabelValues("carrier")); got != 1 {
		t.Errorf("failovers of the first trunk %v, want 1", got)
	}
}

func TestExitFailoverSameTrunk(t *testing.T) {
	s := newScene(t, false)
	second := withSecondTrunk(s, true)

	s.call("offer-1")

	in := s.carrier.next("invite").req
	first := in.Message()
	_ = in.Reject(503)

	m := second.next("invite").req.Message()

	from1, _ := first.Header.From()
	from2, _ := m.Header.From()

	if m.Header.CallID() != first.Header.CallID() || from1.Tag() != from2.Tag() || m.URI.String() != first.URI.String() {
		t.Errorf("retry to the trunk's next server differs: %s, %s", first, m)
	}
}

func TestExitNoFailoverOn486(t *testing.T) {
	s := newScene(t, false)
	second := withSecondTrunk(s, false)

	s.call("offer-1")

	_ = s.carrier.next("invite").req.Reject(486)

	if e := s.ims.next("failed"); e.res == nil || e.res.StatusCode != 486 {
		t.Fatalf("got %v, want 486", e.res)
	}

	second.none()
}

func TestExitAllTrunksFail(t *testing.T) {
	s := newScene(t, false)
	second := withSecondTrunk(s, false)

	s.call("offer-1")

	_ = s.carrier.next("invite").req.Reject(503)
	_ = second.next("invite").req.Reject(503)

	if e := s.ims.until("failed"); e.res == nil || e.res.StatusCode != 500 {
		t.Fatalf("got %v, want 500", e.res)
	}
}

func TestExitTelParameters(t *testing.T) {
	for _, tc := range []struct {
		uri     string
		trusted bool
		want    string
	}{
		{"tel:+33-6-12;postd=PP22;ext=12;isub=1411;cpc=ordinary", false, "+33612;isub=1411;postd=pp22;ext=12"},
		{"tel:+33612;cpc=ordinary;oli=0", true, "+33612;cpc=ordinary;oli=0"},
		{"tel:+33612;phone-context=+33", true, "+33612"},
	} {
		u, err := sip.ParseURI(tc.uri)
		if err != nil {
			t.Fatal(err)
		}

		if got := subscriber(u, tc.trusted); got != tc.want {
			t.Errorf("subscriber(%s) = %s, want %s", tc.uri, got, tc.want)
		}
	}
}

func TestSessionTimerOnTrunkLeg(t *testing.T) {
	s := newScene(t, false)

	s.call("offer-1")

	m := s.carrier.next("invite").req.Message()
	if values(m, "Session-Expires") != "1800" || !m.Header.HasOptionTag("Supported", sip.OptionTagTimer) {
		t.Fatalf("trunk INVITE Session-Expires %q, Supported %q", values(m, "Session-Expires"), values(m, "Supported"))
	}
}

func TestEntryTrustedReusesTheConnection(t *testing.T) {
	s := newScene(t, true)

	s.dial(s.carrier, "offer-1", f("Contact", "<sip:carrier@192.0.2.77:5099;transport=tls>"))

	in := s.ims.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	s.carrier.next("established")
	s.carrier.next("answer")

	ims := s.ims.next("established")
	_ = ims.s.Bye(t.Context(), ua.Change{}, nil)

	bye := s.carrier.next("bye")
	if f := bye.msg.Env().Flow; f.Transport != sip.TCP || f.Remote != externalAddr {
		t.Fatalf("BYE to the trusted trunk over %s from %s", f.Transport, f.Remote)
	}
}
