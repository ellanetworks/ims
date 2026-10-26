package sip

import "testing"

func TestParseResponseSource(t *testing.T) {
	for _, tc := range []struct {
		in, entity, fe, out string
	}{
		{"fe=<urn:3gpp:fe:p-cscf.term>", "p-cscf.term", "p-cscf", "fe=<urn:3gpp:fe:p-cscf.term>"},
		{"fe=<urn:3gpp:fe:s-cscf>", "s-cscf", "s-cscf", "fe=<urn:3gpp:fe:s-cscf>"},
		{" FE = <URN:3GPP:FE:P-CSCF.orig> ", "P-CSCF.orig", "p-cscf", "fe=<urn:3gpp:fe:P-CSCF.orig>"},
		{"x;fe=<urn:3gpp:fe:as.tas>;y", "as.tas", "as", "fe=<urn:3gpp:fe:as.tas>"},
	} {
		got, err := ParseResponseSource(tc.in)
		if err != nil {
			t.Errorf("ParseResponseSource(%q): %v", tc.in, err)
			continue
		}

		if got.Entity != tc.entity || got.FunctionalEntity() != tc.fe || got.String() != tc.out {
			t.Errorf("ParseResponseSource(%q) = %+v (%s, %s), want %s, %s, %s", tc.in, got, got.FunctionalEntity(), got,
				tc.entity, tc.fe, tc.out)
		}
	}

	for _, in := range []string{
		"", "x", "fe", "fe=urn:3gpp:fe:p-cscf", "fe=<urn:3gpp:fe:p-cscf", "fe=<urn:x:p-cscf>", "fe=<urn:3gpp:fe:>",
		"fe=<urn:3gpp:fe:p-cscf> x", "<urn:3gpp:fe:s-cscf>",
	} {
		if r, err := ParseResponseSource(in); err == nil {
			t.Errorf("ParseResponseSource(%q) = %+v, want error", in, r)
		}
	}
}

func TestHeaderResponseSource(t *testing.T) {
	var h Header

	if _, ok, err := h.ResponseSource(); ok || err != nil {
		t.Fatalf("ResponseSource of no header = %v, %v", ok, err)
	}

	h.Add("Response-Source", ResponseSource{Entity: EntityPCSCF + "." + SideTerm}.String())

	if r, ok, err := h.ResponseSource(); !ok || err != nil || r.FunctionalEntity() != EntityPCSCF {
		t.Fatalf("ResponseSource = %+v, %v, %v", r, ok, err)
	}
}
