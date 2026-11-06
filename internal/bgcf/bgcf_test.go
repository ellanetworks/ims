package bgcf

import (
	"errors"
	"slices"
	"testing"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
)

type states map[string]settings.TrunkState

func (s states) TrunkStatus(id string) settings.TrunkStatus {
	return settings.TrunkStatus{State: s[id]}
}

func trunk(id string, enabled bool) settings.Trunk {
	return settings.Trunk{ID: id, Name: id, Enabled: enabled}
}

func newBGCF(routes []settings.SIPRoute, trunks []settings.Trunk, st settings.TrunkStates) *BGCF {
	s := settings.Settings{Trunks: trunks, SIPRoutes: routes}
	return New(func() settings.Settings { return s }, st)
}

func ids(trunks []settings.Trunk) []string {
	out := make([]string, 0, len(trunks))
	for _, t := range trunks {
		out = append(out, t.ID)
	}

	return out
}

func TestTrunks(t *testing.T) {
	trunks := []settings.Trunk{
		trunk("a", true), trunk("b", true), trunk("c", true), trunk("off", false), trunk("down", true), trunk("unknown", true),
	}

	routes := []settings.SIPRoute{
		{ID: "r0", Prefix: "", Trunks: []string{"a"}},
		{ID: "r1", Prefix: "+1", Trunks: []string{"b", "a"}},
		{ID: "r2", Prefix: "+1661", Trunks: []string{"c", "b"}},
		{ID: "r3", Prefix: "+44", Trunks: []string{"off", "down", "b", "unknown"}},
		{ID: "r4", Prefix: "+33", Trunks: []string{"off", "down"}},
	}

	st := states{"a": settings.TrunkUp, "b": settings.TrunkUp, "c": settings.TrunkUp, "down": settings.TrunkDown, "unknown": settings.TrunkUnknown}

	tests := []struct {
		name   string
		number string
		want   []string
		err    error
	}{
		{"longest prefix", "+16617629239", []string{"c", "b"}, nil},
		{"shorter overlapping prefix", "+15551234567", []string{"b", "a"}, nil},
		{"default route", "+4915112345678", []string{"a"}, nil},
		{"disabled and down trunks skipped, order kept", "+442079460000", []string{"b", "unknown"}, nil},
		{"no usable trunk", "+33612345678", nil, ErrNoUsableTrunk},
		{"not a global number", "16617629239", nil, nil},
		{"empty number", "", nil, nil},
		{"plus alone", "+", nil, nil},
		{"emergency code under a country code", "+1911", nil, nil},
		{"short code under a country code", "+44112", nil, nil},
		{"six digits", "+441234", nil, nil},
		{"seven digits", "+4412345", []string{"b", "unknown"}, nil},
		{"fifteen digits", "+491511234567890", []string{"a"}, nil},
		{"sixteen digits", "+4915112345678901", nil, nil},
		{"twenty digits", "+12345678901234567890", nil, nil},
		{"leading zero", "+0123456789", nil, nil},
	}

	b := newBGCF(routes, trunks, st)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := b.Trunks(tt.number)

			if tt.want == nil {
				want := tt.err
				if want == nil {
					want = ErrNoRoute
				}

				if !errors.Is(err, want) {
					t.Fatalf("Trunks(%q) = %v, %v, want %v", tt.number, ids(got), err, want)
				}

				return
			}

			if err != nil || !slices.Equal(ids(got), tt.want) {
				t.Fatalf("Trunks(%q) = %v, %v, want %v", tt.number, ids(got), err, tt.want)
			}
		})
	}
}

func TestTrunksNoDefaultRoute(t *testing.T) {
	b := newBGCF([]settings.SIPRoute{{ID: "r1", Prefix: "+1", Trunks: []string{"a"}}}, []settings.Trunk{trunk("a", true)}, nil)

	if _, err := b.Trunks("+4915112345678"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("Trunks without a matching route = %v, want ErrNoRoute", err)
	}

	got, err := b.Trunks("+16617629239")
	if err != nil || !slices.Equal(ids(got), []string{"a"}) {
		t.Fatalf("Trunks without a state source = %v, %v, want [a]", ids(got), err)
	}
}

func TestTrunksLongestPrefixWithoutUsableTrunk(t *testing.T) {
	b := newBGCF([]settings.SIPRoute{
		{ID: "r0", Prefix: "", Trunks: []string{"a"}},
		{ID: "r1", Prefix: "+1", Trunks: []string{"down"}},
	}, []settings.Trunk{trunk("a", true), trunk("down", true)}, states{"down": settings.TrunkDown})

	if got, err := b.Trunks("+16617629239"); !errors.Is(err, ErrNoUsableTrunk) {
		t.Fatalf("Trunks = %v, %v, want ErrNoUsableTrunk rather than the default route", ids(got), err)
	}
}

func TestTrunksFollowLiveSettings(t *testing.T) {
	s := settings.Settings{
		Trunks:    []settings.Trunk{trunk("a", true)},
		SIPRoutes: []settings.SIPRoute{{ID: "r0", Trunks: []string{"a"}}},
	}
	b := New(func() settings.Settings { return s }, nil)

	if _, err := b.Trunks("+16617629239"); err != nil {
		t.Fatalf("Trunks = %v, want a route", err)
	}

	s.Trunks = []settings.Trunk{trunk("a", false)}

	if _, err := b.Trunks("+16617629239"); !errors.Is(err, ErrNoUsableTrunk) {
		t.Fatalf("Trunks after disabling the trunk = %v, want ErrNoUsableTrunk", err)
	}
}

func TestNumber(t *testing.T) {
	tests := []struct {
		uri  string
		want string
		ok   bool
	}{
		{"tel:+1-661-762-9239", "+16617629239", true},
		{"tel:+1(661)762.9239;foo=bar", "+16617629239", true},
		{"tel:6617629239;phone-context=+1", "", false},
		{"tel:+1661*9239", "", false},
		{"sip:+16617629239@ims.example.org;user=phone", "", false},
	}

	for _, tt := range tests {
		u, err := sip.ParseURI(tt.uri)
		if err != nil {
			t.Fatalf("ParseURI(%q): %v", tt.uri, err)
		}

		got, ok := Number(u)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("Number(%s) = %q, %v, want %q, %v", tt.uri, got, ok, tt.want, tt.ok)
		}
	}
}
