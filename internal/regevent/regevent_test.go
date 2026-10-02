package regevent_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/regevent"
)

func u32(v uint32) *uint32 { return &v }

func fullReginfo() regevent.Reginfo {
	return regevent.Reginfo{
		Version: 7,
		State:   regevent.Full,
		Registrations: []regevent.Registration{
			{
				AOR:   "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
				ID:    "a7",
				State: regevent.Active,
				Contacts: []regevent.Contact{
					{
						ID:          "76",
						State:       regevent.Active,
						Event:       regevent.Registered,
						Expires:     u32(3600),
						RetryAfter:  u32(30),
						CallID:      "abc@10.0.0.1",
						CSeq:        u32(42),
						Q:           "0.5",
						URI:         "sip:user@10.0.0.1:5060;transport=udp",
						DisplayName: "Joe <Bloggs> & \"Co\"",
						DisplayLang: "en",
						UnknownParams: []regevent.UnknownParam{
							{Name: "+g.3gpp.smsip"},
							{Name: "+sip.instance", Value: `"<urn:gsma:imei:35622410-483840-0>"`},
						},
					},
					{
						ID:    "77",
						State: regevent.Terminated,
						Event: regevent.Expired,
						URI:   "sip:user@10.0.0.2",
					},
				},
			},
			{
				AOR:   "tel:+15551234",
				ID:    "a8",
				State: regevent.Init,
			},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	in := fullReginfo()

	b, err := regevent.Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	s := string(b)

	if !strings.HasPrefix(s, "<?xml") {
		t.Errorf("missing XML declaration: %s", s)
	}

	if !strings.Contains(s, `<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="7" state="full">`) {
		t.Errorf("unexpected root element: %s", s)
	}

	if strings.Contains(s, "<urn:gsma") || strings.Contains(s, `>"<`) {
		t.Errorf("unknown-param value not escaped: %s", s)
	}

	if !strings.Contains(s, "&lt;urn:gsma:imei:35622410-483840-0&gt;") {
		t.Errorf("expected escaped instance id: %s", s)
	}

	if !strings.Contains(s, `xml:lang="en"`) {
		t.Errorf("expected xml:lang on display-name: %s", s)
	}

	uri := strings.Index(s, "<uri>")
	dn := strings.Index(s, "<display-name")
	up := strings.Index(s, "<unknown-param")

	if uri >= dn || dn >= up {
		t.Errorf("unexpected contact child order: %s", s)
	}

	second := s[strings.Index(s, `id="77"`):]
	second = second[:strings.Index(second, ">")]

	for _, attr := range []string{"expires", "retry-after", "callid", "cseq", "q="} {
		if strings.Contains(second, attr) {
			t.Errorf("absent optional attribute %s emitted: %s", attr, second)
		}
	}

	out, err := regevent.Decode(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}

func TestEncodeValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*regevent.Reginfo)
	}{
		{"reginfo state missing", func(r *regevent.Reginfo) { r.State = "" }},
		{"reginfo state invalid", func(r *regevent.Reginfo) { r.State = "init" }},
		{"aor missing", func(r *regevent.Reginfo) { r.Registrations[0].AOR = "" }},
		{"registration id missing", func(r *regevent.Reginfo) { r.Registrations[0].ID = "" }},
		{"registration state missing", func(r *regevent.Reginfo) { r.Registrations[0].State = "" }},
		{"registration state invalid", func(r *regevent.Reginfo) { r.Registrations[0].State = "pending" }},
		{"contact id missing", func(r *regevent.Reginfo) { r.Registrations[0].Contacts[0].ID = "" }},
		{"contact state missing", func(r *regevent.Reginfo) { r.Registrations[0].Contacts[0].State = "" }},
		{"contact state init", func(r *regevent.Reginfo) { r.Registrations[0].Contacts[0].State = regevent.Init }},
		{"contact event missing", func(r *regevent.Reginfo) { r.Registrations[0].Contacts[0].Event = "" }},
		{"contact event invalid", func(r *regevent.Reginfo) { r.Registrations[0].Contacts[0].Event = "bogus" }},
		{"contact uri missing", func(r *regevent.Reginfo) { r.Registrations[0].Contacts[0].URI = "" }},
		{"unknown-param name missing", func(r *regevent.Reginfo) {
			r.Registrations[0].Contacts[0].UnknownParams[0].Name = ""
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fullReginfo()
			tt.mutate(&r)

			if _, err := regevent.Encode(r); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	const ns = `xmlns="urn:ietf:params:xml:ns:reginfo"`

	tests := []struct {
		name string
		doc  string
	}{
		{"wrong root", `<foo ` + ns + ` version="0" state="full"/>`},
		{"wrong namespace", `<reginfo xmlns="urn:example" version="0" state="full"/>`},
		{"no namespace", `<reginfo version="0" state="full"/>`},
		{"version missing", `<reginfo ` + ns + ` state="full"/>`},
		{"version invalid", `<reginfo ` + ns + ` version="x" state="full"/>`},
		{"state missing", `<reginfo ` + ns + ` version="0"/>`},
		{"state invalid", `<reginfo ` + ns + ` version="0" state="bogus"/>`},
		{"unknown charset", `<?xml version="1.0" encoding="EBCDIC"?><reginfo ` + ns + ` version="0" state="full"/>`},
		{"malformed", `<reginfo ` + ns + ` version="0" state="full">`},
		{"too large", `<reginfo ` + ns + ` version="0" state="full">` + strings.Repeat(" ", 1<<20) + `</reginfo>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := regevent.Decode([]byte(tt.doc)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestDecodeSkipsInvalidElements(t *testing.T) {
	const ns = `xmlns="urn:ietf:params:xml:ns:reginfo"`

	good := `<contact id="1" state="active" event="registered"><uri>sip:a@good</uri></contact>`
	goodReg := `<registration aor="sip:a@b" id="1" state="active">` + good + `</registration>`

	contacts := []struct {
		name    string
		contact string
	}{
		{"contact id missing", `<contact state="active" event="registered"><uri>sip:a@c</uri></contact>`},
		{"contact state missing", `<contact id="2" event="registered"><uri>sip:a@c</uri></contact>`},
		{"contact state invalid", `<contact id="2" state="bogus" event="registered"><uri>sip:a@c</uri></contact>`},
		{"contact event missing", `<contact id="2" state="active"><uri>sip:a@c</uri></contact>`},
		{"contact event invalid", `<contact id="2" state="active" event="bogus"><uri>sip:a@c</uri></contact>`},
		{"contact uri missing", `<contact id="2" state="active" event="registered"/>`},
		{"contact uri empty", `<contact id="2" state="active" event="registered"><uri> </uri></contact>`},
		{"contact expires invalid", `<contact id="2" state="active" event="registered" expires="-1"><uri>sip:a@c</uri></contact>`},
		{"contact cseq invalid", `<contact id="2" state="active" event="registered" cseq="x"><uri>sip:a@c</uri></contact>`},
		{"unknown-param name missing", `<contact id="2" state="active" event="registered"><uri>sip:a@c</uri>` +
			`<unknown-param>v</unknown-param></contact>`},
	}

	for _, tt := range contacts {
		t.Run(tt.name, func(t *testing.T) {
			doc := `<reginfo ` + ns + ` version="3" state="full"><registration aor="sip:a@b" id="1" state="active">` +
				good + tt.contact + good + `</registration></reginfo>`

			r, err := regevent.Decode([]byte(doc))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if len(r.Registrations) != 1 || len(r.Registrations[0].Contacts) != 2 {
				t.Fatalf("got %+v, want one registration with the two good contacts", r)
			}

			for _, c := range r.Registrations[0].Contacts {
				if c.URI != "sip:a@good" {
					t.Fatalf("kept contact %+v", c)
				}
			}
		})
	}

	registrations := []struct {
		name string
		reg  string
	}{
		{"aor missing", `<registration id="1" state="active"/>`},
		{"registration id missing", `<registration aor="sip:a@b" state="active"/>`},
		{"registration state missing", `<registration aor="sip:a@b" id="1"/>`},
		{"registration state invalid", `<registration aor="sip:a@b" id="1" state="bogus"/>`},
		{"prefixed required attribute", `<registration xmlns:x="urn:x" x:aor="sip:a@b" id="1" state="active"/>`},
	}

	for _, tt := range registrations {
		t.Run(tt.name, func(t *testing.T) {
			doc := `<reginfo ` + ns + ` version="3" state="full">` + goodReg + tt.reg + goodReg + `</reginfo>`

			r, err := regevent.Decode([]byte(doc))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if len(r.Registrations) != 2 {
				t.Fatalf("got %+v, want the two good registrations", r)
			}

			for _, reg := range r.Registrations {
				if reg.AOR != "sip:a@b" || len(reg.Contacts) != 1 {
					t.Fatalf("kept registration %+v", reg)
				}
			}
		})
	}
}

func TestDecodeLargeNumbers(t *testing.T) {
	doc := `<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="18446744073709551615" state="full">` +
		`<registration aor="sip:a@b" id="1" state="active">` +
		`<contact id="2" state="active" event="registered" expires="4294967296" retry-after="18446744073709551615" cseq="4294967295">` +
		`<uri>sip:a@c</uri></contact></registration></reginfo>`

	r, err := regevent.Decode([]byte(doc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if r.Version != 18446744073709551615 {
		t.Fatalf("version = %d", r.Version)
	}

	c := r.Registrations[0].Contacts[0]
	for name, got := range map[string]*uint32{"expires": c.Expires, "retry-after": c.RetryAfter, "cseq": c.CSeq} {
		if got == nil || *got != 4294967295 {
			t.Fatalf("%s = %v, want clamped to 4294967295", name, got)
		}
	}
}

func TestDecodeCharsets(t *testing.T) {
	const body = `<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full">` +
		`<registration aor="sip:a@b" id="1" state="active">` +
		`<contact id="2" state="active" event="registered"><uri>sip:a@c</uri>` +
		"<display-name>Ren\xe9 M\xfcller</display-name></contact></registration></reginfo>"

	tests := []struct {
		name     string
		encoding string
		body     string
		want     string
	}{
		{"ISO-8859-1", "ISO-8859-1", body, "Ren\u00e9 M\u00fcller"},
		{"latin1", "latin1", body, "Ren\u00e9 M\u00fcller"},
		{"us-ascii", "US-ASCII", strings.NewReplacer("\xe9", "e", "\xfc", "u").Replace(body), "Rene Muller"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := `<?xml version="1.0" encoding="` + tt.encoding + `"?>` + tt.body

			r, err := regevent.Decode([]byte(doc))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if got := r.Registrations[0].Contacts[0].DisplayName; got != tt.want {
				t.Fatalf("display name = %q, want %q", got, tt.want)
			}
		})
	}
}

// Example in the style of 3GPP TS 24.229 with GRUU (RFC 5628) and wildcarded
// identity (TS 24.229 §7.10) extensions.
const extendedDoc = `<?xml version="1.0"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo"
         xmlns:gr="urn:ietf:params:xml:ns:gruuinfo"
         xmlns:ere="urn:3gpp:ns:extRegExp:1.0"
         version="0" state="full">
  <registration aor="sip:user1_public1@home1.net" id="a7" state="active">
    <contact id="76" state="active" event="registered" gr:extra="ignored">
      <uri>
        sip:[5555::aaa:bbb:ccc:ddd];comp=sigcomp
      </uri>
      <unknown-param name="+sip.instance">"&lt;urn:gsma:imei:90420156-025763-0&gt;"</unknown-param>
      <gr:pub-gruu uri="sip:user1_public1@home1.net;gr=urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"/>
      <gr:temp-gruu uri="sip:tgruu.7hs==jd7vnzga5w7fajsc7-ajd6fabz0f8g5@example.com;gr" first-cseq="54301"/>
      <foo>unknown element in the reginfo namespace</foo>
    </contact>
    <ere:wildcardedIdentity>sip:user!.*!@home1.net</ere:wildcardedIdentity>
    <other:contact xmlns:other="urn:other" id="99" state="active" event="registered">
      <other:uri>sip:ignored@example.com</other:uri>
    </other:contact>
  </registration>
  <ere:registration aor="sip:ignored@home1.net" id="x" state="active"/>
</reginfo>`

func TestDecodeIgnoresExtensions(t *testing.T) {
	r, err := regevent.Decode([]byte(extendedDoc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := regevent.Reginfo{
		Version: 0,
		State:   regevent.Full,
		Registrations: []regevent.Registration{{
			AOR:   "sip:user1_public1@home1.net",
			ID:    "a7",
			State: regevent.Active,
			Contacts: []regevent.Contact{{
				ID:    "76",
				State: regevent.Active,
				Event: regevent.Registered,
				URI:   "sip:[5555::aaa:bbb:ccc:ddd];comp=sigcomp",
				UnknownParams: []regevent.UnknownParam{
					{Name: "+sip.instance", Value: `"<urn:gsma:imei:90420156-025763-0>"`},
				},
			}},
		}},
	}

	if !reflect.DeepEqual(r, want) {
		t.Fatalf("got  %+v\nwant %+v", r, want)
	}
}

func sipBody(t *testing.T, path string) []byte {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	for _, sep := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n")} {
		if i := bytes.Index(b, sep); i >= 0 {
			return b[i+len(sep):]
		}
	}

	t.Fatalf("%s: no message body", path)

	return nil
}

func TestDecodeCorpus(t *testing.T) {
	files, err := filepath.Glob("../../sip/internal/corpus/testdata/*/*/*-NOTIFY.sip")
	if err != nil {
		t.Fatal(err)
	}

	var n int

	for _, f := range files {
		body := sipBody(t, f)
		if !bytes.Contains(body, []byte("<reginfo")) {
			continue
		}

		n++

		t.Run(filepath.Base(filepath.Dir(f))+"/"+filepath.Base(f), func(t *testing.T) {
			r, err := regevent.Decode(body)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if r.Version != 2 || r.State != regevent.Full {
				t.Errorf("version/state = %d/%s, want 2/full", r.Version, r.State)
			}

			if len(r.Registrations) != 2 {
				t.Fatalf("registrations = %d, want 2", len(r.Registrations))
			}

			if !strings.HasPrefix(r.Registrations[0].AOR, "tel:") {
				t.Errorf("first aor = %q, want tel URI", r.Registrations[0].AOR)
			}

			for _, reg := range r.Registrations {
				if reg.State != regevent.Active || len(reg.Contacts) != 1 {
					t.Fatalf("registration %s: state %s, %d contacts", reg.AOR, reg.State, len(reg.Contacts))
				}

				c := reg.Contacts[0]
				if c.Event != regevent.Registered || c.Expires == nil || !strings.HasPrefix(c.URI, "sip:") {
					t.Errorf("unexpected contact %+v", c)
				}

				var instance string

				for _, p := range c.UnknownParams {
					if p.Name == "+sip.instance" {
						instance = p.Value
					}
				}

				if !strings.HasPrefix(instance, `"<urn:gsma:imei:`) || !strings.HasSuffix(instance, `>"`) {
					t.Errorf("+sip.instance = %q", instance)
				}
			}

			reenc, err := regevent.Encode(r)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}

			again, err := regevent.Decode(reenc)
			if err != nil {
				t.Fatalf("decode re-encoded: %v", err)
			}

			if !reflect.DeepEqual(r, again) {
				t.Fatalf("re-encode mismatch:\n%+v\n%+v", r, again)
			}
		})
	}

	if n == 0 {
		t.Fatal("no reginfo NOTIFY found in corpus")
	}
}

func TestDecodeCorpusValues(t *testing.T) {
	r, err := regevent.Decode(sipBody(t, "../../sip/internal/corpus/testdata/open5gs/ipsec_reg/020-NOTIFY.sip"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	reg := r.Registrations[0]
	if reg.AOR != "tel:0398765432100" || reg.ID != "0x7f45bcb27c98" {
		t.Errorf("registration = %s/%s", reg.AOR, reg.ID)
	}

	if r.Registrations[1].AOR != "sip:0398765432100" {
		t.Errorf("second aor = %s", r.Registrations[1].AOR)
	}

	c := reg.Contacts[0]
	if c.ID != "0x7f45bcb27058" || *c.Expires != 3599 || c.Q != "1.000" {
		t.Errorf("contact = %+v", c)
	}

	if c.URI != "sip:192.168.101.5:6300;alias=192.168.101.5~6301~2" {
		t.Errorf("uri = %q", c.URI)
	}

	want := []regevent.UnknownParam{
		{Name: "+g.3gpp.smsip"},
		{Name: "+g.3gpp.icsi-ref", Value: `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`},
		{Name: "q", Value: `"1.0"`},
		{Name: "+sip.instance", Value: `"<urn:gsma:imei:35622410-483840-0>"`},
	}

	if !reflect.DeepEqual(c.UnknownParams, want) {
		t.Errorf("unknown params = %+v", c.UnknownParams)
	}
}
