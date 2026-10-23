package registrar

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
)

func uri(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// TS 33.180 §B.2.2: the mcptt_id claim of the access token of the REGISTER of TS 24.379 §7.2.1, authorised at the
// IMPUs of its user only: at a registration one IMPU of its user is of, an implicit registration set (TS 24.229
// §5.4.1.7 c).
func TestUnvalidated(t *testing.T) {
	cfg, err := settings.New(settings.Settings{
		System: settings.System{
			ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com", ControllingPSI: "sip:mcptt-controlling@mcptt.example.com",
		},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    "mcptt.example.com",
			OnNetwork: settings.OnNetwork{FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)}},
		},
		Users: []settings.User{
			{MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:alice@ims.example.com"}},
			{MCPTTUserID: "sip:bob@mcptt.example.com", IMPUs: []string{"sip:bob@ims.example.com"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile("../../../sip/mcpttinfo/testdata/register.xml")
	if err != nil {
		t.Fatal(err)
	}

	info, err := mcpttinfo.Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	token, _ := info.Params.AccessToken.Text()

	u, err := Unvalidated{}.Authorise(cfg, token, []sip.URI{uri(t, "sip:alice@IMS.example.com")})
	if err != nil || u.ID().String() != "sip:alice@mcptt.example.com" {
		t.Fatalf("%v, %v", u, err)
	}

	if u, err := (Unvalidated{}).Authorise(cfg, token, []sip.URI{uri(t, "tel:+15555550199"), uri(t, "sip:alice@ims.example.com")}); err != nil || u.ID().String() != "sip:alice@mcptt.example.com" {
		t.Fatalf("implicit registration set: %v, %v", u, err)
	}

	if _, err := (Unvalidated{}).Authorise(cfg, token, []sip.URI{uri(t, "sip:bob@ims.example.com")}); !errors.Is(err, ErrIMPU) {
		t.Errorf("IMPU of another user: %v", err)
	}

	for _, bad := range []string{"", "a.b", "a.!.c", "a.e30.c", "a.eyJtY3B0dF9pZCI6Inglen0.c"} {
		if _, err := (Unvalidated{}).Authorise(cfg, bad, []sip.URI{uri(t, "sip:alice@ims.example.com")}); !errors.Is(err, ErrToken) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
