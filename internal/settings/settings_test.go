package settings

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	saved []any
	err   error
}

func (f *fakeStore) save(v any) error {
	if f.err != nil {
		return f.err
	}

	f.saved = append(f.saved, v)

	return nil
}

func (f *fakeStore) UpdateOperator(_ context.Context, o Operator) error { return f.save(o) }

func (f *fakeStore) CreatePeer(_ context.Context, p Peer) error { return f.save(p) }

func (f *fakeStore) UpdatePeer(_ context.Context, p Peer) error { return f.save(p) }

func (f *fakeStore) DeletePeer(_ context.Context, id string) error { return f.save(id) }

func (f *fakeStore) UpdateRoute(_ context.Context, r Route) error { return f.save(r) }

func (f *fakeStore) UpdatePolicy(_ context.Context, p Policy) error { return f.save(p) }

func (f *fakeStore) UpdateCallRecords(_ context.Context, c CallRecords) error { return f.save(c) }

func validSettings() Settings {
	return Settings{
		Operator: validOperator(), Routes: []Route{{Application: ApplicationCx}, {Application: ApplicationRx}},
		Policy: Policy{Interface: PolicyNone}, CallRecords: CallRecords{RetentionDays: 90},
	}
}

func validOperator() Operator {
	return Operator{MCC: "001", MNC: "01", Numbering: Numbering{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"}}
}

func TestValidate(t *testing.T) {
	tests := map[string]func(*Operator){
		"short mcc":            func(o *Operator) { o.MCC = "01" },
		"non-digit mcc":        func(o *Operator) { o.MCC = "0a1" },
		"short mnc":            func(o *Operator) { o.MNC = "1" },
		"long mnc":             func(o *Operator) { o.MNC = "0001" },
		"missing country code": func(o *Operator) { o.Numbering.CountryCode = "" },
		"country code with 0":  func(o *Operator) { o.Numbering.CountryCode = "01" },
		"long country code":    func(o *Operator) { o.Numbering.CountryCode = "1234" },
		"long prefix":          func(o *Operator) { o.Numbering.InternationalPrefix = "00000" },
		"non-digit prefix":     func(o *Operator) { o.Numbering.NationalPrefix = "0x" },
		"same prefixes":        func(o *Operator) { o.Numbering.NationalPrefix, o.Numbering.InternationalPrefix = "0", "0" },
	}

	if err := validSettings().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	noPrefixes := validOperator()
	noPrefixes.Numbering.NationalPrefix, noPrefixes.Numbering.InternationalPrefix = "", ""

	if err := noPrefixes.Validate(); err != nil {
		t.Fatalf("Validate without prefixes: %v", err)
	}

	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			o := validOperator()
			edit(&o)

			if err := o.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestDerivedNames(t *testing.T) {
	o := Operator{MCC: "310", MNC: "410"}

	for _, tt := range []struct{ name, got, want string }{
		{"home domain", o.HomeDomain(), "ims.mnc410.mcc310.3gppnetwork.org"},
		{"Diameter host", o.DiameterHost(), "ims.ims.mnc410.mcc310.3gppnetwork.org"},
		{"Diameter realm", o.DiameterRealm(), "ims.mnc410.mcc310.3gppnetwork.org"},
		{"S-CSCF name", o.SCSCFName(5082), "sip:scscf.ims.mnc410.mcc310.3gppnetwork.org:5082"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}

	want := []string{
		"ims.mnc410.mcc310.3gppnetwork.org", "pcscf.ims.mnc410.mcc310.3gppnetwork.org", "scscf.ims.mnc410.mcc310.3gppnetwork.org",
	}
	if got := o.SIPAliases(); !reflect.DeepEqual(got, want) {
		t.Errorf("SIPAliases = %v, want %v", got, want)
	}
}

func TestHomeDomain(t *testing.T) {
	for _, tt := range []struct{ mcc, mnc, want string }{
		{"001", "01", "ims.mnc001.mcc001.3gppnetwork.org"},
		{"234", "15", "ims.mnc015.mcc234.3gppnetwork.org"},
		{"310", "410", "ims.mnc410.mcc310.3gppnetwork.org"},
	} {
		if got := (Operator{MCC: tt.mcc, MNC: tt.mnc}).HomeDomain(); got != tt.want {
			t.Errorf("home domain of %s/%s = %q, want %q", tt.mcc, tt.mnc, got, tt.want)
		}
	}
}

func TestSameIdentity(t *testing.T) {
	o := validOperator()

	renumbered := o
	renumbered.Numbering.CountryCode = "44"

	if !o.SameIdentity(renumbered) {
		t.Fatal("a numbering change changed the identity")
	}

	// A 2-digit MNC is padded, so 01 and 001 are the same network.
	if padded := (Operator{MCC: "001", MNC: "001"}); !o.SameIdentity(padded) {
		t.Fatal("MNC 01 and 001 differ")
	}

	if other := (Operator{MCC: "001", MNC: "02"}); o.SameIdentity(other) {
		t.Fatal("another MNC kept the identity")
	}
}

func TestLiveUpdate(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings(), nil)
	changed := live.Changed()

	next := validOperator()
	next.MNC = "02"

	if err := live.UpdateOperator(t.Context(), next); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	select {
	case <-changed:
	default:
		t.Fatal("Changed not closed by the update")
	}

	if got := live.Get().Operator; got != next {
		t.Fatalf("Get = %+v, want %+v", got, next)
	}

	if len(store.saved) != 1 || store.saved[0] != any(next) {
		t.Fatalf("saved = %+v, want %+v", store.saved, next)
	}
}

func TestLiveRejectsInvalid(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings(), nil)

	bad := validOperator()
	bad.MCC = "1"

	if err := live.UpdateOperator(t.Context(), bad); err == nil {
		t.Fatal("expected an error")
	}

	if len(store.saved) != 0 || live.Get().Operator != validOperator() {
		t.Fatal("an invalid operator was saved or applied")
	}
}

func TestLiveKeepsSettingsWhenTheStoreFails(t *testing.T) {
	store := &fakeStore{err: errors.New("disk full")}
	live := NewLive(store, validSettings(), nil)
	changed := live.Changed()

	next := validOperator()
	next.MNC = "02"

	if err := live.UpdateOperator(t.Context(), next); err == nil {
		t.Fatal("expected an error")
	}

	if live.Get().Operator != validOperator() {
		t.Fatal("an unsaved operator was applied")
	}

	select {
	case <-changed:
		t.Fatal("Changed closed by a failed update")
	default:
	}
}

func hss() Peer {
	return Peer{
		Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Address: netip.MustParseAddr("10.0.0.10"), Port: 3868,
		Transport: TransportTCP, Applications: []Application{ApplicationCx}, Priority: DefaultPriority,
	}
}

func pcrf() Peer {
	return Peer{
		Host: "pcrf.epc.mnc001.mcc001.3gppnetwork.org", Address: netip.MustParseAddr("10.0.0.11"), Port: 3868,
		Transport: TransportSCTP, Applications: []Application{ApplicationRx}, Priority: DefaultPriority,
	}
}

func TestPeerValidate(t *testing.T) {
	tests := map[string]struct {
		edit func(*Peer)
		want string
	}{
		"host not a domain":   {func(p *Peer) { p.Host = "hss ims" }, "host must be a domain name"},
		"negative priority":   {func(p *Peer) { p.Priority = -1 }, "priority must be between 0 and 65535"},
		"high priority":       {func(p *Peer) { p.Priority = 65536 }, "priority must be between 0 and 65535"},
		"no address":          {func(p *Peer) { p.Address = netip.Addr{} }, "address is required"},
		"unspecified address": {func(p *Peer) { p.Address = netip.IPv6Unspecified() }, "address must be a specific IPv4 or IPv6 address"},
		"port out of range":   {func(p *Peer) { p.Port = 70000 }, "port must be between 1 and 65535"},
		"unknown transport":   {func(p *Peer) { p.Transport = "udp" }, "transport must be tcp or sctp"},
		"no applications":     {func(p *Peer) { p.Applications = nil }, "applications must list cx, rx or both"},
		"unknown application": {func(p *Peer) { p.Applications = []Application{"gx"} }, `applications must list cx, rx or both, not "gx"`},
		"twice an application": {
			func(p *Peer) { p.Applications = []Application{ApplicationCx, ApplicationCx} }, "applications lists cx twice",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := hss()
			tt.edit(&p)

			if err := p.Validate(); err == nil || err.Error() != tt.want || !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPolicyValidate(t *testing.T) {
	for _, p := range []Policy{
		{Interface: PolicyNone},
		{Interface: PolicyRx},
		{Interface: PolicyN5, PCFURI: "http://10.0.0.13:7777"},
		{Interface: PolicyN5, PCFURI: "https://pcf.example.org/prefix"},
	} {
		if err := p.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", p, err)
		}
	}

	for _, p := range []Policy{
		{},
		{Interface: "gx"},
		{Interface: PolicyNone, PCFURI: "http://10.0.0.13:7777"},
		{Interface: PolicyN5},
		{Interface: PolicyN5, PCFURI: "ftp://10.0.0.13"},
		{Interface: PolicyN5, PCFURI: "http://10.0.0.13:7777/?a=b"},
	} {
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(%+v) = %v, want it invalid", p, err)
		}
	}
}

func TestLivePeers(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings(), nil)

	created, err := live.CreatePeer(t.Context(), hss())
	if err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	if _, err := uuid.Parse(created.ID); err != nil || created.ID[14] != '7' {
		t.Fatalf("ID = %q, want a UUIDv7", created.ID)
	}

	if got := live.Get().Serving(ApplicationCx); len(got) != 1 || !got[0].equal(created) {
		t.Fatalf("cx peers = %+v, want %+v", got, created)
	}

	if _, err := live.CreatePeer(t.Context(), hss()); !errors.Is(err, ErrConflict) {
		t.Fatalf("second peer with the same host = %v, want a conflict", err)
	}

	primary := hss()
	primary.Host = "hss2.ims.mnc001.mcc001.3gppnetwork.org"
	primary.Priority = 1

	primary, err = live.CreatePeer(t.Context(), primary)
	if err != nil {
		t.Fatalf("second cx peer: %v", err)
	}

	if got := live.Get().Serving(ApplicationCx); len(got) != 2 || got[0].ID != primary.ID || got[1].ID != created.ID {
		t.Fatalf("cx peers = %+v, want the lower priority first", got)
	}

	if err := live.DeletePeer(t.Context(), primary.ID); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}

	moved := created
	moved.Address = netip.MustParseAddr("10.0.0.20")

	if err := live.UpdatePeer(t.Context(), moved); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}

	if p := live.Get().Serving(ApplicationCx); p[0].Address != moved.Address {
		t.Fatalf("address = %s, want %s", p[0].Address, moved.Address)
	}

	unknown := moved
	unknown.ID = "0192"

	if err := live.UpdatePeer(t.Context(), unknown); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePeer of an unknown peer = %v, want not found", err)
	}

	if err := live.DeletePeer(t.Context(), "0192"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeletePeer of an unknown peer = %v, want not found", err)
	}

	if err := live.DeletePeer(t.Context(), created.ID); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}

	if len(live.Get().Peers) != 0 {
		t.Fatalf("peers = %+v, want none", live.Get().Peers)
	}
}

func TestLivePeersWithoutHost(t *testing.T) {
	live := NewLive(&fakeStore{}, validSettings(), nil)

	noHost := hss()
	noHost.Host = ""

	if _, err := live.CreatePeer(t.Context(), noHost); err != nil {
		t.Fatalf("CreatePeer without a host: %v", err)
	}

	if _, err := live.CreatePeer(t.Context(), hss()); err != nil {
		t.Fatalf("a peer with a host on the same address: %v", err)
	}

	sctp := noHost
	sctp.Transport = TransportSCTP

	if _, err := live.CreatePeer(t.Context(), sctp); err != nil {
		t.Fatalf("a peer without a host on the same address over another transport: %v", err)
	}

	if _, err := live.CreatePeer(t.Context(), noHost); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second peer without a host on the same address = %v, want a conflict", err)
	}
}

func TestLivePolicy(t *testing.T) {
	live := NewLive(&fakeStore{}, validSettings(), nil)

	if err := live.UpdatePolicy(t.Context(), Policy{Interface: PolicyRx}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rx without a peer = %v, want a conflict", err)
	}

	p, err := live.CreatePeer(t.Context(), pcrf())
	if err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	if err := live.UpdatePolicy(t.Context(), Policy{Interface: PolicyRx}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	if err := live.DeletePeer(t.Context(), p.ID); !errors.Is(err, ErrConflict) || err.Error() != "rx requires a Diameter peer serving rx" {
		t.Fatalf("DeletePeer of the rx peer = %v, want a conflict", err)
	}

	second := pcrf()
	second.Host = "pcrf2.epc.mnc001.mcc001.3gppnetwork.org"

	if _, err := live.CreatePeer(t.Context(), second); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	if err := live.DeletePeer(t.Context(), p.ID); err != nil {
		t.Fatalf("DeletePeer of an rx peer with another left: %v", err)
	}

	p = live.Get().Serving(ApplicationRx)[0]

	cx := p
	cx.Applications = []Application{ApplicationCx}

	if err := live.UpdatePeer(t.Context(), cx); !errors.Is(err, ErrConflict) {
		t.Fatalf("taking rx from the policy's peer = %v, want a conflict", err)
	}
}

func TestLiveCheck(t *testing.T) {
	live := NewLive(&fakeStore{}, validSettings(), func(s Settings) error {
		if s.Policy.Interface == PolicyN5 {
			return Invalidf("n5 needs n5 in the configuration file")
		}

		return nil
	})

	err := live.UpdatePolicy(t.Context(), Policy{Interface: PolicyN5, PCFURI: "http://10.0.0.13:7777"})
	if !errors.Is(err, ErrInvalid) || err.Error() != "n5 needs n5 in the configuration file" {
		t.Fatalf("UpdatePolicy = %v, want the check's error", err)
	}

	if live.Get().Policy.Interface != PolicyNone {
		t.Fatal("a rejected policy was applied")
	}
}

func TestLiveRoutes(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings(), nil)

	if got := live.Get().Realm(ApplicationCx); got != "ims.mnc001.mcc001.3gppnetwork.org" {
		t.Fatalf("default cx realm = %q, want the home domain", got)
	}

	r := Route{Application: ApplicationRx, Realm: "epc.mnc001.mcc001.3gppnetwork.org"}
	if err := live.UpdateRoute(t.Context(), r); err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}

	if got := live.Get().Realm(ApplicationRx); got != r.Realm || !reflect.DeepEqual(store.saved, []any{r}) {
		t.Fatalf("rx realm = %q, saved %v", got, store.saved)
	}

	if got := live.Get().Realm(ApplicationCx); got != "ims.mnc001.mcc001.3gppnetwork.org" {
		t.Fatalf("cx realm = %q after an rx change", got)
	}

	if err := live.UpdateRoute(t.Context(), Route{Application: ApplicationRx, Realm: "bad realm"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid realm = %v, want invalid", err)
	}

	if err := live.UpdateRoute(t.Context(), Route{Application: "gx"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown application = %v, want not found", err)
	}

	if err := live.UpdateRoute(t.Context(), Route{Application: ApplicationRx}); err != nil || live.Get().Realm(ApplicationRx) != "ims.mnc001.mcc001.3gppnetwork.org" {
		t.Fatalf("reset to the home domain = %v", err)
	}
}

func TestSameCore(t *testing.T) {
	s := validSettings()
	s.Peers = []Peer{hss()}

	renumbered := s
	renumbered.Operator.Numbering.CountryCode = "44"

	if !s.SameCore(renumbered) {
		t.Fatal("a numbering change needs another core")
	}

	moved := s
	moved.Peers = []Peer{hss()}
	moved.Peers[0].Port = 3869

	if s.SameCore(moved) {
		t.Fatal("a moved peer keeps the core")
	}

	rerouted := s
	rerouted.Routes = []Route{{Application: ApplicationCx, Realm: "example.org"}, {Application: ApplicationRx}}

	if s.SameCore(rerouted) {
		t.Fatal("another realm keeps the core")
	}

	n5 := s
	n5.Policy = Policy{Interface: PolicyN5, PCFURI: "http://10.0.0.13:7777"}

	if s.SameCore(n5) {
		t.Fatal("another policy function keeps the core")
	}

	retained := s
	retained.CallRecords.RetentionDays = 7

	if !s.SameCore(retained) {
		t.Fatal("a retention change needs another core")
	}
}

func TestCallRecordsValidate(t *testing.T) {
	for _, days := range []int{1, 90, 3650} {
		if err := (CallRecords{RetentionDays: days}).Validate(); err != nil {
			t.Fatalf("Validate(%d days): %v", days, err)
		}
	}

	for _, days := range []int{-1, 0, 3651} {
		if err := (CallRecords{RetentionDays: days}).Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Validate(%d days) = %v, want invalid", days, err)
		}
	}

	if got := (CallRecords{RetentionDays: 2}).Retention(); got != 48*time.Hour {
		t.Fatalf("Retention = %v, want 48h", got)
	}
}

func TestLiveCallRecords(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings(), nil)
	changed := live.Changed()

	if err := live.UpdateCallRecords(t.Context(), CallRecords{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no retention = %v, want invalid", err)
	}

	c := CallRecords{RetentionDays: 30}
	if err := live.UpdateCallRecords(t.Context(), c); err != nil {
		t.Fatalf("UpdateCallRecords: %v", err)
	}

	if live.Get().CallRecords != c || !reflect.DeepEqual(store.saved, []any{c}) {
		t.Fatalf("call records = %+v, saved %v; want %+v saved once", live.Get().CallRecords, store.saved, c)
	}

	select {
	case <-changed:
	default:
		t.Fatal("a retention change is not announced")
	}
}

func TestSameNode(t *testing.T) {
	s := validSettings()
	s.Peers = []Peer{hss()}

	for name, edit := range map[string]func(*Settings){
		"another address":   func(t *Settings) { t.Peers[0].Address = netip.MustParseAddr("10.0.0.20") },
		"another host":      func(t *Settings) { t.Peers[0].Host = "hss2.ims.mnc001.mcc001.3gppnetwork.org" },
		"another priority":  func(t *Settings) { t.Peers[0].Priority = 1 },
		"more applications": func(t *Settings) { t.Peers[0].Applications = []Application{ApplicationCx, ApplicationRx} },
		"another peer": func(t *Settings) {
			t.Peers = append(t.Peers, func() Peer { p := hss(); p.Host = "hss2.example.org"; return p }())
		},
		"another realm": func(t *Settings) {
			t.Routes = []Route{{Application: ApplicationCx, Realm: "example.org"}, {Application: ApplicationRx}}
		},
	} {
		changed := s
		changed.Peers = []Peer{hss()}
		edit(&changed)

		if !s.SameNode(changed) || s.SameCore(changed) {
			t.Errorf("%s: needs another core, want the node reconfigured", name)
		}
	}

	for name, edit := range map[string]func(*Settings){
		"another transport": func(t *Settings) { t.Peers[0].Transport = TransportSCTP },
		"added transport":   func(t *Settings) { t.Peers = append(t.Peers, pcrf()) },
		"another identity":  func(t *Settings) { t.Operator.MNC = "02" },
		"another policy":    func(t *Settings) { t.Policy = Policy{Interface: PolicyN5, PCFURI: "http://10.0.0.13:7777"} },
	} {
		changed := s
		changed.Peers = []Peer{hss()}
		edit(&changed)

		if s.SameNode(changed) {
			t.Errorf("%s: reconfigures the node, want another core", name)
		}
	}
}
