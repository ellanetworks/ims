package reginfo

import (
	"bytes"
	"io/fs"
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
				{ID: "76", State: Active, Event: Registered, URI: "sip:user@pc887.example.com", DurationRegistered: ptr(7322), Q: "0.8"},
				{ID: "77", State: Terminated, Event: Expired, URI: "sip:user@university.edu", DurationRegistered: ptr(3600), Q: "0.5"},
			},
		}}}},
		{"ims.xml", Reginfo{Version: 3, State: Partial, Registrations: []Registration{
			{AOR: "sip:alice@ims.example.com", ID: "a7", State: Active, Contacts: []Contact{
				{
					ID: "9", State: Terminated, Event: Unregistered, URI: "sip:alice@10.0.0.1:5060", CallID: "ue1@10.0.0.1", CSeq: ptr(4),
					DisplayName: "Alice", DisplayLang: "en",
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

		roundTrip(t, got)
	}
}

func roundTrip(t *testing.T, r Reginfo) {
	t.Helper()

	b, err := Encode(r)
	if err != nil {
		t.Fatalf("Encode(%+v): %v", r, err)
	}

	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode(Encode): %v\n%s", err, b)
	}

	if !reflect.DeepEqual(got, r) {
		t.Fatalf("Decode(Encode)\n%+v\nwant\n%+v\n%s", got, r, b)
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

func full() Reginfo {
	return Reginfo{
		Version: 7,
		State:   Full,
		Registrations: []Registration{
			{
				AOR:   "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
				ID:    "a7",
				State: Active,
				Contacts: []Contact{
					{
						ID:          "76",
						State:       Active,
						Event:       Registered,
						Expires:     ptr(3600),
						RetryAfter:  ptr(30),
						CallID:      "abc@10.0.0.1",
						CSeq:        ptr(42),
						Q:           "0.5",
						URI:         "sip:user@10.0.0.1:5060;transport=udp",
						DisplayName: "Joe <Bloggs> & \"Co\"",
						DisplayLang: "en",
						UnknownParams: []Param{
							{Name: "+g.3gpp.smsip"},
							{Name: "+sip.instance", Value: `"<urn:gsma:imei:35622410-483840-0>"`},
						},
					},
					{ID: "77", State: Terminated, Event: Expired, URI: "sip:user@10.0.0.2"},
				},
			},
			{AOR: "tel:+15551234", ID: "a8", State: Init},
		},
	}
}

func TestEncode(t *testing.T) {
	in := full()

	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}

	s := string(b)

	for _, want := range []string{
		`<?xml`,
		`<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="7" state="full">`,
		`<contact id="76" state="active" event="registered" expires="3600" retry-after="30" callid="abc@10.0.0.1" cseq="42" q="0.5">`,
		`<display-name xml:lang="en">Joe &lt;Bloggs&gt; &amp; "Co"</display-name>`,
		`<unknown-param name="+sip.instance">"&lt;urn:gsma:imei:35622410-483840-0&gt;"</unknown-param>`,
		// The optional attributes absent are left out.
		`<contact id="77" state="terminated" event="expired"><uri>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %s in\n%s", want, s)
		}
	}

	// The children of a contact in the order of the schema (§5.4).
	if uri, dn, up := strings.Index(s, "<uri>"), strings.Index(s, "<display-name"), strings.Index(s, "<unknown-param"); uri >= dn || dn >= up {
		t.Errorf("order of the children of <contact>: %s", s)
	}

	roundTrip(t, in)
}

// Encode writes only what Decode reads.
func TestEncodeInvalid(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*Reginfo)
	}{
		{"reginfo state missing", func(r *Reginfo) { r.State = "" }},
		{"reginfo state invalid", func(r *Reginfo) { r.State = "init" }},
		{"aor missing", func(r *Reginfo) { r.Registrations[0].AOR = " " }},
		{"registration state missing", func(r *Reginfo) { r.Registrations[0].State = "" }},
		{"registration state invalid", func(r *Reginfo) { r.Registrations[0].State = "pending" }},
		{"contact state missing", func(r *Reginfo) { r.Registrations[0].Contacts[0].State = "" }},
		{"contact state init", func(r *Reginfo) { r.Registrations[0].Contacts[0].State = Init }},
		{"contact event missing", func(r *Reginfo) { r.Registrations[0].Contacts[0].Event = "" }},
		{"contact event invalid", func(r *Reginfo) { r.Registrations[0].Contacts[0].Event = "bogus" }},
		{"shortened without expires", func(r *Reginfo) {
			r.Registrations[0].Contacts[1].Event = Shortened
		}},
		{"probation without retry-after", func(r *Reginfo) {
			r.Registrations[0].Contacts[1].Event = Probation
		}},
		{"contact uri missing", func(r *Reginfo) { r.Registrations[0].Contacts[0].URI = "" }},
		{"invalid character", func(r *Reginfo) { r.Registrations[0].Contacts[0].CallID = "\x00" }},
	} {
		r := full()
		c.mutate(&r)

		if b, err := Encode(r); err == nil {
			t.Errorf("%s: %s", c.name, b)
		}
	}
}

// RFC 3680 §5.1, §5.4: documents not valid in what is read are refused, whole: of a contact skipped, a full document
// would tell a subscriber it is gone (§5.2).
func TestDecodeInvalid(t *testing.T) {
	const ns = `xmlns="urn:ietf:params:xml:ns:reginfo"`

	reg := func(contact string) string {
		return `<reginfo ` + ns + ` version="1" state="full"><registration aor="sip:a@x" id="r" state="active">` +
			`<contact id="1" state="active" event="registered"><uri>sip:a@good</uri></contact>` +
			contact + `</registration></reginfo>`
	}

	for _, c := range []struct{ name, doc, want string }{
		{"not XML", "<reginfo", "reginfo: "},
		{"another root", `<presence xmlns="urn:ietf:params:xml:ns:pidf"/>`, "root element"},
		{"another namespace", `<reginfo xmlns="urn:example" version="0" state="full"/>`, "root element"},
		{"no namespace", `<reginfo version="0" state="full"/>`, "root element"},
		{"not UTF-8", `<?xml version="1.0" encoding="ISO-8859-1"?><reginfo ` + ns + ` version="0" state="full"/>`, "reginfo: "},
		{"no version", `<reginfo ` + ns + ` state="full"/>`, "without version"},
		{"version", `<reginfo ` + ns + ` version="x" state="full"/>`, "version"},
		{"version of 33 bits", `<reginfo ` + ns + ` version="4294967296" state="full"/>`, "version"},
		{"no state", `<reginfo ` + ns + ` version="0"/>`, "state"},
		{"state", `<reginfo ` + ns + ` version="0" state="all"/>`, "state"},
		{"element of the namespace", `<reginfo ` + ns + ` version="0" state="full"><contact/></reginfo>`, "<contact> in <reginfo>"},
		{"no aor", `<reginfo ` + ns + ` version="0" state="full"><registration id="r" state="active"/></reginfo>`, "without aor"},
		{"prefixed aor", `<reginfo ` + ns + ` xmlns:x="urn:x" version="0" state="full"><registration x:aor="sip:a@x" id="r" state="active"/></reginfo>`, "without aor"},
		{"no id", `<reginfo ` + ns + ` version="0" state="full"><registration aor="sip:a@x" state="active"/></reginfo>`, "without id"},
		{"no registration state", `<reginfo ` + ns + ` version="0" state="full"><registration aor="sip:a@x" id="r"/></reginfo>`, "state"},
		{"registration state", `<reginfo ` + ns + ` version="0" state="full"><registration aor="sip:a@x" id="r" state="gone"/></reginfo>`, "state"},
		{"element of the namespace in a registration", reg(`<uri>sip:c@x</uri>`), "<uri> in <registration>"},
		{"contact without id", reg(`<contact state="active" event="registered"><uri>sip:c@x</uri></contact>`), "without id"},
		{"no contact state", reg(`<contact id="c" event="registered"><uri>sip:c@x</uri></contact>`), "state"},
		{"contact state", reg(`<contact id="c" state="init" event="registered"><uri>sip:c@x</uri></contact>`), "state"},
		{"no event", reg(`<contact id="c" state="active"><uri>sip:c@x</uri></contact>`), "event"},
		{"event", reg(`<contact id="c" state="active" event="moved"><uri>sip:c@x</uri></contact>`), "event"},
		{"expires", reg(`<contact id="c" state="active" event="refreshed" expires="-1"><uri>sip:c@x</uri></contact>`), "expires"},
		{"expires of 65 bits", reg(`<contact id="c" state="active" event="refreshed" expires="18446744073709551616"><uri>sip:c@x</uri></contact>`), "expires"},
		{"duration-registered", reg(`<contact id="c" state="active" event="refreshed" duration-registered="1h"><uri>sip:c@x</uri></contact>`), "duration-registered"},
		{"cseq", reg(`<contact id="c" state="active" event="refreshed" cseq="x"><uri>sip:c@x</uri></contact>`), "cseq"},
		{"shortened", reg(`<contact id="c" state="active" event="shortened"><uri>sip:c@x</uri></contact>`), "without expires"},
		{"probation", reg(`<contact id="c" state="terminated" event="probation"><uri>sip:c@x</uri></contact>`), "without retry-after"},
		{"no uri", reg(`<contact id="c" state="active" event="registered"/>`), "0 <uri>"},
		{"empty uri", reg(`<contact id="c" state="active" event="registered"><uri> </uri></contact>`), "1 <uri>"},
		{"two uris", reg(`<contact id="c" state="active" event="registered"><uri>sip:c@x</uri><uri>sip:d@x</uri></contact>`), "2 <uri>"},
		{"two display names", reg(`<contact id="c" state="active" event="registered"><uri>sip:c@x</uri><display-name>a</display-name><display-name>b</display-name></contact>`), "2 <display-name>"},
		{"param without name", reg(`<contact id="c" state="active" event="registered"><uri>sip:c@x</uri><unknown-param>v</unknown-param></contact>`), "without name"},
		{"element of the namespace in a contact", reg(`<contact id="c" state="active" event="registered"><uri>sip:c@x</uri><foo/></contact>`), "<foo>"},
		{"too large", `<reginfo ` + ns + ` version="0" state="full">` + strings.Repeat(" ", maxSize) + `</reginfo>`, "exceeds"},
	} {
		if r, err := Decode([]byte(c.doc)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %+v, %v, want %q", c.name, r, err, c.want)
		}
	}
}

func TestDecodeLargeNumbers(t *testing.T) {
	doc := `<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="4294967295" state="full">` +
		`<registration aor="sip:a@b" id="1" state="active">` +
		`<contact id="2" state="active" event="registered" expires="4294967296" retry-after="+18446744073709551615" cseq=" 4294967295 ">` +
		`<uri>sip:a@c</uri></contact></registration></reginfo>`

	r, err := Decode([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}

	c := r.Registrations[0].Contacts[0]
	if r.Version != 4294967295 || *c.Expires != 4294967296 || *c.RetryAfter != 18446744073709551615 || *c.CSeq != 4294967295 {
		t.Errorf("%d %+v", r.Version, c)
	}
}

const extended = `<?xml version="1.0" encoding="US-ASCII"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo"
         xmlns:gr="urn:ietf:params:xml:ns:gruuinfo"
         xmlns:ere="urn:3gpp:ns:extRegExp:1.0"
         version="0" state="full" ere:x="ignored">
  <registration aor="sip:user1_public1@home1.net" id="a7" state="active">
    <contact id="76" state="active" event="registered" gr:extra="ignored">
      <uri>
        sip:[5555::aaa:bbb:ccc:ddd];comp=sigcomp
      </uri>
      <unknown-param name="+sip.instance">"&lt;urn:gsma:imei:90420156-025763-0&gt;"</unknown-param>
      <gr:pub-gruu uri="sip:user1_public1@home1.net;gr=urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"/>
      <gr:temp-gruu uri="sip:tgruu.7hs==jd7vnzga5w7fajsc7-ajd6fabz0f8g5@example.com;gr" first-cseq="54301"/>
    </contact>
    <ere:wildcardedIdentity>sip:user!.*!@home1.net</ere:wildcardedIdentity>
    <other:contact xmlns:other="urn:other" id="99" state="active" event="registered">
      <other:uri>sip:ignored@example.com</other:uri>
    </other:contact>
  </registration>
  <ere:registration aor="sip:ignored@home1.net" id="x" state="active"/>
</reginfo>`

// The elements and attributes of other namespaces are ignored (§5.1): TS 24.229 §5.4.2.1.2's GRUUs and wildcarded
// identities among them.
func TestDecodeIgnoresExtensions(t *testing.T) {
	r, err := Decode([]byte(extended))
	if err != nil {
		t.Fatal(err)
	}

	want := Reginfo{State: Full, Registrations: []Registration{{
		AOR: "sip:user1_public1@home1.net", ID: "a7", State: Active, Contacts: []Contact{{
			ID: "76", State: Active, Event: Registered, URI: "sip:[5555::aaa:bbb:ccc:ddd];comp=sigcomp",
			UnknownParams: []Param{{Name: "+sip.instance", Value: `"<urn:gsma:imei:90420156-025763-0>"`}},
		}},
	}}}

	if !reflect.DeepEqual(r, want) {
		t.Fatalf("\n%+v\nwant\n%+v", r, want)
	}
}

func sipBody(t *testing.T, path string) []byte {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, sep := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n")} {
		if i := bytes.Index(b, sep); i >= 0 {
			return b[i+len(sep):]
		}
	}

	t.Fatalf("%s: no message body", path)

	return nil
}

const corpus = "../internal/corpus/testdata"

// The NOTIFYs of the corpus, of Ella IMS's S-CSCF to live UEs.
func TestDecodeCorpus(t *testing.T) {
	var files []string

	err := filepath.WalkDir(corpus, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, "-NOTIFY.sip") && !strings.HasSuffix(p, "-200-NOTIFY.sip") {
			files = append(files, p)
		}

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	n := 0

	for _, f := range files {
		body := sipBody(t, f)
		if !bytes.Contains(body, []byte("<reginfo")) {
			continue
		}

		n++

		r, err := Decode(body)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}

		if r.State != Full || len(r.Registrations) != 2 || !strings.HasPrefix(r.Registrations[1].AOR, "tel:") {
			t.Errorf("%s: %+v, want the full state of an IMPU and its tel URI alias", f, r)
		}

		for _, reg := range r.Registrations {
			if reg.State != Active || len(reg.Contacts) == 0 {
				t.Errorf("%s: registration %+v", f, reg)
			}

			for _, c := range reg.Contacts {
				if v, _ := c.Param("+sip.instance"); c.Expires == nil || !strings.HasPrefix(v, `"<urn:gsma:imei:`) {
					t.Errorf("%s: contact %+v", f, c)
				}
			}
		}

		roundTrip(t, r)
	}

	if n == 0 {
		t.Fatal("no reginfo NOTIFY in the corpus")
	}
}

// A re-registration from a new address: the old contact ends in the same NOTIFY.
func TestDecodeCorpusValues(t *testing.T) {
	r, err := Decode(sipBody(t, corpus+"/ella/live/4g/motorola-xt2417/reregister/004-NOTIFY.sip"))
	if err != nil {
		t.Fatal(err)
	}

	if r.Version != 1 || r.State != Full {
		t.Errorf("version/state = %d/%s", r.Version, r.State)
	}

	reg := r.Registrations[0]
	if reg.AOR != "sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org" || reg.ID != "r57c0916cf3867058" ||
		r.Registrations[1].AOR != "tel:15550000001" || len(reg.Contacts) != 2 {
		t.Fatalf("registrations = %+v", r.Registrations)
	}

	c := reg.Contacts[0]
	if c.ID != "cb0dae370f17df2d7" || c.State != Active || c.Event != Created || *c.Expires != 3600 ||
		c.URI != "sip:1b4261b8-485f-4f9e-a1af-589ac5cae854@10.46.0.7:41498" {
		t.Errorf("contact = %+v", c)
	}

	if old := reg.Contacts[1]; old.State != Terminated || old.Event != Unregistered || *old.Expires != 0 ||
		old.URI != "sip:52b461eb-953e-4a68-a0fb-6d71b4ed2da6@10.46.0.6:43472" {
		t.Errorf("old contact = %+v", old)
	}

	want := []Param{
		{Name: "+g.3gpp.accesstype", Value: `"cellular2"`},
		{Name: "+sip.instance", Value: `"<urn:gsma:imei:00000000-000001-0>"`},
		{Name: "+g.3gpp.ps-data-off", Value: `"inactive"`},
		{Name: "audio"},
		{Name: "+g.3gpp.nw-init-ussi"},
		{Name: "+g.3gpp.smsip"},
		{Name: "video"},
		{Name: "+g.3gpp.icsi-ref", Value: `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`},
	}

	if !reflect.DeepEqual(c.UnknownParams, want) {
		t.Errorf("unknown params = %+v", c.UnknownParams)
	}
}

func FuzzDecode(f *testing.F) {
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

	f.Add([]byte(extended))

	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := Decode(b)
		if err != nil {
			return
		}

		// What decodes is of the values of the schema, and encodes to a document that decodes the same.
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

		roundTrip(t, r)
	})
}
