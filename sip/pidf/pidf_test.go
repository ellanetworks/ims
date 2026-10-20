package pidf

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip/xmltree"
)

func decodeFile(t *testing.T, name string) Presence {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	p, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

// stable checks that p encodes, decodes to what encodes again the same: the elements kept, malformed, too.
func stable(t *testing.T, p Presence) {
	t.Helper()

	b, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}

	again, err := Decode(b)
	if err != nil && !errors.Is(err, ErrValue) {
		t.Fatalf("Decode(Encode): %v\n%s", err, b)
	}

	b2, err := Encode(again)
	if err != nil || string(b2) != string(b) {
		t.Fatalf("Encode not stable:\n%s\n%s, %v", b, b2, err)
	}
}

// RFC 3863 §4.3.1: status extensions, contact priority, notes in languages, a timestamp, a note of the presentity.
func TestExample431(t *testing.T) {
	p := decodeFile(t, "rfc3863-4.3.1.xml")

	if p.Entity != "pres:someone@example.com" || len(p.Tuples) != 2 || !reflect.DeepEqual(p.Notes, []Note{{Text: "I'll be in Tokyo next week"}}) {
		t.Fatalf("%+v", p)
	}

	t0 := p.Tuples[0]
	if t0.ID != "bs35r9" || t0.Status.Basic != Open || len(t0.Status.Other) != 2 || t0.Status.Other[0].Name.Local != "im" {
		t.Errorf("tuple %+v", t0)
	}

	if t0.Contact.URI != "im:someone@mobilecarrier.net" || *t0.Contact.Priority != 800 {
		t.Errorf("contact %+v", t0.Contact)
	}

	if !reflect.DeepEqual(t0.Notes, []Note{{"Don't Disturb Please!", "en"}, {"Ne derangez pas, s'il vous plait", "fr"}}) {
		t.Errorf("notes %+v", t0.Notes)
	}

	if !t0.Timestamp.Equal(time.Date(2001, 10, 27, 16, 49, 29, 0, time.UTC)) {
		t.Errorf("timestamp %s", t0.Timestamp)
	}

	if *p.Tuples[1].Contact.Priority != 1000 {
		t.Errorf("priority %d", *p.Tuples[1].Contact.Priority)
	}

	stable(t, p)
}

// RFC 3863 §4.3.2: a prefixed namespace; extensions of the tuple and of the presence; a contact's white space.
func TestExample432(t *testing.T) {
	p := decodeFile(t, "rfc3863-4.3.2.xml")

	if len(p.Tuples[0].Other) != 1 || p.Tuples[0].Other[0].Name.Local != "mytupletag" || len(p.Other) != 1 ||
		p.Tuples[1].Contact.URI != "im:someone@mobilecarrier.net" || *p.Tuples[0].Contact.Priority != 650 {
		t.Fatalf("%+v", p)
	}

	stable(t, p)
}

// RFC 3863 §4.2.3, §4.3.3: mustUnderstand on an element nested in an extension.
func TestExample433(t *testing.T) {
	p := decodeFile(t, "rfc3863-4.3.3.xml")

	ext := p.Tuples[0].Other[0]
	if ext.Name.Local != "complexExtension" || MustUnderstand(ext) {
		t.Fatalf("%+v", ext)
	}

	if es := ext.Elements(); !MustUnderstand(es[0]) || MustUnderstand(es[1]) {
		t.Errorf("mustUnderstand of ex1, ex2: %v, %v", MustUnderstand(es[0]), MustUnderstand(es[1]))
	}

	stable(t, p)
}

// TS 24.379 §9.3.1.2: tuples of status extensions only, no <basic> (RFC 3863 §4.1.3); extensions of the presence.
func TestMCPTT(t *testing.T) {
	for _, f := range []string{"ts24379-user-affiliation.xml", "ts24379-group-dynamic.xml"} {
		p := decodeFile(t, f)

		for _, tu := range p.Tuples {
			if tu.Status.Basic != "" || len(tu.Status.Other) == 0 || tu.Status.Other[0].Name.Space != "urn:3gpp:ns:mcpttPresInfo:1.0" {
				t.Errorf("%s: tuple %+v", f, tu)
			}
		}

		if len(p.Other) == 0 {
			t.Errorf("%s: no extension of the presence", f)
		}

		stable(t, p)
	}

	// §9.3.1.2 5: the tuple id is the MCPTT client ID, a URN: not an xs:ID, an arbitrary string (RFC 3863 §4.1.2).
	p, err := Decode([]byte(`<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="sip:a@x">` +
		`<tuple id="urn:uuid:00000000-0000-1000-8000-0123456789ab"><status><basic>open</basic></status></tuple></presence>`))
	if err != nil || p.Tuples[0].ID != "urn:uuid:00000000-0000-1000-8000-0123456789ab" {
		t.Errorf("%+v, %v", p, err)
	}

	stable(t, p)
}

// RFC 3863 §4.4: a malformed element kept, not decoded; the first of an element given once counting.
func TestMalformed(t *testing.T) {
	for _, c := range []struct {
		name, tuple string
		check       func(Presence) bool
	}{
		{"basic", `<tuple id="t"><status><basic>away</basic></status></tuple>`, func(p Presence) bool {
			return len(p.Tuples) == 1 && p.Tuples[0].Status.Basic == "" && p.Tuples[0].Status.Other[0].Name.Local == "basic"
		}},
		{"timestamp", `<tuple id="t"><status><basic>open</basic></status><timestamp>yesterday</timestamp></tuple>`, func(p Presence) bool {
			return p.Tuples[0].Timestamp == nil && p.Tuples[0].Other[0].Name.Local == "timestamp"
		}},
		{"tuple without status", `<tuple id="t"/>`, func(p Presence) bool {
			return len(p.Tuples) == 0 && p.Other[0].Name.Local == "tuple"
		}},
		{"empty status", `<tuple id="t"><status/></tuple>`, func(p Presence) bool {
			return len(p.Tuples) == 0 && len(p.Other) == 1
		}},
		{"tuple without id", `<tuple><status><basic>open</basic></status></tuple>`, func(p Presence) bool {
			return len(p.Tuples) == 0 && len(p.Other) == 1
		}},
		{"tuple id twice", `<tuple id="t"><status><basic>open</basic></status></tuple><tuple id="t"><status><basic>closed</basic></status></tuple>`, func(p Presence) bool {
			return len(p.Tuples) == 1 && p.Tuples[0].Status.Basic == Open && len(p.Other) == 1
		}},
		{"elements in a note", `<note><b/></note>`, func(p Presence) bool {
			return len(p.Notes) == 0 && len(p.Other) == 1
		}},
	} {
		p, err := Decode([]byte(`<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="pres:a@x">` + c.tuple + `</presence>`))
		if !errors.Is(err, ErrValue) || !c.check(p) {
			t.Errorf("%s: %+v, %v", c.name, p, err)
			continue
		}

		stable(t, p)
	}

	// Given more than once: the first counts, the others kept, malformed.
	p, err := Decode([]byte(`<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="pres:a@x"><tuple id="t">` +
		`<status><basic>open</basic><basic>closed</basic></status><status><basic>closed</basic></status>` +
		`<contact>a</contact><contact>b</contact></tuple></presence>`))
	if !errors.Is(err, ErrValue) || p.Tuples[0].Status.Basic != Open || len(p.Tuples[0].Status.Other) != 1 || p.Tuples[0].Contact.URI != "a" ||
		len(p.Tuples[0].Other) != 2 {
		t.Errorf("%+v, %v", p, err)
	}
}

// RFC 3863 §4.1.7, RFC 3339 §5.6, §5.7: a timestamp has a timezone; a leap second is one.
func TestTimestamp(t *testing.T) {
	for in, ok := range map[string]bool{
		"2001-10-27T16:49:29Z": true, "2016-12-31T23:59:60Z": true, "2001-10-27T16:49:29.5+09:00": true,
		"2001-10-27T16:49:29": false, "2001-10-27": false,
	} {
		p, err := Decode([]byte(`<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="pres:a@x"><tuple id="t">` +
			`<status><basic>open</basic></status><timestamp>` + in + `</timestamp></tuple></presence>`))
		if ok != (err == nil && p.Tuples[0].Timestamp != nil) {
			t.Errorf("%q: %+v, %v", in, p.Tuples, err)
		}
	}

	ts := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if b, err := Encode(Presence{Entity: "pres:a@x", Tuples: []Tuple{{ID: "t", Status: Status{Basic: Open}, Timestamp: &ts}}}); err == nil {
		t.Errorf("year 10000 encoded: %s", b)
	}
}

// RFC 3863 §4.1.5: a priority out of range, or not of the schema's qvalue, ignored.
func TestPriority(t *testing.T) {
	for in, want := range map[string]int{
		"0": 0, "0.": 0, "0.021": 21, "0.5": 500, "1": 1000, "1.00": 1000, "1.000": 1000,
		"1.5": -1, "2": -1, "0.1234": -1, "-0.1": -1, "0.x": -1, ".5": -1, "": -1,
	} {
		q := parseQValue(in)
		if want < 0 && q != nil || want >= 0 && (q == nil || int(*q) != want) {
			t.Errorf("%q: %v", in, q)
		}
	}

	for q, want := range map[uint16]string{0: "0", 21: "0.021", 500: "0.5", 800: "0.8", 1000: "1"} {
		if got := formatQValue(q); got != want {
			t.Errorf("%d: %q", q, got)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		``,
		`<presence xmlns="urn:ietf:params:xml:ns:pidf"/>`,
		`<presence xmlns="urn:ietf:params:xml:ns:pidf" entity=" "/>`,
		`<presence entity="pres:a@x"/>`,
		`<tuple xmlns="urn:ietf:params:xml:ns:pidf" entity="pres:a@x"/>`,
		`<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="pres:a@x">` + strings.Repeat(" ", maxSize) + `</presence>`,
	} {
		if p, err := Decode([]byte(in)); err == nil || errors.Is(err, ErrValue) {
			t.Errorf("Decode(%.60q) = %+v, %v", in, p, err)
		}
	}
}

func TestEncode(t *testing.T) {
	q := uint16(500)
	ts := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	p := Presence{
		Entity: "pres:a@x",
		Tuples: []Tuple{{
			ID:      "t1",
			Status:  Status{Basic: Closed, Other: []*xmltree.Element{xmltree.New("urn:x", "busy")}},
			Contact: &Contact{URI: "sip:a@x", Priority: &q}, Notes: []Note{{"away", "en"}}, Timestamp: &ts,
		}},
		Other: []*xmltree.Element{xmltree.New("urn:x", "ext")},
	}

	b, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="pres:a@x">` +
		`<tuple id="t1"><status><basic>closed</basic><busy xmlns="urn:x"/></status><contact priority="0.5">sip:a@x</contact>` +
		`<note xml:lang="en">away</note><timestamp>2026-10-20T08:00:00Z</timestamp></tuple><ext xmlns="urn:x"/></presence>`
	if string(b) != want {
		t.Errorf("Encode\n%s\nwant\n%s", b, want)
	}

	for _, p := range []Presence{
		{},
		{Entity: "pres:a@x", Tuples: []Tuple{{ID: "t"}}},
		{Entity: "pres:a@x", Tuples: []Tuple{{Status: Status{Basic: Open}}}},
		{Entity: "pres:a@x", Tuples: []Tuple{{ID: "t", Status: Status{Basic: Open}}, {ID: "t", Status: Status{Basic: Open}}}},
		{Entity: "pres:a@x", Tuples: []Tuple{{ID: "t", Status: Status{Basic: "away"}}}},
	} {
		if b, err := Encode(p); err == nil {
			t.Errorf("Encode(%+v) = %s", p, b)
		}
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

	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Decode(b)
		if err != nil && !errors.Is(err, ErrValue) {
			return
		}

		stable(t, p)

		// A document without error encodes into one without error.
		if err == nil {
			out, _ := Encode(p)
			if _, err := Decode(out); err != nil {
				t.Fatalf("%q decoded without error, encoded to %q: %v", b, out, err)
			}
		}
	})
}
