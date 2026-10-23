package mcpttinfo

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip/xmltree"
)

// The instances of testdata are valid against the schema of F.1.2.
func TestDecodeTestdata(t *testing.T) {
	for _, c := range []struct {
		file  string
		check func(*testing.T, Info)
	}{
		{"prearranged.xml", func(t *testing.T, info Info) {
			p := info.Params
			if p.SessionType != SessionPrearranged {
				t.Errorf("session-type %q", p.SessionType)
			}

			if v, ok := p.RequestURI.URI(); !ok || v != "sip:group123@mcptt.example.com" {
				t.Errorf("mcptt-request-uri %q", v)
			}

			if v, ok := p.ClientID.Text(); !ok || v != "urn:uuid:00000000-0000-1000-8000-0123456789ab" {
				t.Errorf("mcptt-client-id %q", v)
			}

			if len(p.Other) != 1 || p.Other[0].Name != (xml.Name{Space: "urn:example:later", Local: "later-element"}) {
				t.Errorf("other %#v", p.Other)
			}

			if len(p.Attr) != 1 || p.Attr[0].Name.Space != "urn:example:later" {
				t.Errorf("attributes %#v", p.Attr)
			}

			if len(p.Ext) != 1 || p.Ext[0].Name.Local != "functional-alias-URI" {
				t.Errorf("anyExt %#v", p.Ext)
			}
		}},
		{"register.xml", func(t *testing.T, info Info) {
			if v, ok := info.Params.AccessToken.Text(); !ok || !strings.HasPrefix(v, "eyJ") {
				t.Errorf("mcptt-access-token %q", v)
			}
		}},
		{"encrypted.xml", func(t *testing.T, info Info) {
			c := info.Params.RequestURI
			if c.Type != Encrypted || c.Kind != KindOther || c.Element.Name != encryptedData {
				t.Fatalf("mcptt-request-uri %#v", c)
			}

			if _, ok := c.URI(); ok {
				t.Error("encrypted URI in the clear")
			}

			if v, _ := c.Element.AttrValue("", "Type"); v != "http://www.w3.org/2001/04/xmlenc#Content" {
				t.Errorf("EncryptedData Type %q", v)
			}
		}},
		{"all.xml", func(t *testing.T, info Info) {
			no, yes := false, true
			want := Info{
				Params: &Params{
					AccessToken:       Text("token"),
					SessionType:       SessionChat,
					RequestURI:        URI("sip:group1@mcptt.example.com"),
					CallingUserID:     URI("sip:alice@mcptt.example.com"),
					CalledPartyID:     URI("sip:bob@mcptt.example.com"),
					CallingGroupID:    URI("sip:group1@mcptt.example.com"),
					Required:          Bool(true),
					EmergencyInd:      Bool(true),
					AlertInd:          Bool(false),
					ImminentPerilInd:  Bool(false),
					BroadcastInd:      &no,
					MCOrg:             "Fire Department",
					FloorState:        FloorTaken,
					AssociatedGroupID: URI("sip:group2@mcptt.example.com"),
					OriginatedBy:      URI("sip:carol@mcptt.example.com"),
					MKFCGKTPs: xmltree.New(Namespace, "MKFC-GKTPs",
						xmltree.New("urn:3gpp:ns:mcpttGKTP:1.0", "GKTP", xmltree.Text("opaque"))),
					ClientID:     Text("client-1"),
					AlertIndRcvd: Bool(true),
					GWMCPTTUsage: &yes,
				},
				Ext: []*xmltree.Element{xmltree.New(Namespace, "user-requested-priority", xmltree.Text("3"))},
			}

			if !reflect.DeepEqual(info, want) {
				t.Errorf("%+v\nwant %+v", info.Params, want.Params)
			}
		}},
	} {
		t.Run(c.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", c.file))
			if err != nil {
				t.Fatal(err)
			}

			info, err := Decode(b)
			if err != nil {
				t.Fatal(err)
			}

			c.check(t, info)
			roundTrip(t, info)
		})
	}
}

// roundTrip checks that info encodes, decodes to itself, and encodes again to the same bytes.
func roundTrip(t *testing.T, info Info) {
	t.Helper()

	roundTripKept(t, info, nil)
}

// roundTripKept is roundTrip of the info Decode returned with decodeErr: the malformed elements it kept give the same
// error again.
func roundTripKept(t *testing.T, info Info, decodeErr error) {
	t.Helper()

	b, err := Encode(info)
	if err != nil {
		t.Fatal(err)
	}

	again, err := Decode(b)
	if fmt.Sprint(err) != fmt.Sprint(decodeErr) {
		t.Fatalf("Decode(Encode): %v, want %v\n%s", err, decodeErr, b)
	}

	if !reflect.DeepEqual(again, info) {
		t.Fatalf("Decode(Encode) %+v\nwant %+v\n%s", again, info, b)
	}

	if b2, err := Encode(again); err != nil || string(b2) != string(b) {
		t.Fatalf("Encode not stable: %s\n%s, %v", b, b2, err)
	}
}

// TS 24.379 §10.1.1.3.1.1 steps 2 and 5, §6.3.2.1.3 step 8, F.1.3: the participating function sets
// <mcptt-calling-user-id> in the body it copies from the client's INVITE; what it does not know passes on.
func TestRelaySetElement(t *testing.T) {
	b, err := os.ReadFile("testdata/prearranged.xml")
	if err != nil {
		t.Fatal(err)
	}

	info, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	info.Params.CallingUserID = URI("sip:alice@mcptt.example.com")

	out, err := Encode(info)
	if err != nil {
		t.Fatal(err)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params xmlns:ns0="urn:example:later" ns0:flag="1">` +
		`<session-type>prearranged</session-type>` +
		`<mcptt-request-uri type="Normal"><mcpttURI>sip:group123@mcptt.example.com</mcpttURI></mcptt-request-uri>` +
		`<mcptt-calling-user-id type="Normal"><mcpttURI>sip:alice@mcptt.example.com</mcpttURI></mcptt-calling-user-id>` +
		`<mcptt-client-id type="Normal"><mcpttString>urn:uuid:00000000-0000-1000-8000-0123456789ab</mcpttString></mcptt-client-id>` +
		`<later-element xmlns="urn:example:later" a="b">` + "\n      " + `<part>kept &amp; relayed</part>` + "\n    " +
		`</later-element>` +
		`<anyExt><functional-alias-URI type="Normal">` + "\n        " +
		`<mcpttURI>sip:dispatcher@mcptt.example.com</mcpttURI>` + "\n      " +
		`</functional-alias-URI></anyExt></mcptt-Params></mcpttinfo>`
	if string(out) != want {
		t.Errorf("Encode\n%s\nwant\n%s", out, want)
	}
}

// F.1.3 2, 3: a malformed element is kept, not decoded, the others decoded; of an element given twice, the first
// well-formed counts and the others are kept. A relay that sets an element passes them on.
func TestDecodeMalformed(t *testing.T) {
	for _, c := range []struct {
		name, elem string
	}{
		{"boolean value", `<emergency-ind type="Normal"><mcpttBoolean>yes</mcpttBoolean></emergency-ind>`},
		{"URI as a boolean", `<emergency-ind type="Normal"><mcpttURI>sip:a@b</mcpttURI></emergency-ind>`},
		{"boolean as a URI", `<mcptt-request-uri type="Normal"><mcpttBoolean>true</mcpttBoolean></mcptt-request-uri>`},
		{"empty", `<mcptt-request-uri type="Normal"/>`},
		{"other in the clear", `<mcptt-request-uri><x:u xmlns:x="urn:x"/></mcptt-request-uri>`},
		{"type", `<mcptt-request-uri type="Secret"><mcpttURI>sip:a@b</mcpttURI></mcptt-request-uri>`},
		{"encrypted in the clear", `<mcptt-request-uri type="Encrypted"><mcpttURI>sip:a@b</mcpttURI></mcptt-request-uri>`},
		{"two choices", `<mcptt-client-id><mcpttString>a</mcpttString><mcpttString>b</mcpttString></mcptt-client-id>`},
		{"elements in a value", `<mcptt-client-id><mcpttString><b/></mcpttString></mcptt-client-id>`},
		{"broadcast-ind", `<broadcast-ind>maybe</broadcast-ind>`},
		{"session-type", `<session-type><b/></session-type>`},
		{"boolean of Unicode spaces", "<broadcast-ind>\u00a0true</broadcast-ind>"},
	} {
		doc := `<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>` + c.elem +
			`<mcptt-calling-user-id type="Normal"><mcpttURI> sip:alice@x </mcpttURI></mcptt-calling-user-id>` +
			`</mcptt-Params></mcpttinfo>`

		info, err := Decode([]byte(doc))
		if !errors.Is(err, ErrValue) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}

		bad, err := xmltree.Parse([]byte(`<x xmlns="urn:3gpp:ns:mcpttInfo:1.0">` + c.elem + `</x>`))
		if err != nil {
			t.Fatal(err)
		}

		want := &Params{CallingUserID: URI("sip:alice@x"), Other: bad.Elements()}
		if !reflect.DeepEqual(info.Params, want) {
			t.Errorf("%s: %+v", c.name, info.Params)
		}

		// §10.1.1.3.1.1 step 5: set, and relayed with the element it could not decode.
		info.Params.CallingUserID = URI("sip:bob@x")

		out, err := Encode(info)
		if err != nil {
			t.Fatal(err)
		}

		if again, _ := Decode(out); !reflect.DeepEqual(again.Params.Other, want.Other) {
			t.Errorf("%s: relayed %s", c.name, out)
		}
	}

	info, err := Decode([]byte(`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>` +
		`<session-type>private</session-type><session-type>chat</session-type>` +
		`<mcptt-client-id><mcpttString>a</mcpttString></mcptt-client-id>` +
		`<mcptt-client-id><mcpttString>b</mcpttString></mcptt-client-id>` +
		`<alert-ind><mcpttBoolean>x</mcpttBoolean></alert-ind>` +
		`<alert-ind><mcpttBoolean> 1 </mcpttBoolean></alert-ind>` +
		`</mcptt-Params><mcptt-Params><mc-org>second</mc-org></mcptt-Params></mcpttinfo>`))
	if !errors.Is(err, ErrValue) {
		t.Fatal(err)
	}

	p := info.Params
	if p.SessionType != SessionPrivate || !reflect.DeepEqual(p.ClientID, Text("a")) || !reflect.DeepEqual(p.AlertInd, Bool(true)) {
		t.Errorf("first well-formed counts: %+v", p)
	}

	var kept []string
	for _, e := range p.Other {
		kept = append(kept, e.Name.Local)
	}

	if !reflect.DeepEqual(kept, []string{"session-type", "mcptt-client-id", "alert-ind"}) || len(info.Other) != 1 ||
		info.Other[0].Name.Local != "mcptt-Params" {
		t.Errorf("kept %v, %+v", kept, info.Other)
	}
}

// F.1.2: the <anyExt> alternative of contentType, for a value of a later release.
func TestContentExtension(t *testing.T) {
	info, err := Decode([]byte(`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params>` +
		`<emergency-ind><anyExt><later>x</later></anyExt></emergency-ind></mcptt-Params></mcpttinfo>`))
	if err != nil {
		t.Fatal(err)
	}

	c := info.Params.EmergencyInd
	if c.Type != Normal || c.Kind != KindOther || c.Element.Name.Local != "anyExt" {
		t.Fatalf("%+v", c)
	}

	if _, ok := c.Bool(); ok {
		t.Error("a later value taken as a boolean")
	}

	roundTrip(t, info)
}

// F.1.3 8: an <emergency-ind> of "false" cancels an emergency call; absent, it does not.
func TestBool(t *testing.T) {
	var absent *Content

	if v, ok := Bool(false).Bool(); v || !ok {
		t.Error("false")
	}

	if _, ok := absent.Bool(); ok {
		t.Error("absent")
	}

	if _, ok := EncryptedContent(xmltree.New("http://www.w3.org/2001/04/xmlenc#", "EncryptedData")).Bool(); ok {
		t.Error("encrypted")
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"<mcpttinfo",
		`<mcpttinfo/>`,
		`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:2.0"/>`,
		`<mcptt-info xmlns="urn:3gpp:ns:mcpttInfo:1.0"/>`,
		`<?xml version="1.0"?><mcpttinfo><mcptt-Params><session-type>prearranged</session-type></mcptt-Params></mcptt-info>`,
		`<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0">` + strings.Repeat("x", maxSize) + `</mcpttinfo>`,
	} {
		if info, err := Decode([]byte(in)); err == nil || errors.Is(err, ErrValue) {
			t.Errorf("Decode(%.60q) = %+v, %v", in, info, err)
		}
	}
}

func TestEncodeErrors(t *testing.T) {
	for _, p := range []Params{
		{CallingUserID: Text("sip:alice@x")},
		{EmergencyInd: &Content{Type: Normal, Kind: KindBoolean, Value: "1"}},
		{RequestURI: &Content{Type: Encrypted, Kind: KindURI, Value: "sip:a@b"}},
		{RequestURI: &Content{Type: Normal, Kind: KindOther}},
		{MCOrg: "\x00"},
	} {
		if b, err := Encode(Info{Params: &p}); err == nil {
			t.Errorf("Encode(%+v) = %s", p, b)
		}
	}
}

func TestEncode(t *testing.T) {
	yes := true
	info := Info{
		Params: &Params{
			SessionType:   SessionPrearranged,
			RequestURI:    URI("sip:group1@x"),
			CalledPartyID: URI("sip:bob@x"),
			EmergencyInd:  Bool(true),
			BroadcastInd:  &yes,
			FloorState:    FloorIdle,
			MKFCGKTPs:     xmltree.New(Namespace, "MKFC-GKTPs", xmltree.New("urn:3gpp:ns:mcpttGKTP:1.0", "GKTP")),
			Ext:           []*xmltree.Element{xmltree.New(Namespace, "used-priority", xmltree.Text("4"))},
		},
		Other: []*xmltree.Element{xmltree.New("urn:x", "x")},
	}

	b, err := Encode(info)
	if err != nil {
		t.Fatal(err)
	}

	want := `<mcpttinfo xmlns="urn:3gpp:ns:mcpttInfo:1.0"><mcptt-Params><session-type>prearranged</session-type>` +
		`<mcptt-request-uri type="Normal"><mcpttURI>sip:group1@x</mcpttURI></mcptt-request-uri>` +
		`<mcptt-called-party-id type="Normal"><mcpttURI>sip:bob@x</mcpttURI></mcptt-called-party-id>` +
		`<emergency-ind type="Normal"><mcpttBoolean>true</mcpttBoolean></emergency-ind>` +
		`<broadcast-ind>true</broadcast-ind><floor-state>floor-idle</floor-state>` +
		`<MKFC-GKTPs><GKTP xmlns="urn:3gpp:ns:mcpttGKTP:1.0"/></MKFC-GKTPs>` +
		`<anyExt><used-priority>4</used-priority></anyExt></mcptt-Params><x xmlns="urn:x"/></mcpttinfo>`
	if got := strings.TrimPrefix(string(b), xml.Header); got != want {
		t.Errorf("Encode\n%s\nwant\n%s", got, want)
	}

	roundTrip(t, info)
}

func FuzzMcpttInfo(f *testing.F) {
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
		info, err := Decode(b)
		if err != nil && !errors.Is(err, ErrValue) {
			return
		}

		roundTripKept(t, info, err)
	})
}
