package xmltree

import (
	"encoding/xml"
	"reflect"
	"strings"
	"testing"
)

// Namespaces in XML 1.0: names resolve to their namespaces, whatever the prefixes; Marshal declares the default
// namespace where it changes and prefixes for the attributes of other namespaces.
func TestNamespaces(t *testing.T) {
	root, err := Parse([]byte(`<a:r xmlns:a="urn:a" xmlns="urn:d" xmlns:b="urn:b" b:x="1" y="2" xml:lang="en">` +
		`<c b:z="3"><a:e/></c><f xmlns=""/>t</a:r>`))
	if err != nil {
		t.Fatal(err)
	}

	want := &Element{
		Name: xml.Name{Space: "urn:a", Local: "r"},
		Attr: []xml.Attr{
			{Name: xml.Name{Space: "urn:b", Local: "x"}, Value: "1"},
			{Name: xml.Name{Local: "y"}, Value: "2"},
			{Name: xml.Name{Space: XMLNamespace, Local: "lang"}, Value: "en"},
		},
		Children: []Node{
			&Element{
				Name:     xml.Name{Space: "urn:d", Local: "c"},
				Attr:     []xml.Attr{{Name: xml.Name{Space: "urn:b", Local: "z"}, Value: "3"}},
				Children: []Node{New("urn:a", "e")},
			},
			New("", "f"),
			Text("t"),
		},
	}

	if !reflect.DeepEqual(root, want) {
		t.Fatalf("Parse %#v", root)
	}

	b, err := Marshal(root)
	if err != nil {
		t.Fatal(err)
	}

	got := strings.TrimPrefix(string(b), xml.Header)
	if got != `<r xmlns="urn:a" xmlns:ns0="urn:b" ns0:x="1" y="2" xml:lang="en"><c xmlns="urn:d" ns0:z="3">`+
		`<e xmlns="urn:a"/></c><f xmlns=""/>t</r>` {
		t.Errorf("Marshal %s", got)
	}

	if again, err := Parse(b); err != nil || !reflect.DeepEqual(again, root) {
		t.Errorf("Parse(Marshal) %#v, %v", again, err)
	}
}

// XML 1.0 §2.4, §3.3.3: markup characters escaped; whitespace in attributes kept from normalization.
func TestEscape(t *testing.T) {
	e := New("", "e", Text("a < b & c > d\r\n"))
	e.Attr = []xml.Attr{{Name: xml.Name{Local: "v"}, Value: "\"q\"\t\n<&"}}

	b, err := Marshal(e)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimPrefix(string(b), xml.Header); got != `<e v="&quot;q&quot;&#x9;&#xA;&lt;&amp;">a &lt; b &amp; c &gt; d&#xD;
</e>` {
		t.Errorf("Marshal %s", got)
	}

	if again, err := Parse(b); err != nil || !reflect.DeepEqual(again, e) {
		t.Errorf("Parse(Marshal) %#v, %v", again, err)
	}

	for _, e := range []*Element{
		New("", "e", Text("\x00")),
		New("", "e", Text("\xff")),
		New("", "a:b"),
		New("", "1a"),
		{Name: xml.Name{Local: "e"}, Attr: []xml.Attr{{Name: xml.Name{Local: ""}}}},
	} {
		if b, err := Marshal(e); err == nil {
			t.Errorf("Marshal(%#v) = %s", e, b)
		}
	}
}

// Comments and processing instructions are left out; adjacent text joins.
func TestParseText(t *testing.T) {
	root, err := Parse([]byte("<?xml version=\"1.0\"?>\n<!-- c --><e>a<!-- c -->b<?pi x?><![CDATA[<c>]]></e>\n"))
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(root, New("", "e", Text("ab<c>"))) || root.Text() != "ab<c>" || root.HasElements() {
		t.Errorf("Parse %#v", root)
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"text",
		"<a>",
		"<a></b>",
		"<a/><b/>",
		"<a/>text",
		`<!DOCTYPE a [<!ENTITY e "x">]><a>&e;</a>`,
		`<?xml version="1.0" encoding="ISO-8859-1"?><a/>`,
		"<a:b:c xmlns:a='urn:a'/>",
		"<a b:c:d='1' xmlns:b='urn:b'/>",
		strings.Repeat("<a>", maxDepth+1) + strings.Repeat("</a>", maxDepth+1),
		// XML 1.0: well-formedness constraints.
		`<a x="1" x="2"/>`,             // Unique Att Spec
		`<a x="<"/>`,                   // No < in Attribute Values
		`<a>&e;</a>`,                   // Entity Declared
		`<a>&#0;</a>`,                  // Legal Character
		`<a>&#xD800;</a>`,              // Legal Character
		`<a>]]></a>`,                   // §2.4
		`<a><!-- x -- y --></a>`,       // §2.5
		`<a><!-- x ---></a>`,           // §2.5
		`<a><?xml version="1.0"?></a>`, // §2.6: the target xml reserved
		`<?xml version="1.0"?><?xml version="1.0"?><a/>`,
		` <?xml version="1.0"?><a/>`,   // §2.8: the declaration at the start
		`<?xml encoding="UTF-8"?><a/>`, // version first
		`<?xml version="2.0"?><a/>`,
		`<?xml version="1.0" standalone="yes" encoding="UTF-8"?><a/>`,
		`<?xml version="1.0" standalone="maybe"?><a/>`,
		`<?xml version="1.0" encoding="US-ASCII"?><a>é</a>`,
		`<a><![CDATA[x</a>`,
		`<a></a >x`,
		`<a b="1"c="2"/>`,
		"<a>\x01</a>",
		"<a>\xff</a>",
		// Namespaces in XML 1.0: namespace constraints.
		`<a><foo:b/></a>`,        // Prefix Declared
		`<a foo:x="1"/>`,         // Prefix Declared
		`<a xmlns:p=""/>`,        // No Prefix Undeclaring
		`<a xmlns:xml="urn:x"/>`, // Reserved Prefixes
		`<a xmlns:p="http://www.w3.org/XML/1998/namespace"/>`,  // Reserved Prefixes
		`<a xmlns="http://www.w3.org/XML/1998/namespace"/>`,    // Reserved Prefixes
		`<a xmlns:xmlns="http://www.w3.org/2000/xmlns/"/>`,     // Reserved Prefixes
		`<a xmlns:p="http://www.w3.org/2000/xmlns/"/>`,         // Reserved Prefixes
		`<xmlns:a xmlns:xmlns="urn:x"/>`,                       // element of the xmlns prefix
		`<a xmlns:p="urn:x" xmlns:q="urn:x" p:x="1" q:x="2"/>`, // Attributes Unique (§6.3)
		`<:a/>`,
		`<a:/>`,
		`<a xmlns:="urn:x"/>`,      // PrefixedAttName (Namespaces §3 [2])
		`<a><?p:q x?></a>`,         // Namespaces §7
		`<a>&#x110000;</a>`,        // above the Char range
		`<a>&#99999999999999;</a>`, // overflowing
	} {
		if e, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) = %#v", in, e)
		}
	}

	if _, err := Parse([]byte(strings.Repeat("<a>", maxDepth) + strings.Repeat("</a>", maxDepth))); err != nil {
		t.Errorf("depth %d: %v", maxDepth, err)
	}
}

// XML 1.0 §2.11, §3.3.3, §4.3.3: line ends normalized; white space in attribute values normalized but of character
// references; a byte order mark; the xml prefix bound; a declaration, version 1.x, and US-ASCII.
func TestNormalization(t *testing.T) {
	root, err := Parse([]byte("\xef\xbb\xbf<?xml version='1.1' encoding='us-ascii' standalone='no' ?>\r\n" +
		"<a v=\"x\ty\r\nz&#9;&#xA;&#xD;\" xml:lang='en' xmlns:xml='http://www.w3.org/XML/1998/namespace'>1\r\n2\r3&#xD;</a>"))
	if err != nil {
		t.Fatal(err)
	}

	want := &Element{
		Name: xml.Name{Local: "a"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "v"}, Value: "x y z\t\n\r"},
			{Name: xml.Name{Space: XMLNamespace, Local: "lang"}, Value: "en"},
		},
		Children: []Node{Text("1\n2\n3\r")},
	}
	if !reflect.DeepEqual(root, want) {
		t.Fatalf("Parse %#v", root)
	}

	b, err := Marshal(root)
	if err != nil {
		t.Fatal(err)
	}

	if again, err := Parse(b); err != nil || !reflect.DeepEqual(again, root) {
		t.Errorf("Parse(Marshal) %#v, %v", again, err)
	}

	// Names of the 5th edition (§2.3): U+203F is a NameChar, U+00AA is not.
	if _, err := Parse([]byte("<a\u203f/>")); err != nil {
		t.Error(err)
	}

	if _, err := Parse([]byte("<a\u00aa/>")); err == nil {
		t.Error("U+00AA in a name")
	}
}

// Namespaces in XML 1.0 §3, §6.3: what Marshal cannot write as a namespace-well-formed document.
func TestMarshalErrors(t *testing.T) {
	attr := func(space, local string) *Element {
		return &Element{Name: xml.Name{Local: "e"}, Attr: []xml.Attr{{Name: xml.Name{Space: space, Local: local}}}}
	}

	for _, e := range []*Element{
		{Name: xml.Name{Local: "e"}, Attr: []xml.Attr{{Name: xml.Name{Local: "x"}}, {Name: xml.Name{Local: "x"}}}},
		{Name: xml.Name{Local: "e"}, Attr: []xml.Attr{{Name: xml.Name{Space: "urn:a", Local: "x"}}, {Name: xml.Name{Space: "urn:a", Local: "x"}}}},
		attr("", "xmlns"),
		attr(xmlnsURL, "p"),
		attr("\x00", "x"),
		New(xmlnsURL, "e"),
		New("\x00", "e"),
		New("", "a\u00aa"),
	} {
		if b, err := Marshal(e); err == nil {
			t.Errorf("Marshal(%#v) = %s", e, b)
		}
	}
}

// Namespaces in XML 1.0 §3: an element of the xml namespace, its prefix bound by definition; XML 1.0 §4.1: a character
// reference of any number of digits.
func TestXMLPrefix(t *testing.T) {
	root, err := Parse([]byte(`<a xmlns="urn:a"><xml:e>&#x0000000041;&#00066;</xml:e></a>`))
	if err != nil {
		t.Fatal(err)
	}

	e := root.Elements()[0]
	if e.Name != (xml.Name{Space: XMLNamespace, Local: "e"}) || e.Text() != "AB" {
		t.Fatalf("%#v", e)
	}

	b, err := Marshal(root)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimPrefix(string(b), xml.Header); got != `<a xmlns="urn:a"><xml:e>AB</xml:e></a>` {
		t.Errorf("Marshal %s", got)
	}

	if again, err := Parse(b); err != nil || !reflect.DeepEqual(again, root) {
		t.Errorf("Parse(Marshal) %#v, %v", again, err)
	}
}

func TestClone(t *testing.T) {
	e := New("urn:a", "e", New("urn:a", "c", Text("x")))
	c := e.Clone()
	c.Elements()[0].Children[0] = Text("y")

	if e.Elements()[0].Text() != "x" {
		t.Error("Clone shares children")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`<a:r xmlns:a="urn:a" xmlns:b="urn:b" b:x="1"><c xml:lang="en">t &amp; u</c></a:r>`,
		"<e v='&#9;'>\r\n<![CDATA[x]]></e>",
		`<p:e xmlns:p="urn:p" xmlns="urn:d"><f p:a="1"/><g xmlns=""/></p:e>`,
		`<a xmlns="urn:a"><xml:e xml:lang="en">x</xml:e></a>`,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		root, err := Parse(b)
		if err != nil {
			return
		}

		out, err := Marshal(root)
		if err != nil {
			t.Fatalf("%q parsed, Marshal: %v", b, err)
		}

		again, err := Parse(out)
		if err != nil || !reflect.DeepEqual(again, root) {
			t.Fatalf("%q: Marshal %q parses to %#v, %v; want %#v", b, out, again, err, root)
		}
	})
}
