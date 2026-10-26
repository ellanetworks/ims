package db

import (
	"reflect"
	"testing"
	"time"
)

func TestIPSMGWRegistrations(t *testing.T) {
	d := openTestDB(t)

	r := IPSMGWRegistration{
		IMPUKey: "sip:alice@example.org", IMPU: "sip:alice@example.org",
		IMPUs: []string{"sip:alice@example.org", "tel:+15551230001"}, MSISDN: "15551230001", IMSI: "001010000000001",
		SCSCF: "sip:10.0.0.1:5080", AS: "sip:ipsmgw.example.org", ExpiresAt: time.Unix(1000, 0).UTC(), Available: true,
	}

	if err := d.PutIPSMGWRegistration(t.Context(), r); err != nil {
		t.Fatal(err)
	}

	r.Available = false

	if err := d.PutIPSMGWRegistration(t.Context(), r); err != nil {
		t.Fatal(err)
	}

	got, err := d.IPSMGWRegistrations(t.Context())
	if err != nil || !reflect.DeepEqual(got, []IPSMGWRegistration{r}) {
		t.Fatalf("registrations = %+v, %v", got, err)
	}

	if err := d.DeleteIPSMGWRegistration(t.Context(), r.IMPUKey); err != nil {
		t.Fatal(err)
	}

	if got, _ := d.IPSMGWRegistrations(t.Context()); len(got) != 0 {
		t.Fatalf("registrations after delete = %+v", got)
	}

	h := IPSMGWHSSRegistration{UserKey: "msisdn:15551230001", MSISDN: "15551230001", Number: "15550000100"}

	if err := d.PutIPSMGWHSSRegistration(t.Context(), h); err != nil {
		t.Fatal(err)
	}

	if got, err := d.IPSMGWHSSRegistrations(t.Context()); err != nil || !reflect.DeepEqual(got, []IPSMGWHSSRegistration{h}) {
		t.Fatalf("HSS registrations = %+v, %v", got, err)
	}

	if err := d.PutIPSMGWHSSRegistration(t.Context(), IPSMGWHSSRegistration{UserKey: "x"}); err == nil {
		t.Fatal("an HSS registration of no number stored")
	}

	if err := d.DeleteIPSMGWHSSRegistration(t.Context(), h.UserKey); err != nil {
		t.Fatal(err)
	}
}
