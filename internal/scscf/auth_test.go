package scscf

import (
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/homedomain"
	"github.com/ellanetworks/ims/sip"
)

func TestParseAuthorizationPrefersAHomeRealm(t *testing.T) {
	domains := homedomain.New("ims.example.com", homeDomain)

	for _, realm := range domains {
		h := sip.Header{}
		h.Add("Authorization", `Digest username="other@visited.example.org", realm="visited.example.org", nonce="", uri="sip:visited.example.org", response=""`)
		h.Add("Authorization", `Digest username="alice@`+realm+`", realm="`+realm+`", nonce="", uri="sip:`+realm+`", response=""`)

		c, err := parseAuthorization(h, domains)
		if err != nil {
			t.Fatal(err)
		}

		if c == nil || c.realm != realm {
			t.Errorf("credentials for realm %s = %+v", realm, c)
		}
	}
}

func TestCredentialsAnswerTheChallengedRealm(t *testing.T) {
	ch := &challenge{realm: "ims.example.com", nonce: "n"}

	if !(&credentials{realm: "IMS.example.com", nonce: "n"}).answers(ch) {
		t.Error("the challenged realm does not answer the challenge")
	}

	if (&credentials{realm: homeDomain, nonce: "n"}).answers(ch) {
		t.Error("another home realm answers the challenge")
	}
}

func TestForeignIdentity(t *testing.T) {
	r := &Registrar{cfg: Config{Home: homedomain.New("ims.example.com", homeDomain)}}
	sub := func(ids ...cx.ProfileIdentity) cx.IMSSubscription {
		return cx.IMSSubscription{ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: ids}}}
	}

	for _, tt := range []struct {
		name string
		sub  cx.IMSSubscription
		want string
	}{
		{"home identities", sub(cx.ProfileIdentity{Identity: "sip:+15551230001@ims.example.com"},
			cx.ProfileIdentity{Identity: "sip:001010000000001@" + homeDomain, Barred: true}, cx.ProfileIdentity{Identity: testTel}), ""},
		{"barred foreign identity", sub(cx.ProfileIdentity{Identity: "sip:001010000000001@other.example.org", Barred: true}), ""},
		{"foreign identity", sub(cx.ProfileIdentity{Identity: "sip:001010000000001@" + homeDomain, Barred: true},
			cx.ProfileIdentity{Identity: "sip:+15551230001@other.example.org"}), "sip:+15551230001@other.example.org"},
	} {
		if got, ok := r.foreignIdentity(tt.sub); got != tt.want || ok != (tt.want != "") {
			t.Errorf("%s: foreignIdentity = %q, %v, want %q", tt.name, got, ok, tt.want)
		}
	}
}
