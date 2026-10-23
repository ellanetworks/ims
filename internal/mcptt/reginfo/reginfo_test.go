package reginfo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func ptr(n uint64) *uint64 { return &n }

// The instances of testdata are valid against the schema of RFC 3680 §5.4.
func TestDecodeTestdata(t *testing.T) {
	for _, c := range []struct {
		file string
		want Reginfo
	}{
		{"example.xml", Reginfo{Version: 0, State: Full, Registrations: []Registration{{
			AOR: "sip:user@example.com", ID: "as9", State: Active, Contacts: []Contact{
				{ID: "76", State: Active, Event: Registered, URI: "sip:user@pc887.example.com"},
				{ID: "77", State: Terminated, Event: Expired, URI: "sip:user@university.edu"},
			},
		}}}},
		{"ims.xml", Reginfo{Version: 3, State: Partial, Registrations: []Registration{
			{AOR: "sip:alice@ims.example.com", ID: "a7", State: Active, Contacts: []Contact{
				{
					ID: "9", State: Terminated, Event: Unregistered, URI: "sip:alice@10.0.0.1:5060", CallID: "ue1@10.0.0.1", CSeq: ptr(4),
					UnknownParams: []Param{
						{Name: "+sip.instance", Value: `"<urn:uuid:00000000-0000-1000-8000-000000000001>"`},
						{Name: "+g.3gpp.mcptt"},
					},
				},
				{ID: "12", State: Active, Event: Shortened, URI: "sip:alice@10.0.0.2:5060", Expires: ptr(60)},
			}},
			{AOR: "tel:+15555550101", ID: "a8", State: Terminated, Contacts: []Contact{
				{ID: "13", State: Terminated, Event: Probation, URI: "sip:alice@10.0.0.1:5060", RetryAfter: ptr(300)},
			}},
		}}},
		{"init.xml", Reginfo{State: Full, Registrations: []Registration{{AOR: "sip:bob@ims.example.com", ID: "b1", State: Init}}}},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}

		got, err := Decode(b)
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}

		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n%+v\nwant\n%+v", c.file, got, c.want)
		}
	}
}

func TestParam(t *testing.T) {
	c := Contact{UnknownParams: []Param{{Name: "+sip.Instance", Value: `"<urn:x>"`}}}

	if v, ok := c.Param("+SIP.instance"); !ok || v != `"<urn:x>"` {
		t.Errorf("%q %v", v, ok)
	}

	if _, ok := c.Param("+g.3gpp.mcptt"); ok {
		t.Error("absent parameter")
	}
}

// RFC 3680 §5.1, §5.4: documents not valid in what is read are refused.
func TestDecodeInvalid(t *testing.T) {
	const ns = `xmlns="urn:ietf:params:xml:ns:reginfo"`

	reg := func(contact string) string {
		return `<reginfo ` + ns + ` version="1" state="partial"><registration aor="sip:a@x" id="r" state="active">` +
			contact + `</registration></reginfo>`
	}

	for _, c := range []struct{ name, doc, want string }{
		{"not XML", "<reginfo", "reginfo: "},
		{"another root", `<presence xmlns="urn:ietf:params:xml:ns:pidf"/>`, "root element"},
		{"no version", `<reginfo ` + ns + ` state="full"/>`, "version"},
		{"version of 33 bits", `<reginfo ` + ns + ` version="4294967296" state="full"/>`, "version"},
		{"state", `<reginfo ` + ns + ` version="0" state="all"/>`, "state"},
		{"element of the namespace", `<reginfo ` + ns + ` version="0" state="full"><contact/></reginfo>`, "<contact> in <reginfo>"},
		{"no aor", `<reginfo ` + ns + ` version="0" state="full"><registration id="r" state="active"/></reginfo>`, "without aor"},
		{"no id", `<reginfo ` + ns + ` version="0" state="full"><registration aor="sip:a@x" state="active"/></reginfo>`, "without id"},
		{"registration state", `<reginfo ` + ns + ` version="0" state="full"><registration aor="sip:a@x" id="r" state="gone"/></reginfo>`, "state"},
		{"contact without id", reg(`<contact state="active" event="registered"><uri>sip:c@x</uri></contact>`), "without id"},
		{"contact state", reg(`<contact id="c" state="init" event="registered"><uri>sip:c@x</uri></contact>`), "state"},
		{"event", reg(`<contact id="c" state="active" event="moved"><uri>sip:c@x</uri></contact>`), "event"},
		{"expires", reg(`<contact id="c" state="active" event="refreshed" expires="-1"><uri>sip:c@x</uri></contact>`), "expires"},
		{"cseq", reg(`<contact id="c" state="active" event="refreshed" cseq="x"><uri>sip:c@x</uri></contact>`), "cseq"},
		{"shortened", reg(`<contact id="c" state="active" event="shortened"><uri>sip:c@x</uri></contact>`), "without expires"},
		{"probation", reg(`<contact id="c" state="terminated" event="probation"><uri>sip:c@x</uri></contact>`), "without retry-after"},
		{"no uri", reg(`<contact id="c" state="active" event="registered"/>`), "0 <uri>"},
		{"two uris", reg(`<contact id="c" state="active" event="registered"><uri>sip:c@x</uri><uri>sip:d@x</uri></contact>`), "2 <uri>"},
		{"param without name", reg(`<contact id="c" state="active" event="registered"><uri>sip:c@x</uri><unknown-param/></contact>`), "without name"},
		{"too large", `<reginfo ` + ns + ` version="0" state="full">` + strings.Repeat(" ", maxSize) + `</reginfo>`, "exceeds"},
	} {
		if _, err := Decode([]byte(c.doc)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func FuzzReginfo(f *testing.F) {
	fs, err := filepath.Glob("testdata/*.xml")
	if err != nil {
		f.Fatal(err)
	}

	for _, name := range fs {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}

		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := Decode(b)
		if err != nil {
			return
		}

		// What decodes is of the values of the schema.
		if r.State != Full && r.State != Partial {
			t.Fatalf("state %q", r.State)
		}

		for _, reg := range r.Registrations {
			if reg.AOR == "" || reg.State != Init && reg.State != Active && reg.State != Terminated {
				t.Fatalf("registration %+v", reg)
			}

			for _, c := range reg.Contacts {
				if c.URI == "" || c.State != Active && c.State != Terminated || c.Event == "" {
					t.Fatalf("contact %+v", c)
				}
			}
		}
	})
}
