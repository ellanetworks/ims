package resourcelists

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip/xmltree"
)

func decodeFile(t *testing.T, name string) ResourceLists {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	r, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

// stable checks that r encodes, decodes to what encodes again the same: the elements kept, malformed, too.
func stable(t *testing.T, r ResourceLists) {
	t.Helper()

	b, err := Encode(r)
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

// RFC 4826 §3.3: a list of an entry, an entry reference and a list of entries and an external list. The line folding
// of the example, for display (§3.3), is white space in the attribute values (XML 1.0 §3.3.3), kept.
func TestExample33(t *testing.T) {
	r := decodeFile(t, "rfc4826-3.3.xml")

	friends := r.Lists[0]
	if len(r.Lists) != 1 || friends.Name != "friends" || len(friends.Members) != 3 {
		t.Fatalf("%+v", r)
	}

	if e := friends.Entries(); len(e) != 1 || e[0].URI != "sip:bill@example.com" || e[0].DisplayName != (DisplayName{Text: "Bill Doe"}) {
		t.Errorf("entries %+v", e)
	}

	unfold := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if ref := friends.Members[1].(*EntryRef); unfold(ref.Ref) !=
		"resource-lists/users/sip:bill@example.com/index/~~/resource-lists/list%5b@name=%22list1%22%5d/entry%5b@uri=%22sip:petri@example.com%22%5d" {
		t.Errorf("entry-ref %q", ref.Ref)
	}

	closeFriends := friends.Members[2].(*List)
	if closeFriends.Name != "close-friends" || closeFriends.DisplayName.Text != "Close Friends" || len(closeFriends.Entries()) != 2 ||
		closeFriends.Entries()[1].DisplayName.Text != "Nancy Gross" {
		t.Errorf("list %+v", closeFriends)
	}

	if x := closeFriends.Members[2].(*External); unfold(x.Anchor) !=
		"http://xcap.example.org/resource-lists/users/sip:a@example.org/index/~~/resource-lists/list%5b@name=%22mkting%22%5d" ||
		x.DisplayName.Text != "Marketing" {
		t.Errorf("external %+v", x)
	}

	stable(t, r)
}

// RFC 5364 §6 Figure 3: a recipient list; Figure 4: the recipient-history list of the URI-list server, of counts.
func TestCopyControl(t *testing.T) {
	r := decodeFile(t, "rfc5364-figure3.xml")

	var got []Copy
	for _, e := range r.Lists[0].Entries() {
		got = append(got, e.Copy)

		if len(e.Attr) != 0 {
			t.Errorf("%s: attributes %+v", e.URI, e.Attr)
		}
	}

	want := []Copy{{CopyControl: To}, {To, true, nil}, {To, true, nil}, {CopyControl: CC}, {CC, true, nil}, {CopyControl: BCC}, {CopyControl: BCC}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%+v", got)
	}

	stable(t, r)

	r = decodeFile(t, "rfc5364-figure4.xml")

	es := r.Lists[0].Entries()
	if len(es) != 4 || es[1].URI != "sip:anonymous@anonymous.invalid" || es[1].CopyControl != To || *es[1].Count != 2 ||
		es[3].CopyControl != CC || *es[3].Count != 1 || es[0].Count != nil {
		t.Errorf("%+v", es)
	}

	stable(t, r)

	b, err := Encode(r)
	if err != nil || !strings.Contains(string(b), `<entry uri="sip:anonymous@anonymous.invalid" xmlns:ns1="urn:ietf:params:xml:ns:copycontrol" ns1:copyControl="to" ns1:count="2"/>`) {
		t.Errorf("%s, %v", b, err)
	}
}

// RFC 5366 §4 Figure 1: the flat list of a URI list. Figure 3 gives the namespace of RFC 5364 as
// urn:ietf:params:xml:ns:copyControl, which is not that of RFC 5364 §5 and §9.2, urn:ietf:params:xml:ns:copycontrol:
// its attributes are extensions of a namespace unknown, kept.
func TestURIList(t *testing.T) {
	r := decodeFile(t, "rfc5366-figure1.xml")

	if es := r.Lists[0].Entries(); len(es) != 3 || es[0].URI != "sip:bill@example.com" || es[0].CopyControl != To ||
		es[1].CopyControl != CC || es[2].CopyControl != BCC {
		t.Errorf("%+v", es)
	}

	stable(t, r)

	r = decodeFile(t, "rfc5366-figure3.xml")

	es := r.Lists[0].Entries()
	if len(es) != 7 || es[1].CopyControl != "" || len(es[1].Attr) != 2 || es[1].Attr[0].Name.Space != "urn:ietf:params:xml:ns:copyControl" {
		t.Errorf("%+v", es)
	}

	stable(t, r)
}

// TS 24.379 §10.1.4.2.1, §17.5: the functional alias of an entry kept in its Other.
func TestMCPTT(t *testing.T) {
	r := decodeFile(t, "ts24379-10.1.4.2.1.xml")

	es := r.Lists[0].Entries()
	if len(es) != 1 || es[0].URI != "sip:bob@mcptt.example.com" || len(es[0].Other) != 1 ||
		es[0].Other[0].Name != (xml.Name{Space: "urn:3gpp:ns:mcpttResLists:1.0", Local: "functional-alias"}) {
		t.Fatalf("%+v", es)
	}

	stable(t, r)
}

// RFC 4826 §3.2, §6, RFC 5364 §5: the members of each kind, the extension elements and attributes, the copy control
// attributes of members other than entries, entries of a URI twice (RFC 5364 §4), which Encode refuses (RFC 4826
// §3.1).
func TestExtensions(t *testing.T) {
	r := decodeFile(t, "extensions.xml")

	a := r.Lists[0]
	if len(r.Lists) != 2 || a.Name != "a" || a.DisplayName != (DisplayName{"Liste A", "fr"}) || len(a.Members) != 5 ||
		len(a.Attr) != 1 || len(a.Other) != 2 {
		t.Fatalf("%+v", a)
	}

	one := a.Members[0].(*Entry)
	if one.DisplayName != (DisplayName{"One", "en"}) || one.CopyControl != To || !one.Anonymize || *one.Count != 0 ||
		len(one.Attr) != 1 || len(one.Other) != 1 {
		t.Errorf("entry %+v", one)
	}

	if twice := a.Members[1].(*Entry); twice.URI != one.URI || twice.CopyControl != CC || twice.Anonymize {
		t.Errorf("entry %+v", twice)
	}

	if ref := a.Members[2].(*EntryRef); ref.CopyControl != CC || ref.DisplayName.Text != "Ref" || len(ref.Other) != 1 {
		t.Errorf("entry-ref %+v", ref)
	}

	b := a.Members[3].(*List)
	if x := b.Members[0].(*External); b.CopyControl != BCC || *x.Count != 18446744073709551615 || x.DisplayName != (DisplayName{}) ||
		len(x.Other) != 1 || len(b.Other) != 1 {
		t.Errorf("list %+v", b)
	}

	if b, err := Encode(r); !errors.Is(err, errDuplicate) {
		t.Errorf("%s, %v", b, err)
	}

	r.Lists[0].Members = slices.Delete(r.Lists[0].Members, 1, 2)
	stable(t, r)
}

// RFC 4826 §3.2: a malformed element kept, not decoded; the first of an element given once counting.
func TestMalformed(t *testing.T) {
	for _, c := range []struct {
		name, list string
		check      func(ResourceLists) bool
	}{
		{"entry without uri", `<entry/>`, func(r ResourceLists) bool {
			return len(r.Lists[0].Members) == 0 && len(r.Lists[0].Other) == 1
		}},
		{"entry-ref without ref", `<entry-ref/>`, func(r ResourceLists) bool { return len(r.Lists[0].Other) == 1 }},
		{"external without anchor", `<external/>`, func(r ResourceLists) bool { return len(r.Lists[0].Other) == 1 }},
		{"copyControl", `<entry uri="sip:a@x" cp:copyControl="from"/>`, func(r ResourceLists) bool { return len(r.Lists[0].Other) == 1 }},
		{"anonymize", `<entry uri="sip:a@x" cp:anonymize="yes"/>`, func(r ResourceLists) bool { return len(r.Lists[0].Other) == 1 }},
		{"count", `<entry uri="sip:a@x" cp:count="-1"/>`, func(r ResourceLists) bool { return len(r.Lists[0].Other) == 1 }},
		{"count too large", `<entry uri="sip:a@x" cp:count="18446744073709551616"/>`, func(r ResourceLists) bool { return len(r.Lists[0].Other) == 1 }},
		{
			"count of a sign", `<entry uri="sip:a@x" cp:count="-1"/><entry uri="sip:b@x" cp:count="+-0"/><entry uri="sip:c@x" cp:count="+"/>`,
			func(r ResourceLists) bool { return len(r.Lists[0].Other) == 3 },
		},
		{"list", `<list cp:copyControl="x"/>`, func(r ResourceLists) bool { return len(r.Lists[0].Other) == 1 }},
		{"display-name", `<entry uri="sip:a@x"><display-name><b/></display-name></entry>`, func(r ResourceLists) bool {
			e := r.Lists[0].Members[0].(*Entry)
			return e.DisplayName == (DisplayName{}) && len(e.Other) == 1
		}},
	} {
		r, err := Decode([]byte(`<resource-lists xmlns="urn:ietf:params:xml:ns:resource-lists" xmlns:cp="urn:ietf:params:xml:ns:copycontrol">` +
			`<list>` + c.list + `</list></resource-lists>`))
		if !errors.Is(err, ErrValue) || !c.check(r) {
			t.Errorf("%s: %+v, %v", c.name, r, err)
			continue
		}

		stable(t, r)
	}

	// Given more than once: the first counts, malformed or not, the others kept, malformed.
	r, err := Decode([]byte(`<resource-lists xmlns="urn:ietf:params:xml:ns:resource-lists"><list><display-name>a</display-name>` +
		`<display-name>b</display-name><entry uri="u"><display-name><b/></display-name><display-name>d</display-name></entry></list>` +
		`</resource-lists>`))
	if e := r.Lists[0].Entries(); !errors.Is(err, ErrValue) || r.Lists[0].DisplayName.Text != "a" || len(r.Lists[0].Other) != 1 ||
		e[0].DisplayName.Text != "" || len(e[0].Other) != 2 {
		t.Errorf("%+v, %v", r, err)
	}

	stable(t, r)

	// A display name of no text nor language is as none: one may follow. Elements other than lists kept at the root.
	r, err = Decode([]byte(`<resource-lists xmlns="urn:ietf:params:xml:ns:resource-lists"><list><display-name/>` +
		`<display-name>b</display-name></list><entry uri="v"/></resource-lists>`))
	if err != nil || r.Lists[0].DisplayName.Text != "b" || len(r.Lists[0].Other) != 0 || len(r.Other) != 1 {
		t.Errorf("%+v, %v", r, err)
	}

	stable(t, r)
}

// XML Schema Part 2 §3.3.20.1: count is an xs:nonNegativeInteger, of a sign or not.
func TestCount(t *testing.T) {
	r, err := Decode([]byte(`<resource-lists xmlns="urn:ietf:params:xml:ns:resource-lists" xmlns:cp="urn:ietf:params:xml:ns:copycontrol">` +
		`<list><entry uri="a" cp:count="+5"/><entry uri="b" cp:count="-00"/><entry uri="c" cp:count=" 007 "/></list></resource-lists>`))
	if es := r.Lists[0].Entries(); err != nil || len(es) != 3 || *es[0].Count != 5 || *es[1].Count != 0 || *es[2].Count != 7 {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		``,
		`<resource-lists/>`,
		`<list xmlns="urn:ietf:params:xml:ns:resource-lists"/>`,
		`<resource-lists xmlns="urn:ietf:params:xml:ns:resource-lists">` + strings.Repeat(" ", maxSize) + `</resource-lists>`,
	} {
		if r, err := Decode([]byte(in)); err == nil || errors.Is(err, ErrValue) {
			t.Errorf("Decode(%.60q) = %+v, %v", in, r, err)
		}
	}
}

func TestEncode(t *testing.T) {
	two := uint64(2)
	r := ResourceLists{Lists: []List{{
		Members: []Member{
			&Entry{URI: "sip:a@x", DisplayName: DisplayName{"A", "en"}, Copy: Copy{CopyControl: To}},
			&Entry{URI: "sip:anonymous@anonymous.invalid", Copy: Copy{CopyControl: CC, Count: &two}},
			&List{Name: "l", Members: []Member{&EntryRef{Ref: "r"}, &External{Anchor: "http://x/"}}},
		},
		Other: []*xmltree.Element{xmltree.New("urn:x", "ext")},
	}}}

	b, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<resource-lists xmlns="urn:ietf:params:xml:ns:resource-lists"><list>` +
		`<entry uri="sip:a@x" xmlns:ns0="urn:ietf:params:xml:ns:copycontrol" ns0:copyControl="to"><display-name xml:lang="en">A</display-name></entry>` +
		`<entry uri="sip:anonymous@anonymous.invalid" xmlns:ns1="urn:ietf:params:xml:ns:copycontrol" ns1:copyControl="cc" ns1:count="2"/>` +
		`<list name="l"><entry-ref ref="r"/><external anchor="http://x/"/></list><ext xmlns="urn:x"/></list></resource-lists>`
	if string(b) != want {
		t.Errorf("Encode\n%s\nwant\n%s", b, want)
	}

	var nilEntry *Entry

	for _, l := range []List{
		{Members: []Member{nil}},
		{Members: []Member{nilEntry}},
		{Members: []Member{&Entry{Copy: Copy{CopyControl: "from"}}}},
		{Members: []Member{&Entry{Attr: []xml.Attr{{Name: xml.Name{Local: "uri"}}}}}},
		{Members: []Member{&Entry{Attr: []xml.Attr{{Name: xml.Name{Space: CopyControlNamespace, Local: "count"}}}}}},
		{Attr: []xml.Attr{{Name: xml.Name{Space: Namespace, Local: "a"}}}},
		{Members: []Member{&Entry{URI: "u"}, &Entry{URI: "u", Copy: Copy{CopyControl: To}}}},
		{Members: []Member{&EntryRef{Ref: "r"}, &EntryRef{Ref: "r"}}},
		{Members: []Member{&External{Anchor: "http://x/"}, &External{Anchor: "http://x/"}}},
		{Members: []Member{&List{Name: "l"}, &List{Name: "l"}}},
		{Members: []Member{&Entry{URI: AnonymousURI}, &Entry{URI: AnonymousURI, Copy: Copy{CopyControl: BCC}}}},
	} {
		if b, err := Encode(ResourceLists{Lists: []List{l}}); err == nil {
			t.Errorf("Encode(%+v) = %s", l, b)
		}
	}

	// The keys of different kinds, of lists of no name, of AnonymousURI of different levels (RFC 5364 Figure 4) do not
	// clash; the names of the lists of the root do.
	if b, err := Encode(ResourceLists{Lists: []List{{Members: []Member{
		&Entry{URI: "u"}, &EntryRef{Ref: "u"}, &External{Anchor: "u"}, &List{Name: "u"}, &List{}, &List{},
		&Entry{URI: AnonymousURI, Copy: Copy{CopyControl: To}}, &Entry{URI: AnonymousURI, Copy: Copy{CopyControl: CC}},
	}}}}); err != nil {
		t.Errorf("%s, %v", b, err)
	}

	if b, err := Encode(ResourceLists{Lists: []List{{Name: "a"}, {Name: "a"}}}); !errors.Is(err, errDuplicate) {
		t.Errorf("%s, %v", b, err)
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
		r, err := Decode(b)
		if err != nil && !errors.Is(err, ErrValue) {
			return
		}

		// Decode keeps the members of a key twice (RFC 5364 §4), which Encode refuses (RFC 4826 §3.1).
		out, encErr := Encode(r)
		if errors.Is(encErr, errDuplicate) {
			return
		}

		stable(t, r)

		// A document decoded without error encodes to one decoded without error.
		if err == nil {
			if _, err := Decode(out); err != nil {
				t.Fatalf("Decode(Encode): %v\n%s\n%s", err, b, out)
			}
		}
	})
}
