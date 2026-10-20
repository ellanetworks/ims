package simplefilter

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip/xmltree"
)

func decodeFile(t *testing.T, name string) FilterSet {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	fs, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	return fs
}

// stable checks that fs encodes, decodes to what encodes again the same: the elements kept, malformed, too.
func stable(t *testing.T, fs FilterSet) {
	t.Helper()

	b, err := Encode(fs)
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

var pidfBinding = NSBinding{Prefix: "pidf", URN: "urn:ietf:params:xml:ns:pidf"}

// RFC 4661 §6.1 to §6.6.
func TestExamples(t *testing.T) {
	fs := decodeFile(t, "rfc4661-6.1.xml")

	f := fs.Filters[0]
	if !reflect.DeepEqual(fs.NSBindings, []NSBinding{pidfBinding, {"rpid", "urn:ietf:params:xml:ns:pidf:rpid"}}) ||
		f.ID != "123" || f.URI != "sip:presentity@example.com" || len(f.What.Includes) != 1 || f.What.Includes[0].Type != XPath ||
		!strings.HasPrefix(strings.TrimSpace(f.What.Includes[0].Value), `/pidf:presence/pidf:tuple[rpid:class="IM"`) {
		t.Errorf("6.1: %+v", fs)
	}

	stable(t, fs)

	fs = decodeFile(t, "rfc4661-6.2.xml")

	c := fs.Filters[0].Triggers[0].Changed[0]
	if *c.From != "CLOSED" || *c.To != "OPEN" || c.By != "" ||
		strings.TrimSpace(c.Reference) != "/pidf:presence/pidf:tuple/pidf:status/pidf:basic" || fs.Filters[0].What != nil {
		t.Errorf("6.2: %+v", c)
	}

	stable(t, fs)

	fs = decodeFile(t, "rfc4661-6.3.xml")

	f = fs.Filters[0]
	if len(f.Triggers) != 2 || *f.Triggers[1].Changed[0].To != "waiting" || f.Triggers[1].Changed[0].From != nil ||
		f.What.Includes[0].Type != "" {
		t.Errorf("6.3: %+v", f)
	}

	stable(t, fs)

	fs = decodeFile(t, "rfc4661-6.4.xml")

	if s := fs.Filters[0].What.Includes[0]; fs.NSBindings != nil || s.Type != NamespaceValue || strings.TrimSpace(s.Value) != "urn:ietf:params:xml:ns:pidf" {
		t.Errorf("6.4: %+v", fs)
	}

	stable(t, fs)

	// §6.5 uses the prefix pidf without binding it: the selection is kept as given.
	fs = decodeFile(t, "rfc4661-6.5.xml")

	if f := fs.Filters[0]; f.URI != "" || len(fs.NSBindings) != 1 || !strings.Contains(f.What.Includes[0].Value, `pidf:status[game-ext:label="game-X"]`) {
		t.Errorf("6.5: %+v", fs)
	}

	stable(t, fs)

	fs = decodeFile(t, "rfc4661-6.6.xml")

	if len(fs.Filters) != 2 || fs.Filters[1].ID != "999" || fs.Filters[1].What.Includes[0].Type != NamespaceValue ||
		strings.TrimSpace(fs.Filters[1].What.Excludes[0].Value) != "/pidf:presence/pidf:tuple/pidf:note" {
		t.Errorf("6.6: %+v", fs)
	}

	stable(t, fs)
}

// TS 24.379 §9.3.2.2: the restrictions of presence notifications per client and per group.
func TestMCPTT(t *testing.T) {
	fs := decodeFile(t, "ts24379-9.3.2.2-client.xml")

	f := fs.Filters[0]
	if !reflect.DeepEqual(fs.NSBindings, []NSBinding{pidfBinding, {"mcpttPI10", "urn:3gpp:ns:mcpttPresInfo:1.0"}}) || f.URI != "" ||
		f.Domain != "" || !reflect.DeepEqual(f.What.Includes, []Selector{{Value: `//pidf:presence/pidf:tuple[@id="urn:uuid:00000000-0000-1000-8000-0123456789ab"]`}}) {
		t.Errorf("%+v", fs)
	}

	stable(t, fs)

	fs = decodeFile(t, "ts24379-9.3.2.2-group.xml")

	if in := fs.Filters[0].What.Includes; len(in) != 2 || in[1].Value != "//pidf:presence/pidf:additionalData/@pidf:groupCallOngoing" {
		t.Errorf("%+v", in)
	}

	stable(t, fs)
}

// RFC 4661 §3, §4: the attributes of filters, the kinds of triggers, the extensions.
func TestExtensions(t *testing.T) {
	fs := decodeFile(t, "extensions.xml")

	f := fs.Filters[0]
	if fs.Package != "presence" || len(fs.Attr) != 1 || f.Domain != "example.com" || f.Remove || *f.Enabled || len(f.Attr) != 1 ||
		len(f.Other) != 1 {
		t.Fatalf("%+v", fs)
	}

	w := f.What
	if len(w.Includes) != 2 || len(w.Includes[0].Attr) != 1 || w.Includes[1].Type != XPath ||
		!reflect.DeepEqual(w.Excludes, []Selector{{Value: "//tuple/note"}, {Type: NamespaceValue}}) || len(w.Other) != 1 {
		t.Errorf("what %+v", w)
	}

	tr := f.Triggers[0]
	if len(tr.Changed) != 2 || tr.Changed[0].By != "-2.50" || len(tr.Changed[0].Attr) != 1 || *tr.Changed[1].From != "" ||
		!reflect.DeepEqual(tr.Added, []string{"//tuple"}) || !reflect.DeepEqual(tr.Removed, []string{"//tuple", "//person"}) ||
		len(tr.Other) != 1 || f.Triggers[1].Added[0] != "//device" {
		t.Errorf("triggers %+v", f.Triggers)
	}

	if g := fs.Filters[1]; g.URI != "sip:a@example.com" || !g.Remove || !*g.Enabled {
		t.Errorf("filter %+v", g)
	}

	stable(t, fs)
}

// RFC 4661 §3, §7: a malformed element kept, not decoded; the first of an element given once counting.
func TestMalformed(t *testing.T) {
	for _, c := range []struct {
		name, body string
		check      func(FilterSet) bool
	}{
		{"filter without id", `<filter/><filter id="1"/>`, func(fs FilterSet) bool { return len(fs.Filters) == 1 && len(fs.Other) == 1 }},
		{"filter id twice", `<filter id="1"/><filter id="1" remove="true"/>`, func(fs FilterSet) bool {
			return len(fs.Filters) == 1 && !fs.Filters[0].Remove && len(fs.Other) == 1
		}},
		{"uri and domain", `<filter id="1" uri="sip:a@x" domain="x"/><filter id="2"/>`, func(fs FilterSet) bool { return len(fs.Other) == 1 }},
		{"remove", `<filter id="1" remove="yes"/><filter id="2"/>`, func(fs FilterSet) bool { return len(fs.Other) == 1 }},
		{"enabled", `<filter id="1" enabled="no"/><filter id="2"/>`, func(fs FilterSet) bool { return len(fs.Other) == 1 }},
		{"ns-binding without urn", `<ns-bindings><ns-binding prefix="p"/></ns-bindings><filter id="1"/>`, func(fs FilterSet) bool {
			return fs.NSBindings == nil && len(fs.Other) == 1
		}},
		{"ns-bindings empty", `<ns-bindings/><filter id="1"/>`, func(fs FilterSet) bool { return fs.NSBindings == nil && len(fs.Other) == 1 }},
		{"include type", `<filter id="1"><what><include type="regexp">x</include></what></filter>`, func(fs FilterSet) bool {
			return len(fs.Filters[0].What.Includes) == 0 && len(fs.Filters[0].What.Other) == 1
		}},
		{"elements in an exclude", `<filter id="1"><what><exclude><b/></exclude></what></filter>`, func(fs FilterSet) bool {
			return len(fs.Filters[0].What.Excludes) == 0 && len(fs.Filters[0].What.Other) == 1
		}},
		{"empty trigger", `<filter id="1"><trigger/></filter>`, func(fs FilterSet) bool {
			return len(fs.Filters[0].Triggers) == 0 && len(fs.Filters[0].Other) == 1
		}},
		{"by", `<filter id="1"><trigger><changed by="2x">//a</changed><added>//b</added></trigger></filter>`, func(fs FilterSet) bool {
			tr := fs.Filters[0].Triggers[0]
			return len(tr.Changed) == 0 && len(tr.Added) == 1 && len(tr.Other) == 1
		}},
		{
			"from of a by", `<filter id="1"><trigger><changed by="2" from="1">//a</changed><changed by="2" to="x">//a</changed></trigger></filter>`,
			func(fs FilterSet) bool {
				tr := fs.Filters[0].Triggers[0]
				return len(tr.Changed) == 1 && *tr.Changed[0].From == "1" && len(tr.Other) == 1
			},
		},
	} {
		fs, err := Decode([]byte(`<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter">` + c.body + `</filter-set>`))
		if !errors.Is(err, ErrValue) || !c.check(fs) {
			t.Errorf("%s: %+v, %v", c.name, fs, err)
			continue
		}

		stable(t, fs)
	}

	// Given more than once: the first counts, malformed or not, the others kept, malformed.
	fs, err := Decode([]byte(`<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter"><ns-bindings><ns-binding prefix="a" urn="u"/></ns-bindings>` +
		`<ns-bindings><ns-binding prefix="b" urn="v"/></ns-bindings><filter id="1"><what/><what><include>x</include></what></filter></filter-set>`))
	if !errors.Is(err, ErrValue) || fs.NSBindings[0].Prefix != "a" || len(fs.Other) != 1 || len(fs.Filters[0].What.Includes) != 0 ||
		len(fs.Filters[0].Other) != 1 {
		t.Errorf("%+v, %v", fs, err)
	}

	stable(t, fs)

	fs, err = Decode([]byte(`<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter"><ns-bindings/>` +
		`<ns-bindings><ns-binding prefix="b" urn="v"/></ns-bindings><filter id="1"/></filter-set>`))
	if !errors.Is(err, ErrValue) || fs.NSBindings != nil || len(fs.Other) != 2 {
		t.Errorf("%+v, %v", fs, err)
	}

	stable(t, fs)
}

// RFC 4661 §3.6.1.4: from and to of a change by an amount are decimals.
func TestChangedBy(t *testing.T) {
	fs, err := Decode([]byte(`<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter"><filter id="1"><trigger>` +
		`<changed by="2" from=" -1.5 " to="+3">//a</changed><changed from="x" to="y">//b</changed></trigger></filter></filter-set>`))
	if err != nil || len(fs.Filters[0].Triggers[0].Changed) != 2 {
		t.Fatalf("%+v, %v", fs, err)
	}

	stable(t, fs)

	x := "x"
	fs.Filters[0].Triggers[0].Changed[1].By = "1"
	fs.Filters[0].Triggers[0].Changed[1].To = &x

	if b, err := Encode(fs); err == nil {
		t.Errorf("%s", b)
	}
}

func TestDecimal(t *testing.T) {
	for in, want := range map[string]bool{
		"2": true, "-2.50": true, "+.5": true, "1.": true, "007": true,
		"": false, ".": false, "+": false, "1.2.3": false, "1e3": false, "--1": false, " 1": false,
	} {
		if got := isDecimal(in); got != want {
			t.Errorf("isDecimal(%q) = %v", in, got)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		``,
		`<filter-set/>`,
		`<filter xmlns="urn:ietf:params:xml:ns:simple-filter" id="1"/>`,
		`<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter">` + strings.Repeat(" ", maxSize) + `</filter-set>`,
		`<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter"/>`,
		`<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter"><ns-bindings><ns-binding prefix="a" urn="u"/></ns-bindings><filter/></filter-set>`,
	} {
		if fs, err := Decode([]byte(in)); err == nil || errors.Is(err, ErrValue) {
			t.Errorf("Decode(%.60q) = %+v, %v", in, fs, err)
		}
	}
}

func TestEncode(t *testing.T) {
	no := false
	to := "5"
	no2 := "no"
	fs := FilterSet{
		NSBindings: []NSBinding{pidfBinding},
		Filters: []Filter{{
			ID: "1", Enabled: &no,
			What:     &What{Includes: []Selector{{Value: "//pidf:tuple"}}, Excludes: []Selector{{Type: NamespaceValue, Value: "urn:x"}}},
			Triggers: []Trigger{{Changed: []Changed{{Reference: "//pidf:basic", To: &to, By: "1"}}, Removed: []string{"//pidf:tuple"}}},
			Other:    []*xmltree.Element{xmltree.New("urn:x", "ext")},
		}},
	}

	b, err := Encode(fs)
	if err != nil {
		t.Fatal(err)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<filter-set xmlns="urn:ietf:params:xml:ns:simple-filter">` +
		`<ns-bindings><ns-binding prefix="pidf" urn="urn:ietf:params:xml:ns:pidf"/></ns-bindings><filter id="1" enabled="false">` +
		`<what><include>//pidf:tuple</include><exclude type="namespace">urn:x</exclude></what>` +
		`<trigger><changed to="5" by="1">//pidf:basic</changed><removed>//pidf:tuple</removed></trigger><ext xmlns="urn:x"/></filter></filter-set>`
	if string(b) != want {
		t.Errorf("Encode\n%s\nwant\n%s", b, want)
	}

	for _, fs := range []FilterSet{
		{Filters: []Filter{{ID: "1"}, {ID: "1"}}},
		{Filters: []Filter{{ID: "1", URI: "sip:a@x", Domain: "x"}}},
		{Filters: []Filter{{ID: "1", What: &What{Includes: []Selector{{Type: "regexp"}}}}}},
		{Filters: []Filter{{ID: "1", Triggers: []Trigger{{}}}}},
		{Filters: []Filter{{ID: "1", Triggers: []Trigger{{Changed: []Changed{{By: "x"}}}}}}},
		{Filters: []Filter{{ID: "1"}}, Attr: []xml.Attr{{Name: xml.Name{Local: "package"}}}},
		{},
		{NSBindings: []NSBinding{pidfBinding}},
		{Filters: []Filter{{ID: "1", Triggers: []Trigger{{Changed: []Changed{{By: "1", From: &no2}}}}}}},
	} {
		if b, err := Encode(fs); err == nil {
			t.Errorf("Encode(%+v) = %s", fs, b)
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
		fs, err := Decode(b)
		if err != nil && !errors.Is(err, ErrValue) {
			return
		}

		stable(t, fs)

		// A document decoded without error encodes to one decoded without error.
		if err == nil {
			out, _ := Encode(fs)
			if _, err := Decode(out); err != nil {
				t.Fatalf("Decode(Encode): %v\n%s\n%s", err, b, out)
			}
		}
	})
}
