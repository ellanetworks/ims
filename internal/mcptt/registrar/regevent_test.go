package registrar

import (
	"slices"
	"testing"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

// TS 24.229 §5.7.1.1 a, c; TS 23.003 §13.4; RFC 3261 §19.1.6: the reg event subscription of an IMPU, the To of the
// third-party REGISTER, is of a SIP URI that contains it: a SIP IMPU itself; of a tel URI, a SIP URI of its implicit
// registration set, else the SIP URI of its number at the home domain, "user=phone", of the userinfo of the tel URI,
// as of Ella Core's set of a tel URI and a barred temporary IMPU, which the P-Associated-URI leaves out: of a global
// number, without parameters; of a local number, with its phone-context. The IMPU decides, wherever the set has it.
// None without home domain, or of a number that is neither.
func TestSubscribed(t *testing.T) {
	const home = "ims.mnc001.mcc001.3gppnetwork.org"

	for _, tc := range []struct {
		name  string
		impus []string
		home  string
		want  string
	}{
		{"SIP", []string{"sip:alice@" + home, "tel:+15555550101"}, home, "sip:alice@" + home},
		{"SIP, after a tel URI of the set", []string{"sip:alice@" + home, "tel:+15555550101", "sip:a2@" + home}, home, "sip:alice@" + home},
		{"SIP without home domain", []string{"sip:alice@" + home}, "", "sip:alice@" + home},
		// §5.7.1.1 a: the SIP URI of the To's number, not another public user identity of the set.
		{"tel, SIP URI in the set", []string{"tel:+15555550101", "sip:alice@" + home}, home, "sip:+15555550101@" + home + ";user=phone"},
		{"tel of no home domain, SIP URI in the set", []string{"tel:+15555550101", "sip:alice@" + home}, "", "sip:alice@" + home},
		{"tel alone", []string{"tel:+15555550101"}, home, "sip:+15555550101@" + home + ";user=phone"},
		{"tel, another tel URI first in the set", []string{"tel:+15555550101", "tel:+15555550199"}, home, "sip:+15555550101@" + home + ";user=phone"},
		{"tel of visual separators and parameters", []string{"tel:+1-555-555-0101;isub=1"}, home, "sip:+15555550101@" + home + ";user=phone"},
		{"tel without home domain", []string{"tel:+15555550101"}, "", ""},
		{"local number", []string{"tel:555-0101;phone-context=Home.Example.net"}, home, "sip:5550101;phone-context=home.example.net@" + home + ";user=phone"},
		{"local number of a global context", []string{"tel:0101;phone-context=+1-555-555"}, home, "sip:0101;phone-context=+1555555@" + home + ";user=phone"},
		{"local number of #", []string{"tel:*31#;phone-context=" + home}, home, "sip:*31%23;phone-context=" + home + "@" + home + ";user=phone"},
		{"local number without phone-context", []string{"tel:5550101"}, home, ""},
		{"none", nil, home, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var impus []sip.URI
			for _, s := range tc.impus {
				impus = append(impus, uri(t, s))
			}

			var impu sip.URI
			if len(impus) > 0 {
				impu = impus[0]
			}

			// The set as stored may be in any order: the IMPU decides.
			slices.Reverse(impus)

			got := subscribed(impu, impus, tc.home)
			if tc.want == "" {
				if got.Scheme != "" || got.IsSIP() {
					t.Fatalf("subscribed %s", got)
				}

				return
			}

			// RFC 3261 §19.1.4: "user" compared, if either has it.
			if !got.IsSIP() || !got.Equivalent(uri(t, tc.want)) {
				t.Fatalf("subscribed %s, want %s", got, tc.want)
			}
		})
	}
}

// RFC 3680 §5.1; TS 24.229 §5.4.2.1.2 step 3: the <registration> elements of the documents of the subscription of
// an IMPU are of it whether of the IMPU, of the URI subscribed to, the SIP URI of the number of a tel URI, or of
// another IMPU of the implicit registration set of its bindings; not of another IMPU.
func TestReports(t *testing.T) {
	const home = "ims.example.com"

	g := New(Config{})
	tel := uri(t, "tel:+15555550101")
	ik := settings.Key(tel)

	var ch change

	g.bind(&binding{
		key: bindingKey{ik, "c1"}, user: settings.Key(uri(t, "sip:alice@mcptt.example.com")),
		Binding: Binding{
			MCPTTID: uri(t, "sip:alice@mcptt.example.com"), ClientID: "c1", IMPU: tel,
			IMPUs: []sip.URI{tel, uri(t, "sip:alice@"+home)},
		},
	}, &ch)

	w := &watch{ik: ik, impu: subscribed(tel, []sip.URI{tel}, home)}

	for aor, want := range map[string]bool{
		"tel:+15555550101":                              true,
		"tel:+1-555-555-0101":                           true,
		"sip:+15555550101@" + home + ";user=phone":      true,
		"sip:+15555550101@IMS.example.com;user=phone":   true,
		"sip:alice@" + home:                             true,
		"sip:bob@" + home:                               false,
		"tel:+15555550102":                              false,
		"sip:+15555550101@other.example.com;user=phone": false,
		"not a URI":                                     false,
	} {
		if got := g.reports(w, aor); got != want {
			t.Errorf("reports %s: %v, want %v", aor, got, want)
		}
	}
}
