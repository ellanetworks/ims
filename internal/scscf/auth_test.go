package scscf

import (
	"testing"

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
