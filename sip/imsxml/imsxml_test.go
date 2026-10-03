package imsxml

import (
	"reflect"
	"strings"
	"testing"
)

func TestEncodeEmergency(t *testing.T) {
	b, err := Encode(Emergency("Emergency calls are not supported"))
	if err != nil {
		t.Fatal(err)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<ims-3gpp version="1"><alternative-service><type>emergency</type><reason>Emergency calls are not supported</reason></alternative-service></ims-3gpp>`
	if string(b) != want {
		t.Errorf("Encode:\n%s\nwant\n%s", b, want)
	}
}

func TestEncode(t *testing.T) {
	for _, tc := range []struct {
		d    IMS3GPP
		want string
	}{
		{
			IMS3GPP{Version: Version, AlternativeService: &AlternativeService{Type: TypeEmergency, Action: ActionEmergencyRegistration}},
			`<ims-3gpp version="1"><alternative-service><type>emergency</type><reason></reason><action>emergency-registration</action></alternative-service></ims-3gpp>`,
		},
		{
			IMS3GPP{Version: Version, AlternativeService: &AlternativeService{Type: TypeRestoration, Reason: "a < b & c", Action: ActionInitialRegistration}},
			`<ims-3gpp version="1"><alternative-service><type>restoration</type><reason>a &lt; b &amp; c</reason><action>initial-registration</action></alternative-service></ims-3gpp>`,
		},
		{
			IMS3GPP{Version: "1.0", ServiceInfo: "opaque"},
			`<ims-3gpp version="1.0"><service-info>opaque</service-info></ims-3gpp>`,
		},
	} {
		b, err := Encode(tc.d)
		if err != nil {
			t.Fatalf("Encode(%+v): %v", tc.d, err)
		}

		if got := strings.TrimPrefix(string(b), `<?xml version="1.0" encoding="UTF-8"?>`+"\n"); got != tc.want {
			t.Errorf("Encode:\n%s\nwant\n%s", got, tc.want)
		}

		d, err := Decode(b)
		if err != nil {
			t.Fatalf("Decode(%s): %v", b, err)
		}

		if !reflect.DeepEqual(d, tc.d) {
			t.Errorf("Decode(Encode(%+v)) = %+v", tc.d, d)
		}
	}
}

func TestEncodeErrors(t *testing.T) {
	for _, d := range []IMS3GPP{
		{AlternativeService: &AlternativeService{Type: TypeEmergency}},
		{Version: "v1", ServiceInfo: "x"},
		{Version: "1.", ServiceInfo: "x"},
		{Version: Version, AlternativeService: &AlternativeService{Reason: "no type"}},
		{Version: Version, AlternativeService: &AlternativeService{Type: TypeEmergency}, ServiceInfo: "both"},
	} {
		if b, err := Encode(d); err == nil {
			t.Errorf("Encode(%+v) = %s, want error", d, b)
		}
	}
}

func TestDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want IMS3GPP
	}{
		{
			"indented",
			`<?xml version="1.0" encoding="UTF-8"?>
<ims-3gpp version="1">
  <alternative-service>
    <type>emergency</type>
    <reason>Emergency call</reason>
    <action>emergency-registration</action>
  </alternative-service>
</ims-3gpp>`,
			IMS3GPP{Version: "1", AlternativeService: &AlternativeService{Type: TypeEmergency, Reason: "Emergency call", Action: ActionEmergencyRegistration}},
		},
		{
			"namespaced, extensions and a second type",
			`<x:ims-3gpp xmlns:x="urn:example" version="1" foo="bar"><x:alternative-service><x:type> emergency </x:type><x:reason/>` +
				`<x:action>anonymous-emergencycall</x:action><x:type>restoration</x:type><ext>1</ext></x:alternative-service><other/></x:ims-3gpp>`,
			IMS3GPP{Version: "1", AlternativeService: &AlternativeService{Type: TypeEmergency, Action: ActionAnonymousEmergencyCall}},
		},
		{
			"service-info",
			`<ims-3gpp version="1"><service-info>&lt;x/&gt;</service-info></ims-3gpp>`,
			IMS3GPP{Version: "1", ServiceInfo: "<x/>"},
		},
		{
			"us-ascii",
			`<?xml version="1.0" encoding="US-ASCII"?><ims-3gpp version=" 2 "/>`,
			IMS3GPP{Version: "2"},
		},
	} {
		got, err := Decode([]byte(tc.in))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}

		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v (%+v), want %+v (%+v)", tc.name, got, got.AlternativeService, tc.want, tc.want.AlternativeService)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"<ims-3gpp",
		`<reginfo version="1"/>`,
		`<ims-3gpp/>`,
		`<ims-3gpp version="one"/>`,
		`<ims-3gpp version="1"><alternative-service><reason>x</reason></alternative-service></ims-3gpp>`,
		`<?xml version="1.0" encoding="EBCDIC"?><ims-3gpp version="1"/>`,
		`<ims-3gpp version="1">` + strings.Repeat(" ", maxDocumentSize) + `</ims-3gpp>`,
	} {
		if d, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%.60q) = %+v, want error", in, d)
		}
	}
}

func FuzzDecode(f *testing.F) {
	for _, d := range []IMS3GPP{
		Emergency("x"),
		{Version: "1", AlternativeService: &AlternativeService{Type: TypeRestoration, Reason: "r", Action: ActionInitialRegistration}},
		{Version: "1.0", ServiceInfo: "<a>"},
	} {
		b, err := Encode(d)
		if err != nil {
			f.Fatal(err)
		}

		f.Add(b)
	}

	f.Add([]byte(`<x:ims-3gpp xmlns:x="u" version="1"><x:alternative-service><x:type>t</x:type><x:reason/></x:alternative-service></x:ims-3gpp>`))

	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := Decode(data)
		if err != nil {
			return
		}

		b, err := Encode(d)
		if err != nil {
			t.Fatalf("decoded %+v does not encode: %v", d, err)
		}

		again, err := Decode(b)
		if err != nil {
			t.Fatalf("%s does not decode: %v", b, err)
		}

		if !reflect.DeepEqual(again, d) {
			t.Fatalf("unstable: %+v became %+v", d, again)
		}
	})
}
