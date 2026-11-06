package settings

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func telnyx() Trunk {
	return Trunk{
		Name:              "Telnyx",
		Addresses:         []TrunkAddress{{Host: "sip.telnyx.com", Port: 5060, Transport: SIPTransportTCP}},
		InboundCIDRs:      []netip.Prefix{netip.MustParsePrefix("192.76.120.10/32"), netip.MustParsePrefix("64.16.250.10/32")},
		Domain:            "sip.telnyx.com",
		Trusted:           true,
		KeepaliveInterval: DefaultKeepaliveInterval,
		Enabled:           true,
	}
}

func sbc() Trunk {
	return Trunk{
		Name:              "SBC",
		Addresses:         []TrunkAddress{{Host: "2001:db8::5", Port: 5080, Transport: SIPTransportUDP}},
		InboundCIDRs:      []netip.Prefix{netip.MustParsePrefix("2001:db8::/64")},
		Domain:            "sbc.example.org",
		KeepaliveInterval: time.Minute,
	}
}

func TestTrunkValidate(t *testing.T) {
	noKeepalive := sbc()
	noKeepalive.KeepaliveInterval = 0

	byAddress := telnyx()
	byAddress.Domain = "2001:db8::5"

	byDNS := telnyx()
	byDNS.Trusted = false
	byDNS.Addresses = []TrunkAddress{
		{Host: "sip.telnyx.com"},
		{Host: "sip.telnyx.com", Transport: SIPTransportTCP},
		{Host: "sip.telnyx.com", Transport: SIPTransportUDP},
		{Host: "sip.telnyx.com", Port: 5060},
		{Host: "sip.telnyx.com", Port: 5060, Transport: SIPTransportTCP},
		{Host: "192.0.2.1"},
		{Host: "2001:db8::1", Transport: SIPTransportTCP},
	}

	trustedByDNS := telnyx()
	trustedByDNS.Addresses = []TrunkAddress{{Host: "sip.telnyx.com"}, {Host: "192.0.2.1", Port: 5060}}

	for _, tr := range []Trunk{telnyx(), sbc(), noKeepalive, byAddress, byDNS, trustedByDNS} {
		if err := tr.Validate(); err != nil {
			t.Fatalf("Validate(%s): %v", tr.Name, err)
		}
	}

	tests := map[string]struct {
		edit func(*Trunk)
		want string
	}{
		"no name":     {func(t *Trunk) { t.Name = "" }, "name must be 1 to 64 printable characters, without leading or trailing spaces"},
		"padded name": {func(t *Trunk) { t.Name = " Telnyx" }, "name must be 1 to 64 printable characters, without leading or trailing spaces"},
		"negative keepalive": {
			func(t *Trunk) { t.KeepaliveInterval = -time.Second }, "keepalive_interval must be 0, for no keepalive, or between 5 and 3600 seconds",
		},
		"mapped domain": {
			func(t *Trunk) { t.Domain = "::ffff:192.0.2.1" }, "domain must be a domain name or a specific IPv4 or IPv6 address",
		},
		"control name": {func(t *Trunk) { t.Name = "Tel\nnyx" }, "name must be 1 to 64 printable characters, without leading or trailing spaces"},
		"long name":    {func(t *Trunk) { t.Name = strings.Repeat("a", 65) }, "name must be 1 to 64 printable characters, without leading or trailing spaces"},
		"no addresses": {func(t *Trunk) { t.Addresses = nil }, "addresses must list at least one address"},
		"trusted over udp": {
			func(t *Trunk) {
				t.Addresses = append(t.Addresses, TrunkAddress{Host: "192.0.2.1", Transport: SIPTransportUDP})
			},
			"addresses must not use udp when trusted is set, not 192.0.2.1 over udp",
		},
		"no domain":     {func(t *Trunk) { t.Domain = "" }, "domain must be a domain name or a specific IPv4 or IPv6 address"},
		"domain is URI": {func(t *Trunk) { t.Domain = "sip:sip.telnyx.com" }, "domain must be a domain name or a specific IPv4 or IPv6 address"},
		"short keepalive": {
			func(t *Trunk) { t.KeepaliveInterval = 4 * time.Second }, "keepalive_interval must be 0, for no keepalive, or between 5 and 3600 seconds",
		},
		"long keepalive": {
			func(t *Trunk) { t.KeepaliveInterval = time.Hour + time.Second }, "keepalive_interval must be 0, for no keepalive, or between 5 and 3600 seconds",
		},
		"fractional keepalive": {
			func(t *Trunk) { t.KeepaliveInterval = 5500 * time.Millisecond }, "keepalive_interval must be 0, for no keepalive, or between 5 and 3600 seconds",
		},
		"bad host": {
			func(t *Trunk) { t.Addresses[0].Host = "sip telnyx" },
			`addresses must have a domain name or a specific IPv4 or IPv6 address as host, not "sip telnyx"`,
		},
		"unspecified host": {
			func(t *Trunk) { t.Addresses[0].Host = "0.0.0.0" },
			`addresses must have a domain name or a specific IPv4 or IPv6 address as host, not "0.0.0.0"`,
		},
		"mapped host": {
			func(t *Trunk) { t.Addresses[0].Host = "::ffff:10.0.0.1" },
			`addresses must have a domain name or a specific IPv4 or IPv6 address as host, not "::ffff:10.0.0.1"`,
		},
		"negative port":  {func(t *Trunk) { t.Addresses[0].Port = -1 }, "addresses must have a port between 1 and 65535, or none"},
		"high port":      {func(t *Trunk) { t.Addresses[0].Port = 65536 }, "addresses must have a port between 1 and 65535, or none"},
		"sctp transport": {func(t *Trunk) { t.Addresses[0].Transport = "sctp" }, `addresses must have udp, tcp or tls as transport, or none, not "sctp"`},
		"host twice for DNS": {
			func(t *Trunk) { t.Addresses = []TrunkAddress{{Host: "sip.telnyx.com"}, {Host: "SIP.telnyx.com"}} },
			"addresses lists SIP.telnyx.com twice",
		},
		"IPv6 host and port twice": {
			func(t *Trunk) {
				t.Addresses = []TrunkAddress{{Host: "2001:db8::5", Port: 5060}, {Host: "2001:db8::5", Port: 5060}}
			},
			"addresses lists [2001:db8::5]:5060 twice",
		},
		"address twice": {
			func(t *Trunk) {
				t.Addresses = append(t.Addresses, TrunkAddress{Host: "SIP.telnyx.com", Port: 5060, Transport: SIPTransportTCP})
			},
			"addresses lists SIP.telnyx.com:5060 over tcp twice",
		},
		"invalid CIDR": {
			func(t *Trunk) { t.InboundCIDRs = []netip.Prefix{{}} }, "inbound_cidrs must be IPv4 or IPv6 networks, not invalid Prefix",
		},
		"host bits set": {
			func(t *Trunk) { t.InboundCIDRs = []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24")} },
			"inbound_cidrs must be network addresses: 10.0.0.0/24, not 10.0.0.1/24",
		},
		"every address": {
			func(t *Trunk) { t.InboundCIDRs = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")} },
			"inbound_cidrs must not cover every address, as 0.0.0.0/0 does",
		},
		"mapped CIDR": {
			func(t *Trunk) { t.InboundCIDRs = []netip.Prefix{netip.MustParsePrefix("::ffff:10.0.0.0/120")} },
			"inbound_cidrs must be IPv4 or IPv6 networks, not ::ffff:10.0.0.0/120",
		},
		"overlapping CIDRs": {
			func(t *Trunk) {
				t.InboundCIDRs = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16"), netip.MustParsePrefix("10.0.1.0/24")}
			},
			"inbound_cidrs 10.0.0.0/16 and 10.0.1.0/24 overlap",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tr := telnyx()
			tt.edit(&tr)

			if err := tr.Validate(); err == nil || err.Error() != tt.want || !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSIPHostname(t *testing.T) {
	for _, host := range []string{"sip.telnyx.com", "localhost", "sip-1.example.org", "1sip.example.org", "a1.b2", "x"} {
		if !isSIPHost(host) {
			t.Errorf("isSIPHost(%q) = false, want true", host)
		}
	}

	for _, host := range []string{
		"", "10.0.0.256", "192.168.1.010", "123", "sip.123", "sip.example.4g", "-sip.example.org", "sip-.example.org",
		"sip..example.org", "sip.example.org.", "sip_1.example.org", "sip.example.org:5060", "[2001:db8::5]",
		"::ffff:10.0.0.1", "0.0.0.0", "::", "fe80::1%eth0",
	} {
		if isSIPHost(host) {
			t.Errorf("isSIPHost(%q) = true, want false", host)
		}
	}

	for _, domain := range []string{"10.0.0.256", "192.168.1.010"} {
		tr := telnyx()
		tr.Domain = domain

		if err := tr.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate with domain %q = %v, want it invalid", domain, err)
		}
	}
}

func TestKeepaliveSeconds(t *testing.T) {
	for seconds, want := range map[int]time.Duration{0: 0, 5: 5 * time.Second, 3600: time.Hour} {
		if got, err := KeepaliveSeconds(seconds); err != nil || got != want {
			t.Errorf("KeepaliveSeconds(%d) = %v, %v; want %v", seconds, got, err, want)
		}
	}

	for _, seconds := range []int{-1, 3601, 36028797018963998} {
		if _, err := KeepaliveSeconds(seconds); !errors.Is(err, ErrInvalid) {
			t.Errorf("KeepaliveSeconds(%d) = %v, want it invalid", seconds, err)
		}
	}
}

func TestSIPRouteValidate(t *testing.T) {
	for _, prefix := range []string{"", "+1", "+33", "+123456789012345"} {
		if err := (SIPRoute{Prefix: prefix, Trunks: []string{"a"}}).Validate(); err != nil {
			t.Errorf("Validate(%q): %v", prefix, err)
		}
	}

	badPrefix := "prefix must be + followed by 1 to 15 digits, not starting with 0, or empty for the default route"

	for _, tt := range []struct {
		route SIPRoute
		want  string
	}{
		{SIPRoute{Prefix: "+", Trunks: []string{"a"}}, badPrefix},
		{SIPRoute{Prefix: "1", Trunks: []string{"a"}}, badPrefix},
		{SIPRoute{Prefix: "+0", Trunks: []string{"a"}}, badPrefix},
		{SIPRoute{Prefix: "+044", Trunks: []string{"a"}}, badPrefix},
		{SIPRoute{Prefix: "+1a", Trunks: []string{"a"}}, badPrefix},
		{SIPRoute{Prefix: "+1-555", Trunks: []string{"a"}}, badPrefix},
		{SIPRoute{Prefix: "+1234567890123456", Trunks: []string{"a"}}, badPrefix},
		{SIPRoute{Prefix: "+1"}, "trunks must list at least one SIP trunk"},
		{SIPRoute{Prefix: "+1", Trunks: []string{"a", "b", "a"}}, "trunks lists a twice"},
	} {
		if err := tt.route.Validate(); err == nil || err.Error() != tt.want || !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(%+v) = %v, want %q", tt.route, err, tt.want)
		}
	}
}

func TestLiveTrunks(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings(), nil)

	created, err := live.CreateTrunk(t.Context(), telnyx())
	if err != nil {
		t.Fatalf("CreateTrunk: %v", err)
	}

	if len(created.ID) != 36 || created.ID[14] != '7' {
		t.Fatalf("ID = %q, want a UUIDv7", created.ID)
	}

	if got, ok := live.Get().Trunk(created.ID); !ok || got.Name != "Telnyx" {
		t.Fatalf("Trunk = %+v %v", got, ok)
	}

	if _, err := live.CreateTrunk(t.Context(), sbc()); err != nil {
		t.Fatalf("second trunk: %v", err)
	}

	sameName := sbc()
	sameName.Name = "TELNYX"
	sameName.InboundCIDRs = nil

	if _, err := live.CreateTrunk(t.Context(), sameName); !errors.Is(err, ErrConflict) ||
		err.Error() != "A SIP trunk already has name TELNYX" {
		t.Fatalf("same name = %v, want a conflict", err)
	}

	overlapping := sbc()
	overlapping.Name = "Overlapping"
	overlapping.InboundCIDRs = []netip.Prefix{netip.MustParsePrefix("192.76.120.0/24")}

	if _, err := live.CreateTrunk(t.Context(), overlapping); !errors.Is(err, ErrConflict) ||
		err.Error() != "Inbound CIDR 192.76.120.0/24 overlaps 192.76.120.10/32 of SIP trunk Telnyx" {
		t.Fatalf("overlapping CIDR = %v, want a conflict", err)
	}

	if n := len(live.Get().Trunks); n != 2 {
		t.Fatalf("%d trunks, want 2", n)
	}

	disabled := created
	disabled.Enabled = false

	if err := live.UpdateTrunk(t.Context(), disabled); err != nil {
		t.Fatalf("UpdateTrunk: %v", err)
	}

	if got, _ := live.Get().Trunk(created.ID); got.Enabled {
		t.Fatalf("trunk = %+v, want it disabled", got)
	}

	unknown := disabled
	unknown.ID = "0192"

	if err := live.UpdateTrunk(t.Context(), unknown); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateTrunk of an unknown trunk = %v, want not found", err)
	}

	if err := live.DeleteTrunk(t.Context(), "0192"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteTrunk of an unknown trunk = %v, want not found", err)
	}

	if err := live.DeleteTrunk(t.Context(), created.ID); err != nil {
		t.Fatalf("DeleteTrunk: %v", err)
	}

	if _, ok := live.Get().Trunk(created.ID); ok {
		t.Fatal("the deleted trunk remains")
	}
}

func TestLiveTrunkGuard(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings(), nil)

	trusted := netip.MustParsePrefix("192.76.120.0/24")

	live.SetTrunkGuard(func(p netip.Prefix) error {
		if p.Overlaps(trusted) {
			return fmt.Errorf("inbound_cidrs %s overlaps the trust domain", p)
		}

		return nil
	})

	if _, err := live.CreateTrunk(t.Context(), telnyx()); !errors.Is(err, ErrInvalid) ||
		err.Error() != "inbound_cidrs 192.76.120.10/32 overlaps the trust domain" {
		t.Fatalf("CreateTrunk inside the trust domain = %v, want invalid", err)
	}

	created, err := live.CreateTrunk(t.Context(), sbc())
	if err != nil {
		t.Fatalf("CreateTrunk outside the trust domain: %v", err)
	}

	moved := created
	moved.InboundCIDRs = []netip.Prefix{netip.MustParsePrefix("192.76.0.0/16")}

	if err := live.UpdateTrunk(t.Context(), moved); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpdateTrunk into the trust domain = %v, want invalid", err)
	}

	if got, _ := live.Get().Trunk(created.ID); got.InboundCIDRs[0] != sbc().InboundCIDRs[0] {
		t.Fatalf("inbound_cidrs = %v, want them unchanged", got.InboundCIDRs)
	}

	kinded := Invalidf("kept as is")

	live.SetTrunkGuard(func(netip.Prefix) error { return kinded })

	if err := live.UpdateTrunk(t.Context(), created); !errors.Is(err, kinded) {
		t.Fatalf("UpdateTrunk = %v, want the guard's own invalid error", err)
	}

	live.SetTrunkGuard(nil)

	if _, err := live.CreateTrunk(t.Context(), telnyx()); err != nil {
		t.Fatalf("CreateTrunk without a guard: %v", err)
	}
}

func TestLiveSIPRoutes(t *testing.T) {
	live := NewLive(&fakeStore{}, validSettings(), nil)

	primary, err := live.CreateTrunk(t.Context(), telnyx())
	if err != nil {
		t.Fatal(err)
	}

	backup, err := live.CreateTrunk(t.Context(), sbc())
	if err != nil {
		t.Fatal(err)
	}

	def, err := live.CreateSIPRoute(t.Context(), SIPRoute{Trunks: []string{primary.ID, backup.ID}})
	if err != nil {
		t.Fatalf("default route: %v", err)
	}

	if len(def.ID) != 36 || def.ID[14] != '7' {
		t.Fatalf("ID = %q, want a UUIDv7", def.ID)
	}

	us, err := live.CreateSIPRoute(t.Context(), SIPRoute{Prefix: "+1", Trunks: []string{backup.ID}})
	if err != nil {
		t.Fatalf("route +1: %v", err)
	}

	for _, tt := range []struct {
		route SIPRoute
		kind  error
		want  string
	}{
		{SIPRoute{Trunks: []string{backup.ID}}, ErrConflict, "A default SIP route already exists"},
		{SIPRoute{Prefix: "+1", Trunks: []string{primary.ID}}, ErrConflict, "A SIP route already has prefix +1"},
		{SIPRoute{Prefix: "+44", Trunks: []string{"0192"}}, ErrInvalid, "trunks lists 0192, which is not a SIP trunk"},
	} {
		if _, err := live.CreateSIPRoute(t.Context(), tt.route); !errors.Is(err, tt.kind) || err.Error() != tt.want {
			t.Errorf("CreateSIPRoute(%+v) = %v, want %q", tt.route, err, tt.want)
		}
	}

	if err := live.DeleteTrunk(t.Context(), primary.ID); !errors.Is(err, ErrConflict) ||
		err.Error() != "SIP trunk Telnyx is used by the default SIP route" {
		t.Fatalf("DeleteTrunk in use = %v, want a conflict", err)
	}

	if err := live.DeleteTrunk(t.Context(), backup.ID); !errors.Is(err, ErrConflict) ||
		err.Error() != "SIP trunk SBC is used by the default SIP route" {
		t.Fatalf("DeleteTrunk in use = %v, want a conflict", err)
	}

	us.Prefix = "+1555"
	us.Trunks = []string{primary.ID, backup.ID}

	if err := live.UpdateSIPRoute(t.Context(), us); err != nil {
		t.Fatalf("UpdateSIPRoute: %v", err)
	}

	if got, _ := live.Get().SIPRoute(us.ID); got.Prefix != "+1555" || len(got.Trunks) != 2 || got.Trunks[0] != primary.ID {
		t.Fatalf("route = %+v, want +1555 to the primary then the backup", got)
	}

	if err := live.UpdateSIPRoute(t.Context(), SIPRoute{ID: "0192", Trunks: []string{primary.ID}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateSIPRoute of an unknown route = %v, want not found", err)
	}

	def.Trunks = []string{backup.ID}

	if err := live.UpdateSIPRoute(t.Context(), def); err != nil {
		t.Fatalf("UpdateSIPRoute: %v", err)
	}

	if err := live.DeleteSIPRoute(t.Context(), us.ID); err != nil {
		t.Fatalf("DeleteSIPRoute: %v", err)
	}

	if err := live.DeleteSIPRoute(t.Context(), us.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSIPRoute = %v, want not found", err)
	}

	if err := live.DeleteTrunk(t.Context(), primary.ID); err != nil {
		t.Fatalf("DeleteTrunk no longer in use: %v", err)
	}
}

func TestLiveTrunksKeepSettingsWhenTheStoreFails(t *testing.T) {
	store := &fakeStore{err: errors.New("disk full")}
	live := NewLive(store, validSettings(), nil)

	if _, err := live.CreateTrunk(t.Context(), telnyx()); err == nil {
		t.Fatal("CreateTrunk succeeded with a failing store")
	}

	if n := len(live.Get().Trunks); n != 0 {
		t.Fatalf("%d trunks, want none", n)
	}
}
