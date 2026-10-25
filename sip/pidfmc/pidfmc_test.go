package pidfmc

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip/pidf"
	"github.com/ellanetworks/ims/sip/xmltree"
	"github.com/ellanetworks/ims/sip/xsd"
)

// doc is a document with its extension decoded: of the presence, and of the status of each tuple.
type doc struct {
	p        pidf.Presence
	x        Presence
	statuses []Status
}

// decode decodes b, an error wrapping ErrValue kept as decode's.
func decode(b []byte) (doc, error) {
	p, err := pidf.Decode(b)
	if err != nil && !errors.Is(err, pidf.ErrValue) {
		return doc{}, err
	}

	errs := []error{err}
	d := doc{p: p}

	d.x, err = DecodePresence(p)
	errs = append(errs, err)

	for _, t := range p.Tuples {
		s, err := DecodeStatus(t.Status)
		d.statuses = append(d.statuses, s)
		errs = append(errs, err)
	}

	return d, errors.Join(errs...)
}

// encode sets the extension of d into its document and encodes it.
func encode(d doc) ([]byte, error) {
	p := d.p
	p.Tuples = append([]pidf.Tuple(nil), p.Tuples...)

	if err := SetPresence(&p, d.x); err != nil {
		return nil, err
	}

	for i := range p.Tuples {
		if err := SetStatus(&p.Tuples[i].Status, d.statuses[i]); err != nil {
			return nil, err
		}
	}

	return pidf.Encode(p)
}

func decodeFile(t *testing.T, name string) doc {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	d, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}

	return d
}

// roundTrip checks that d encodes, decodes to the same extension and elements not decoded, and encodes again to the
// same bytes.
func roundTrip(t *testing.T, d doc) {
	t.Helper()

	b, err := encode(d)
	if err != nil {
		t.Fatal(err)
	}

	again, err := decode(b)
	if err != nil && !errors.Is(err, ErrValue) {
		t.Fatalf("decode(encode): %v\n%s", err, b)
	}

	if !reflect.DeepEqual(again.x, d.x) || !reflect.DeepEqual(norm(again.statuses), norm(d.statuses)) {
		t.Fatalf("decode(encode) %+v %+v\nwant %+v %+v\n%s", again.x, again.statuses, d.x, d.statuses, b)
	}

	if !reflect.DeepEqual(notDecoded(again), notDecoded(d)) {
		t.Fatalf("not decoded %v\nwant %v\n%s", notDecoded(again), notDecoded(d), b)
	}

	if b2, err := encode(again); err != nil || string(b2) != string(b) {
		t.Fatalf("encode not stable:\n%s\n%s, %v", b, b2, err)
	}
}

// notDecoded are the elements of d's presence and statuses its extension does not decode.
func notDecoded(d doc) [][]*xmltree.Element {
	_, used, _ := decodePresence(d.p.Other)
	out := [][]*xmltree.Element{keep(nil, d.p.Other, used)}

	for _, t := range d.p.Tuples {
		_, used, _ := decodeStatus(t.Status.Other)
		out = append(out, keep(nil, t.Status.Other, used))
	}

	return out
}

// norm is ss with each expiration as it encodes, for comparison: of "+00:00" and "Z" alike.
func norm(ss []Status) []Status {
	n := func(d *xsd.DateTime) *xsd.DateTime {
		if d == nil {
			return nil
		}

		v, err := xsd.ParseDateTime(d.String())
		if err != nil {
			panic(err)
		}

		return &v
	}

	var out []Status

	for _, s := range ss {
		var c Status

		for _, a := range s.Affiliations {
			a.Expires = n(a.Expires)
			c.Affiliations = append(c.Affiliations, a)
		}

		for _, f := range s.FunctionalAliases {
			f.Expires = n(f.Expires)
			c.FunctionalAliases = append(c.FunctionalAliases, f)
		}

		out = append(out, c)
	}

	return out
}

func at(s string) *xsd.DateTime {
	d, err := xsd.ParseDateTime(s)
	if err != nil {
		panic(err)
	}

	return &d
}

const (
	alice   = "sip:alice@mcptt.example.com"
	bob     = "sip:bob@mcptt.example.com"
	group1  = "sip:group1@mcptt.example.com"
	group2  = "sip:group2@mcptt.example.com"
	client1 = "urn:uuid:00000000-0000-1000-8000-0123456789ab"
	client2 = "urn:uuid:00000000-0000-1000-8000-0123456789cd"
	pid     = "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
)

// The instances of testdata are valid against the schemas of Table 9.3.1.2-1 and RFC 3863 §4.4, the tuple id taken
// as a string (pidf.Tuple.ID).
func TestDecodeTestdata(t *testing.T) {
	yes := true

	for _, c := range []struct {
		file     string
		entity   string
		ids      []string
		x        Presence
		statuses []Status
	}{
		{
			"user-publish.xml", alice,
			[]string{client1},
			Presence{PID: pid},
			[]Status{{Affiliations: []Affiliation{{Group: group1}, {Group: group2}}}},
		},
		{
			"user-notify.xml", alice,
			[]string{client1, client2},
			Presence{PID: pid},
			[]Status{
				{Affiliations: []Affiliation{
					{Group: group1, Status: Affiliated, Expires: at("2026-10-21T08:00:00Z")},
					{Group: group2, Status: Affiliating, Expires: &xsd.DateTime{
						Time: time.Date(2026, 10, 21, 8, 0, 0, 5e8, time.FixedZone("", 2*3600)), Zoned: true,
					}},
				}},
				{Affiliations: []Affiliation{{Group: group1, Status: Deaffiliating, Expires: at("2026-10-20T08:01:04Z")}}},
			},
		},
		{
			"group-publish.xml", group1,
			[]string{alice},
			Presence{PID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8"},
			[]Status{{Affiliations: []Affiliation{{Client: client1}, {Client: client2}}}},
		},
		{
			"group-notify.xml", group1,
			[]string{alice},
			Presence{PID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8"},
			[]Status{{
				Affiliations: []Affiliation{{Client: client1, Expires: at("2026-10-21T08:00:00Z")}},
				FunctionalAliases: []FunctionalAlias{
					{ID: "sip:dispatcher@mcptt.example.com", Expires: at("2026-10-21T08:00:00Z")},
				},
			}},
		},
		{
			"group-dynamic.xml", group1,
			[]string{alice, bob},
			Presence{
				GroupStatus: &GroupStatus{Value: Emergency, ModifiedBy: alice},
				AdditionalData: &AdditionalData{
					BroadcastAlias: "sip:broadcast1@mcptt.example.com", RegroupAlias: "sip:regroup1@mcptt.example.com",
					CallOngoing: &yes,
				},
			},
			[]Status{
				{
					Affiliations:      []Affiliation{{Client: client1, Expires: at("2026-10-21T08:00:00Z")}},
					FunctionalAliases: []FunctionalAlias{{ID: "sip:dispatcher@mcptt.example.com"}},
				},
				{Affiliations: []Affiliation{
					{Client: "urn:uuid:00000000-0000-1000-8000-0123456789ef", Expires: at("2026-10-21T09:30:00Z")},
				}},
			},
		},
	} {
		t.Run(c.file, func(t *testing.T) {
			d := decodeFile(t, c.file)

			var ids []string
			for _, tu := range d.p.Tuples {
				ids = append(ids, tu.ID)
			}

			if d.p.Entity != c.entity || !reflect.DeepEqual(ids, c.ids) {
				t.Errorf("entity %q, tuples %q", d.p.Entity, ids)
			}

			if !reflect.DeepEqual(d.x, c.x) {
				t.Errorf("presence %+v\nwant %+v", d.x, c.x)
			}

			if !reflect.DeepEqual(d.statuses, c.statuses) {
				t.Errorf("statuses %+v\nwant %+v", d.statuses, c.statuses)
			}

			for _, n := range notDecoded(d) {
				if len(n) != 0 {
					t.Errorf("not decoded %v", n)
				}
			}

			roundTrip(t, d)
		})
	}
}

// §9.2.1.2 step 5: the client publishes the groups the user is interested in at it, without status or expires, and
// a p-id; §9.2.2.2.3 steps 9, 10, 14a and 18: the participating function reads the entity, the client ID, the groups
// and the p-id.
func TestUserPublish(t *testing.T) {
	p := pidf.Presence{Entity: alice, Tuples: []pidf.Tuple{{ID: client1}}}

	if err := SetStatus(&p.Tuples[0].Status, Status{Affiliations: []Affiliation{{Group: group1}, {Group: group2}}}); err != nil {
		t.Fatal(err)
	}

	if err := SetPresence(&p, Presence{PID: pid}); err != nil {
		t.Fatal(err)
	}

	b, err := pidf.Encode(p)
	if err != nil {
		t.Fatal(err)
	}

	want := `<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="sip:alice@mcptt.example.com">` +
		`<tuple id="urn:uuid:00000000-0000-1000-8000-0123456789ab"><status>` +
		`<affiliation xmlns="urn:3gpp:ns:mcpttPresInfo:1.0" group="sip:group1@mcptt.example.com"/>` +
		`<affiliation xmlns="urn:3gpp:ns:mcpttPresInfo:1.0" group="sip:group2@mcptt.example.com"/>` +
		`</status></tuple><p-id xmlns="urn:3gpp:ns:mcpttPresInfo:1.0">` + pid + `</p-id></presence>`
	if got := strings.TrimPrefix(string(b), xml.Header); got != want {
		t.Errorf("encode\n%s\nwant\n%s", got, want)
	}

	got, err := pidf.Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	x, err := DecodePresence(got)
	if err != nil || x.PID != pid {
		t.Fatalf("p-id %q, %v", x.PID, err)
	}

	if got.Entity != alice || len(got.Tuples) != 1 || got.Tuples[0].ID != client1 {
		t.Fatalf("%+v", got)
	}

	s, err := DecodeStatus(got.Tuples[0].Status)
	if err != nil {
		t.Fatal(err)
	}

	var groups []string
	for _, a := range s.Affiliations {
		groups = append(groups, a.Group)
	}

	if !reflect.DeepEqual(groups, []string{group1, group2}) {
		t.Errorf("groups %q", groups)
	}
}

// RFC 3863 §4.2.3: what the extension does not know is kept, and passes on when it sets the elements it decodes; an
// affiliation with a child to understand is not decoded.
func TestKeep(t *testing.T) {
	d := decodeFile(t, "extended.xml")

	later := func(local string) xml.Name { return xml.Name{Space: "urn:example:later", Local: local} }

	s := d.statuses[0]
	if len(s.Affiliations) != 1 {
		t.Fatalf("affiliations %+v", s.Affiliations)
	}

	a := s.Affiliations[0]
	if a.Group != group1 || a.Status != Affiliated || len(a.Other) != 1 || a.Other[0].Name != later("location") ||
		!reflect.DeepEqual(a.Attr, []xml.Attr{{Name: later("priority"), Value: "3"}}) {
		t.Errorf("affiliation %+v", a)
	}

	var kept []string
	for _, e := range notDecoded(d)[1] {
		kept = append(kept, e.Name.Space+" "+e.Name.Local)
	}

	if !reflect.DeepEqual(kept, []string{"urn:example:later mood", Namespace + " affiliation", Namespace + " priority"}) {
		t.Errorf("kept in the status %q", kept)
	}

	if d.p.Tuples[0].Status.Basic != pidf.Open || d.p.Tuples[0].Contact == nil || d.p.Tuples[0].Timestamp == nil ||
		len(d.p.Tuples[0].Other) != 1 || len(d.p.Notes) != 1 {
		t.Errorf("tuple %+v", d.p)
	}

	if n := notDecoded(d)[0]; len(n) != 1 || n[0].Name != later("later") {
		t.Errorf("kept in the presence %v", n)
	}

	roundTrip(t, d)

	// §9.2.2.2.5: a status changed, the rest relayed.
	d.statuses[0].Affiliations[0].Status = Deaffiliating
	d.statuses[0].Affiliations[0].Expires = at("2026-10-20T08:01:04Z")
	d.x.PID = ""

	b, err := encode(d)
	if err != nil {
		t.Fatal(err)
	}

	again, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(again.statuses, d.statuses) || again.x != (Presence{}) ||
		!reflect.DeepEqual(notDecoded(again), notDecoded(d)) {
		t.Errorf("set and relayed\n%s", b)
	}
}

// Table 9.3.1.2-1: an element of the extension not valid against the schema is kept, not decoded, the others
// decoded; set, the extension passes it on.
func TestDecodeMalformed(t *testing.T) {
	good := `<m:affiliation group="sip:g1@x" status="affiliated"/>`

	for _, c := range []struct {
		name, status, presence string
	}{
		{name: "status", status: `<m:affiliation group="sip:g@x" status="deaffiliated"/>`},
		{name: "status of white space", status: `<m:affiliation group="sip:g@x" status=" affiliated"/>`},
		{name: "expires", status: `<m:affiliation group="sip:g@x" expires="2026-10-21"/>`},
		{name: "expires of year 10000", status: `<m:affiliation group="sip:g@x" expires="9999-12-31T24:00:00Z"/>`},
		{name: "character data", status: `<m:affiliation group="sip:g@x">x</m:affiliation>`},
		{name: "functionalAlias expires", status: `<m:functionalAlias functionalAliasID="sip:f@x" expires="x"/>`},
		{name: "functionalAlias data", status: `<m:functionalAlias functionalAliasID="sip:f@x">x</m:functionalAlias>`},
		{name: "affiliation twice", status: `<m:affiliation group="sip:g1@x"/>`},
		{name: "p-id", presence: `<m:p-id><b/></m:p-id>`},
		{name: "groupStatus", presence: `<m:groupStatus><b/></m:groupStatus>`},
		{name: "additionalData element", presence: `<m:additionalData><b/></m:additionalData>`},
		{name: "additionalData white space", presence: `<m:additionalData> </m:additionalData>`},
		{name: "groupCallOngoing", presence: `<m:additionalData groupCallOngoing="yes"/>`},
	} {
		in := `<presence xmlns="urn:ietf:params:xml:ns:pidf" xmlns:m="urn:3gpp:ns:mcpttPresInfo:1.0" entity="sip:a@x">` +
			`<tuple id="c"><status>` + good + c.status + `</status></tuple><m:p-id>1</m:p-id>` + c.presence + `</presence>`

		d, err := decode([]byte(in))
		if !errors.Is(err, ErrValue) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}

		bad, err := xmltree.Parse([]byte(`<x xmlns="urn:ietf:params:xml:ns:pidf" xmlns:m="urn:3gpp:ns:mcpttPresInfo:1.0">` + c.status + c.presence + `</x>`))
		if err != nil {
			t.Fatal(err)
		}

		want := Status{Affiliations: []Affiliation{{Group: "sip:g1@x", Status: Affiliated}}}
		if !reflect.DeepEqual(d.statuses, []Status{want}) || d.x != (Presence{PID: "1"}) {
			t.Errorf("%s: %+v %+v", c.name, d.x, d.statuses)
		}

		if n := notDecoded(d); !reflect.DeepEqual(append(n[0], n[1]...), bad.Elements()) {
			t.Errorf("%s: kept %v", c.name, n)
		}

		// Set and relayed with the element it could not decode.
		d.statuses[0].Affiliations[0].Status = Deaffiliating

		b, err := encode(d)
		if err != nil {
			t.Fatal(err)
		}

		if again, _ := decode(b); !reflect.DeepEqual(notDecoded(again), notDecoded(d)) {
			t.Errorf("%s: relayed %s", c.name, b)
		}

		roundTrip(t, d)
	}
}

const repeated = `<presence xmlns="urn:ietf:params:xml:ns:pidf" xmlns:m="urn:3gpp:ns:mcpttPresInfo:1.0" ` +
	`entity="sip:g@x"><tuple id="sip:a@x"><status>` +
	`<m:affiliation client="c1" expires="x"/><m:affiliation client="c1" expires="2026-10-21T08:00:00Z"/>` +
	`<m:affiliation client="c1"/><m:affiliation client="c2"/>` +
	`<m:functionalAlias functionalAliasID="f"/><m:functionalAlias functionalAliasID="f" expires="2026-10-21T08:00:00Z"/>` +
	`</status></tuple>` +
	`<m:p-id><b/></m:p-id><m:p-id/><m:p-id>2</m:p-id><m:p-id>3</m:p-id>` +
	`<m:groupStatus>in-peril</m:groupStatus><m:groupStatus>emergency</m:groupStatus>` +
	`<m:additionalData groupCallOngoing="x"/><m:additionalData/><m:additionalData groupCallOngoing="1"/>` +
	`</presence>`

// §9.3.1.2: one affiliation per group of a client, per client of a member, one functional alias per ID, one p-id,
// groupStatus and additionalData: the first well-formed counts.
func TestDecodeRepeated(t *testing.T) {
	d, err := decode([]byte(repeated))
	if !errors.Is(err, ErrValue) {
		t.Fatal(err)
	}

	want := Status{
		Affiliations:      []Affiliation{{Client: "c1", Expires: at("2026-10-21T08:00:00Z")}, {Client: "c2"}},
		FunctionalAliases: []FunctionalAlias{{ID: "f"}},
	}
	if !reflect.DeepEqual(d.statuses, []Status{want}) ||
		!reflect.DeepEqual(d.x, Presence{PID: "2", GroupStatus: &GroupStatus{Value: InPeril}, AdditionalData: &AdditionalData{}}) {
		t.Errorf("%+v %+v", d.x, d.statuses)
	}

	if n := notDecoded(d); len(n[0]) != 6 || len(n[1]) != 3 {
		t.Errorf("kept %v", n)
	}

	roundTrip(t, d)
}

const mustUnderstand = `<presence xmlns="urn:ietf:params:xml:ns:pidf" xmlns:m="urn:3gpp:ns:mcpttPresInfo:1.0" ` +
	`xmlns:x="urn:x" entity="sip:a@x"><tuple id="c"><status>` +
	`<m:affiliation group="sip:g1@x"><x:a mustUnderstand="1"/><x:b><x:c xmlns:p="urn:ietf:params:xml:ns:pidf" ` +
	`p:mustUnderstand="1"/></x:b></m:affiliation>` +
	`<m:affiliation group="sip:g2@x"><x:d xmlns:p="urn:ietf:params:xml:ns:pidf" p:mustUnderstand=" true "/></m:affiliation>` +
	`<m:functionalAlias functionalAliasID="f"><x:e xmlns:p="urn:ietf:params:xml:ns:pidf" p:mustUnderstand="1"/></m:functionalAlias>` +
	`</status></tuple></presence>`

// RFC 3863 §4.2.3: an affiliation with a child to understand is unrecognized, not malformed; a mustUnderstand of a
// child of a child the extension ignores is itself ignored.
func TestMustUnderstand(t *testing.T) {
	d, err := decode([]byte(mustUnderstand))
	if err != nil {
		t.Fatal(err)
	}

	s := d.statuses[0]
	if len(s.Affiliations) != 1 || s.Affiliations[0].Group != "sip:g1@x" || len(s.Affiliations[0].Other) != 2 ||
		len(s.FunctionalAliases) != 0 {
		t.Errorf("%+v", s)
	}

	if n := notDecoded(d)[1]; len(n) != 2 || n[0].Name.Local != "affiliation" || n[1].Name.Local != "functionalAlias" {
		t.Errorf("kept %v", n)
	}

	roundTrip(t, d)

	// Not written: DecodeStatus would not decode it.
	s.Affiliations[0].Other = append(s.Affiliations[0].Other, d.p.Tuples[0].Status.Other[1].Elements()[0])
	if err := SetStatus(&pidf.Status{}, s); err == nil {
		t.Error("child to understand set")
	}
}

func TestSetErrors(t *testing.T) {
	for _, s := range []Status{
		{Affiliations: []Affiliation{{Group: "g", Status: "deaffiliated"}}},
		{Affiliations: []Affiliation{{Group: "g", Expires: &xsd.DateTime{Time: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}}},
		{Affiliations: []Affiliation{{Group: "g"}, {Group: "g"}}},
		{Affiliations: []Affiliation{{Group: "g", Attr: []xml.Attr{{Name: xml.Name{Local: "client"}, Value: "c"}}}}},
		{FunctionalAliases: []FunctionalAlias{{ID: "f"}, {ID: "f"}}},
		{FunctionalAliases: []FunctionalAlias{{ID: "f", Expires: &xsd.DateTime{Time: time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)}}}},
		{FunctionalAliases: []FunctionalAlias{{ID: "f", Attr: []xml.Attr{{Name: xml.Name{Local: "expires"}, Value: "x"}}}}},
	} {
		if err := SetStatus(&pidf.Status{}, s); err == nil {
			t.Errorf("SetStatus(%+v)", s)
		}
	}

	for _, x := range []Presence{
		{GroupStatus: &GroupStatus{Attr: []xml.Attr{{Name: xml.Name{Local: "ModifiedBy"}}}}},
		{AdditionalData: &AdditionalData{Attr: []xml.Attr{{Name: xml.Name{Local: "groupCallOngoing"}, Value: "1"}}}},
	} {
		if err := SetPresence(&pidf.Presence{}, x); err == nil {
			t.Errorf("SetPresence(%+v)", x)
		}
	}
}

// §9.2.2.3.10: per-group dynamic data the server owning the group builds.
func TestEncodeGroupDynamic(t *testing.T) {
	no := false
	d := doc{
		p: pidf.Presence{Entity: group1, Tuples: []pidf.Tuple{{ID: alice}}},
		x: Presence{
			GroupStatus:    &GroupStatus{Value: InPeril, ModifiedBy: bob},
			AdditionalData: &AdditionalData{CallOngoing: &no},
		},
		statuses: []Status{{Affiliations: []Affiliation{{Client: client1, Expires: at("2026-10-21T08:00:00Z")}}}},
	}

	b, err := encode(d)
	if err != nil {
		t.Fatal(err)
	}

	want := `<presence xmlns="urn:ietf:params:xml:ns:pidf" entity="sip:group1@mcptt.example.com">` +
		`<tuple id="sip:alice@mcptt.example.com"><status><affiliation xmlns="urn:3gpp:ns:mcpttPresInfo:1.0" ` +
		`client="urn:uuid:00000000-0000-1000-8000-0123456789ab" expires="2026-10-21T08:00:00Z"/></status></tuple>` +
		`<groupStatus xmlns="urn:3gpp:ns:mcpttPresInfo:1.0" ModifiedBy="sip:bob@mcptt.example.com">in-peril</groupStatus>` +
		`<additionalData xmlns="urn:3gpp:ns:mcpttPresInfo:1.0" groupCallOngoing="false"/></presence>`
	if got := strings.TrimPrefix(string(b), xml.Header); got != want {
		t.Errorf("encode\n%s\nwant\n%s", got, want)
	}

	roundTrip(t, d)
}

func FuzzPidf(f *testing.F) {
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

	f.Add([]byte(repeated))
	f.Add([]byte(mustUnderstand))

	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := decode(b)
		if err != nil && !errors.Is(err, ErrValue) {
			return
		}

		// The document is pidf's to encode: of what it does not, the extension is not tested.
		if _, err := pidf.Encode(d.p); err != nil {
			return
		}

		roundTrip(t, d)
	})
}
