package icscf

import (
	"testing"

	"github.com/ellanetworks/ims/internal/homedomain"
	"github.com/ellanetworks/ims/sip"
)

func TestCredentialsPreferAHomeRealm(t *testing.T) {
	domains := homedomain.New("ims.example.com", homeDomain)

	for _, realm := range domains {
		h := sip.Header{}
		h.Add("Authorization", `Digest username="other@visited.example.org", realm="visited.example.org"`)
		h.Add("Authorization", `Digest username="alice@`+realm+`", realm="`+realm+`"`)

		a, ok := credentials(h, domains)
		if !ok || authParam(a, "username") != "alice@"+realm {
			t.Errorf("credentials for realm %s = %+v, %v", realm, a, ok)
		}
	}
}
