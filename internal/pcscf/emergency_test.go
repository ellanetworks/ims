package pcscf

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func TestEmergencyURN(t *testing.T) {
	for uri, want := range map[string]bool{
		"urn:service:sos":                 true,
		"urn:service:sos.police":          true,
		"urn:service:sos.ecall.automatic": true,
		"URN:Service:SOS.Fire":            true,
		"urn:service:sos2":                false,
		"urn:service:counseling":          false,
		"urn:nena:service:sos":            false,
		"urn:urn-7:3gpp-service.ims.icsi": false,
		"tel:112":                         false,
		"sip:sos@" + homeDomain:           false,
	} {
		if got := emergencyURN(mustURI(t, uri)); got != want {
			t.Errorf("emergencyURN(%s) = %t, want %t", uri, got, want)
		}
	}
}

// wantAlternativeService checks a 380 rejecting an emergency request, which the UE acts on (TS 24.229 §5.2.10.5).
func wantAlternativeService(t *testing.T, s *ipsecScene, res *sip.Response, token string) {
	t.Helper()

	wantStatus(t, res, 380)

	if pai := res.Header.Values("P-Asserted-Identity"); !slices.Equal(pai, []string{"<sip:" + token + "@" + s.pcscf.String() + ";lr;ob>"}) {
		t.Errorf("P-Asserted-Identity = %q, want the Path of the registration", pai)
	}

	if ct := res.Header.Get("Content-Type"); ct != "application/3gpp-ims+xml" {
		t.Errorf("Content-Type = %q, want application/3gpp-ims+xml", ct)
	}

	body := string(res.Body)
	for _, want := range []string{`<ims-3gpp version="1">`, "<alternative-service><type>emergency</type><reason>"} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want %s", body, want)
		}
	}

	if strings.Contains(body, "<action>") {
		t.Errorf("body = %q, want no action", body)
	}
}

func TestEmergencyCallIsRefused(t *testing.T) {
	var rec *callrecords.Recorder

	s, u := newIPsecRegScene(t, func(c *Config) {
		rec = callrecords.New(callrecords.Config{Store: c.Registrations.(*db.DB)})
		c.Records = rec
	})

	t.Cleanup(func() { _ = rec.Close(context.Background()) })

	token, _ := s.registerOverIPsec(u)

	u.uc.Send(sip.UDP, s.ps, s.ueInvite(u, func(r *sip.Request) {
		r.URI = mustURI(t, "urn:service:sos.police")
		r.Header.Set("To", "<urn:service:sos.police>")
	}))
	wantAlternativeService(t, s, lastResponse(t, u), token)
	s.scscf.RecvNone(quiet)

	r := s.callRecord(t, rec)
	if r.SIPStatus != 380 || r.RequestedParty != "urn:service:sos.police" || r.EndedBy != db.PartyNetwork {
		t.Errorf("record %+v, want a 380 from the network to the emergency URN", r)
	}
}

func TestEmergencyStandaloneRequestIsRefused(t *testing.T) {
	s, u := newIPsecRegScene(t)
	token, _ := s.registerOverIPsec(u)

	r := siptest.NewRequest("MESSAGE", "urn:service:sos", sip.UDP, u.us.Addr())
	r.Header.Set("From", "<"+testTel+">;tag="+sip.NewTag())
	r.Header.Add("Route", "<sip:"+s.ps.String()+";lr>, "+s.serviceRoute())
	u.uc.Send(sip.UDP, s.ps, r)

	wantAlternativeService(t, s, first(u.us.RecvResponse()), token)
	s.scscf.RecvNone(quiet)
}
