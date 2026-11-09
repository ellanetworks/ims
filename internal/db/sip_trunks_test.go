package db

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
)

func testCertificate(t *testing.T, cn string, ca bool) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: ca, BasicConstraintsValid: ca,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return der
}

func testTrunks(t *testing.T) (settings.Trunk, settings.Trunk) {
	t.Helper()

	pinned := testCertificate(t, "sip2.telnyx.com", false)

	telnyx := settings.Trunk{
		ID: "0192a000-0000-7000-8000-000000000011", Name: "Telnyx",
		Addresses: []settings.TrunkAddress{
			{Host: "sip.telnyx.com", Port: 5061, Transport: settings.SIPTransportTLS},
			{Host: "sip.telnyx.com", Port: 5060, Transport: settings.SIPTransportTCP},
		},
		InboundCIDRs: []netip.Prefix{
			netip.MustParsePrefix("192.76.120.10/32"), netip.MustParsePrefix("64.16.250.10/32"),
		},
		Domain: "sip.telnyx.com", Trusted: true, KeepaliveInterval: 30 * time.Second, Enabled: true,
		TLS: settings.TrunkTLS{
			CACertificates: [][]byte{testCertificate(t, "Telnyx CA", true), testCertificate(t, "Telnyx CA 2", true)},
			Pins:           [][sha256.Size]byte{sha256.Sum256(pinned), {1, 2, 3}},
			Identities:     []string{"sip.telnyx.com", "telnyx.com"},
		},
	}
	sbc := settings.Trunk{
		ID: "0192a000-0000-7000-8000-000000000012", Name: "SBC",
		Addresses:    []settings.TrunkAddress{{Host: "2001:db8::5", Port: 5080, Transport: settings.SIPTransportUDP}},
		InboundCIDRs: []netip.Prefix{netip.MustParsePrefix("2001:db8::/64")},
		Domain:       "sbc.example.org", KeepaliveInterval: time.Minute,
	}

	return telnyx, sbc
}

func TestSIPTrunks(t *testing.T) {
	d := openTestDB(t)
	telnyx, sbc := testTrunks(t)

	for _, tr := range []settings.Trunk{sbc, telnyx} {
		if err := d.CreateTrunk(t.Context(), tr); err != nil {
			t.Fatalf("CreateTrunk: %v", err)
		}
	}

	sbc.Addresses = []settings.TrunkAddress{
		{Host: "sbc.example.org"},
		{Host: "sbc.example.org", Transport: settings.SIPTransportTCP},
		{Host: "10.0.0.5", Port: 5070},
	}
	sbc.InboundCIDRs = nil
	sbc.Enabled, sbc.Trusted, sbc.KeepaliveInterval = true, true, time.Hour

	if err := d.UpdateTrunk(t.Context(), sbc); err != nil {
		t.Fatalf("UpdateTrunk: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if want := []settings.Trunk{telnyx, sbc}; !reflect.DeepEqual(got.Trunks, want) {
		t.Fatalf("trunks = %+v, want %+v in the order of their IDs", got.Trunks, want)
	}

	if err := got.Validate(); err != nil {
		t.Fatalf("the stored settings are invalid: %v", err)
	}

	if err := d.DeleteTrunk(t.Context(), telnyx.ID); err != nil {
		t.Fatalf("DeleteTrunk: %v", err)
	}

	if err := d.DeleteTrunk(t.Context(), telnyx.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteTrunk = %v, want %v", err, ErrNotFound)
	}

	if err := d.UpdateTrunk(t.Context(), telnyx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateTrunk of a deleted trunk = %v, want %v", err, ErrNotFound)
	}

	var n int
	if err := d.writer.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM sip_trunk_addresses WHERE trunk_id = ?) +
		(SELECT count(*) FROM sip_trunk_inbound_cidrs WHERE trunk_id = ?) +
		(SELECT count(*) FROM sip_trunk_tls_ca_certificates WHERE trunk_id = ?) +
		(SELECT count(*) FROM sip_trunk_tls_pins WHERE trunk_id = ?) +
		(SELECT count(*) FROM sip_trunk_tls_identities WHERE trunk_id = ?)`,
		telnyx.ID, telnyx.ID, telnyx.ID, telnyx.ID, telnyx.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows of the deleted trunk remain (%v)", n, err)
	}
}

func TestSIPTrunkTLS(t *testing.T) {
	d := openTestDB(t)
	telnyx, _ := testTrunks(t)

	if err := d.CreateTrunk(t.Context(), telnyx); err != nil {
		t.Fatalf("CreateTrunk: %v", err)
	}

	stored := func() settings.Trunk {
		t.Helper()

		got, err := d.GetSettings(t.Context())
		if err != nil || len(got.Trunks) != 1 {
			t.Fatalf("GetSettings = %+v, %v, want one trunk", got.Trunks, err)
		}

		if err := got.Validate(); err != nil {
			t.Fatalf("the stored settings are invalid: %v", err)
		}

		return got.Trunks[0]
	}

	if got := stored(); !reflect.DeepEqual(got, telnyx) {
		t.Fatalf("trunk = %+v, want %+v", got, telnyx)
	}

	telnyx.Addresses = []settings.TrunkAddress{{Host: "sip.telnyx.com", Transport: settings.SIPTransportTLS}}
	telnyx.TLS = settings.TrunkTLS{
		Required: true,
		Pins:     telnyx.TLS.Pins[1:],
	}

	if err := d.UpdateTrunk(t.Context(), telnyx); err != nil {
		t.Fatalf("UpdateTrunk: %v", err)
	}

	if got := stored(); !reflect.DeepEqual(got, telnyx) {
		t.Fatalf("trunk = %+v, want %+v", got, telnyx)
	}

	telnyx.Addresses = []settings.TrunkAddress{{Host: "sip.telnyx.com", Transport: settings.SIPTransportTCP}}
	telnyx.TLS = settings.TrunkTLS{}

	if err := d.UpdateTrunk(t.Context(), telnyx); err != nil {
		t.Fatalf("UpdateTrunk without TLS: %v", err)
	}

	if got := stored(); !reflect.DeepEqual(got, telnyx) {
		t.Fatalf("trunk = %+v, want %+v without TLS", got, telnyx)
	}

	var n int
	if err := d.writer.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM sip_trunk_tls_ca_certificates) +
		(SELECT count(*) FROM sip_trunk_tls_pins) + (SELECT count(*) FROM sip_trunk_tls_identities)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d TLS rows remain (%v)", n, err)
	}

	telnyx.TLS.CACertificates = [][]byte{make([]byte, settings.MaxTrunkCertificateDER+1)}

	if err := d.UpdateTrunk(t.Context(), telnyx); !errors.Is(err, ErrConstraintCheck) {
		t.Fatalf("UpdateTrunk with an oversized certificate = %v, want a CHECK failure", err)
	}

	telnyx.TLS = settings.TrunkTLS{Pins: [][sha256.Size]byte{{1}, {1}}}

	if err := d.UpdateTrunk(t.Context(), telnyx); !errors.Is(err, ErrConstraintUnique) {
		t.Fatalf("UpdateTrunk with a pin twice = %v, want a UNIQUE failure", err)
	}
}

func TestSIPTrunkUniqueName(t *testing.T) {
	d := openTestDB(t)
	telnyx, sbc := testTrunks(t)

	if err := d.CreateTrunk(t.Context(), telnyx); err != nil {
		t.Fatal(err)
	}

	sbc.Name = "TELNYX"

	if err := d.CreateTrunk(t.Context(), sbc); err == nil {
		t.Fatal("a second trunk named TELNYX was stored")
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Trunks) != 1 {
		t.Fatalf("trunks = %+v, want Telnyx alone", got.Trunks)
	}
}

func TestSIPRoutes(t *testing.T) {
	d := openTestDB(t)
	telnyx, sbc := testTrunks(t)

	for _, tr := range []settings.Trunk{telnyx, sbc} {
		if err := d.CreateTrunk(t.Context(), tr); err != nil {
			t.Fatal(err)
		}
	}

	def := settings.SIPRoute{ID: "0192a000-0000-7000-8000-000000000021", Trunks: []string{telnyx.ID, sbc.ID}}
	us := settings.SIPRoute{ID: "0192a000-0000-7000-8000-000000000022", Prefix: "+1", Trunks: []string{sbc.ID}}

	for _, r := range []settings.SIPRoute{us, def} {
		if err := d.CreateSIPRoute(t.Context(), r); err != nil {
			t.Fatalf("CreateSIPRoute: %v", err)
		}
	}

	if err := d.CreateSIPRoute(t.Context(), settings.SIPRoute{
		ID: "0192a000-0000-7000-8000-000000000023", Prefix: "+1", Trunks: []string{telnyx.ID},
	}); err == nil {
		t.Fatal("a second route of prefix +1 was stored")
	}

	us.Prefix = "+1555"
	us.Trunks = []string{sbc.ID, telnyx.ID}

	if err := d.UpdateSIPRoute(t.Context(), us); err != nil {
		t.Fatalf("UpdateSIPRoute: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if want := []settings.SIPRoute{def, us}; !reflect.DeepEqual(got.SIPRoutes, want) {
		t.Fatalf("routes = %+v, want %+v", got.SIPRoutes, want)
	}

	if err := d.DeleteTrunk(t.Context(), telnyx.ID); err == nil {
		t.Fatal("a trunk in use was deleted")
	}

	if err := d.DeleteSIPRoute(t.Context(), def.ID); err != nil {
		t.Fatalf("DeleteSIPRoute: %v", err)
	}

	if err := d.DeleteSIPRoute(t.Context(), def.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSIPRoute = %v, want %v", err, ErrNotFound)
	}

	if err := d.UpdateSIPRoute(t.Context(), def); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateSIPRoute of a deleted route = %v, want %v", err, ErrNotFound)
	}

	if err := d.UpdateSIPRoute(t.Context(), settings.SIPRoute{ID: us.ID, Prefix: "+1", Trunks: []string{"0192"}}); err == nil {
		t.Fatal("a route to an unknown trunk was stored")
	}

	if got, err := d.GetSettings(t.Context()); err != nil || !reflect.DeepEqual(got.SIPRoutes, []settings.SIPRoute{us}) {
		t.Fatalf("routes = %+v (%v), want +1555 unchanged by the failed update", got.SIPRoutes, err)
	}

	if err := d.DeleteSIPRoute(t.Context(), us.ID); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteTrunk(t.Context(), telnyx.ID); err != nil {
		t.Fatalf("DeleteTrunk no longer in use: %v", err)
	}
}

func TestSIPTrunkWithoutKeepalive(t *testing.T) {
	d := openTestDB(t)
	_, sbc := testTrunks(t)
	sbc.KeepaliveInterval = 0
	sbc.Domain = "2001:db8::5"

	if err := d.CreateTrunk(t.Context(), sbc); err != nil {
		t.Fatalf("CreateTrunk without keepalive: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got.Trunks, []settings.Trunk{sbc}) {
		t.Fatalf("trunks = %+v, want %+v", got.Trunks, []settings.Trunk{sbc})
	}

	for _, keepalive := range []time.Duration{time.Second, 3601 * time.Second} {
		sbc.KeepaliveInterval = keepalive

		if err := d.UpdateTrunk(t.Context(), sbc); !errors.Is(err, ErrConstraintCheck) {
			t.Errorf("UpdateTrunk with keepalive %v = %v, want a CHECK failure", keepalive, err)
		}
	}
}

func TestSIPTrunkAddressChecks(t *testing.T) {
	d := openTestDB(t)
	telnyx, _ := testTrunks(t)

	for _, a := range []settings.TrunkAddress{
		{Host: "sip.telnyx.com", Transport: "sctp"},
		{Host: "sip.telnyx.com", Port: 70000},
	} {
		telnyx.Addresses = []settings.TrunkAddress{a}

		if err := d.CreateTrunk(t.Context(), telnyx); !errors.Is(err, ErrConstraintCheck) {
			t.Errorf("CreateTrunk with %+v = %v, want a CHECK failure", a, err)
		}
	}
}

func TestSIPRouteTrunkForeignKey(t *testing.T) {
	d := openTestDB(t)
	telnyx, _ := testTrunks(t)

	if err := d.CreateTrunk(t.Context(), telnyx); err != nil {
		t.Fatal(err)
	}

	route := settings.SIPRoute{ID: "0192a000-0000-7000-8000-000000000031", Prefix: "+1", Trunks: []string{telnyx.ID}}
	if err := d.CreateSIPRoute(t.Context(), route); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteTrunk(t.Context(), telnyx.ID); !errors.Is(err, ErrConstraintForeignKey) {
		t.Fatalf("DeleteTrunk in use = %v, want a foreign key failure", err)
	}

	if got, err := d.GetSettings(t.Context()); err != nil || len(got.Trunks) != 1 || len(got.SIPRoutes) != 1 {
		t.Fatalf("settings = %+v (%v), want the trunk and its route kept", got, err)
	}
}

func TestLiveSIPTrunksOnTheDatabase(t *testing.T) {
	d := openTestDB(t)

	initial, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	live := settings.NewLive(d, initial, nil)
	telnyx, sbc := testTrunks(t)

	telnyx, err = live.CreateTrunk(t.Context(), telnyx)
	if err != nil {
		t.Fatalf("CreateTrunk: %v", err)
	}

	sbc.KeepaliveInterval = 0
	sbc.Domain = "2001:db8::5"

	sbc, err = live.CreateTrunk(t.Context(), sbc)
	if err != nil {
		t.Fatalf("CreateTrunk without keepalive and with an IPv6 domain: %v", err)
	}

	def, err := live.CreateSIPRoute(t.Context(), settings.SIPRoute{Trunks: []string{telnyx.ID, sbc.ID}})
	if err != nil {
		t.Fatalf("CreateSIPRoute: %v", err)
	}

	us, err := live.CreateSIPRoute(t.Context(), settings.SIPRoute{Prefix: "+1", Trunks: []string{sbc.ID}})
	if err != nil {
		t.Fatalf("CreateSIPRoute: %v", err)
	}

	telnyx.Enabled = false
	telnyx.KeepaliveInterval = time.Hour
	telnyx.InboundCIDRs = telnyx.InboundCIDRs[:1]
	telnyx.TLS.Pins = telnyx.TLS.Pins[1:]
	telnyx.TLS.CACertificates = telnyx.TLS.CACertificates[:1]

	if err := live.UpdateTrunk(t.Context(), telnyx); err != nil {
		t.Fatalf("UpdateTrunk: %v", err)
	}

	us.Prefix = "+1555"
	us.Trunks = []string{sbc.ID, telnyx.ID}

	if err := live.UpdateSIPRoute(t.Context(), us); err != nil {
		t.Fatalf("UpdateSIPRoute: %v", err)
	}

	if err := live.DeleteTrunk(t.Context(), telnyx.ID); !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("DeleteTrunk in use = %v, want a conflict", err)
	}

	if err := live.DeleteSIPRoute(t.Context(), def.ID); err != nil {
		t.Fatalf("DeleteSIPRoute: %v", err)
	}

	stored, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if want := live.Get(); !reflect.DeepEqual(stored.Trunks, want.Trunks) || !reflect.DeepEqual(stored.SIPRoutes, want.SIPRoutes) {
		t.Fatalf("stored = %+v %+v, want %+v %+v", stored.Trunks, stored.SIPRoutes, want.Trunks, want.SIPRoutes)
	}

	if len(stored.Trunks) != 2 || len(stored.SIPRoutes) != 1 || stored.Trunks[1].KeepaliveInterval != 0 ||
		len(stored.Trunks[0].TLS.CACertificates) != 1 || len(stored.Trunks[0].TLS.Pins) != 1 {
		t.Fatalf("stored = %+v %+v, want two trunks, the SBC without keepalive, and +1555", stored.Trunks, stored.SIPRoutes)
	}

	if err := live.DeleteSIPRoute(t.Context(), us.ID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{telnyx.ID, sbc.ID} {
		if err := live.DeleteTrunk(t.Context(), id); err != nil {
			t.Fatalf("DeleteTrunk: %v", err)
		}
	}

	if stored, err := d.GetSettings(t.Context()); err != nil || len(stored.Trunks) != 0 || len(stored.SIPRoutes) != 0 {
		t.Fatalf("stored = %+v (%v), want no trunks and no routes", stored, err)
	}
}
