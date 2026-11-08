package homedomain

import (
	"slices"
	"testing"
)

func TestNew(t *testing.T) {
	d := New("IMS.Example.com.", "ims.mnc001.mcc001.3gppnetwork.org", "ims.example.com", "")

	if want := (Domains{"ims.example.com", "ims.mnc001.mcc001.3gppnetwork.org"}); !slices.Equal(d, want) {
		t.Fatalf("New = %v, want %v", d, want)
	}

	if d.Primary() != "ims.example.com" {
		t.Fatalf("Primary = %q", d.Primary())
	}

	if (Domains{}).Primary() != "" {
		t.Fatal("empty Domains has a primary")
	}
}

func TestMatch(t *testing.T) {
	d := New("ims.example.com", "ims.mnc001.mcc001.3gppnetwork.org")

	for _, tt := range []struct {
		host        string
		has, covers bool
	}{
		{"ims.example.com", true, true},
		{"IMS.EXAMPLE.COM.", true, true},
		{"ims.mnc001.mcc001.3gppnetwork.org", true, true},
		{"mcptt.ims.example.com", false, true},
		{"pcscf.ims.mnc001.mcc001.3gppnetwork.org", false, true},
		{"example.com", false, false},
		{"notims.example.com", false, false},
		{"", false, false},
	} {
		if got := d.Has(tt.host); got != tt.has {
			t.Errorf("Has(%q) = %v, want %v", tt.host, got, tt.has)
		}

		if got := d.Covers(tt.host); got != tt.covers {
			t.Errorf("Covers(%q) = %v, want %v", tt.host, got, tt.covers)
		}
	}
}
