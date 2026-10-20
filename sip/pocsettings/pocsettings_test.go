package pocsettings

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

func decodeFile(t *testing.T, name string) Settings {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	s, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// stable checks that s encodes, decodes to what encodes again the same: the elements kept, malformed, too.
func stable(t *testing.T, s Settings) {
	t.Helper()

	b, err := Encode(s)
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

// RFC 4354 §6.2: an entity of the four settings.
func TestExample62(t *testing.T) {
	s := decodeFile(t, "rfc4354-6.2.xml")

	en := s.Entities[0]
	if len(s.Entities) != 1 || en.ID != "do39s8zksn2d98x" || !en.IncomingSessionBarring.Active || en.AnswerMode.AnswerMode != Automatic ||
		en.IncomingPersonalAlertBarring.Active || !en.SimultaneousSessionsSupport.Active || len(en.Other) != 0 {
		t.Fatalf("%+v", s)
	}

	stable(t, s)
}

// TS 24.379 §7.4.1.2.2: the selected MCPTT user profile index and the multiplex support kept in the entity's Other.
func TestMCPTT(t *testing.T) {
	s := decodeFile(t, "ts24379-7.4.1.2.2.xml")

	en := s.Entities[0]
	if en.ID != "urn:uuid:00000000-0000-1000-8000-0123456789ab" || en.AnswerMode.AnswerMode != Manual || len(en.Other) != 2 ||
		en.Other[0].Name != (xml.Name{Space: "urn:3gpp:mcsSettings:1.0", Local: "selected-user-profile-index"}) ||
		en.Other[1].Text() != "true" {
		t.Fatalf("%+v", en)
	}

	stable(t, s)
}

// RFC 4354 §6.1: the extension attributes, of any namespace, and elements where the schema allows them.
func TestExtensions(t *testing.T) {
	s := decodeFile(t, "extensions.xml")

	if len(s.Entities) != 3 || len(s.Attr) != 2 || len(s.Other) != 1 {
		t.Fatalf("%+v", s)
	}

	a := s.Entities[0]
	if !reflect.DeepEqual(a.Attr, []xml.Attr{{Name: xml.Name{Space: "urn:example:ext", Local: "tag"}, Value: "entity"}, {Name: xml.Name{Local: "since"}, Value: "2026"}}) ||
		len(a.Other) != 1 {
		t.Errorf("entity %+v", a)
	}

	if isb := a.IncomingSessionBarring; isb.Active || len(isb.Attr) != 1 || len(isb.Other) != 2 || isb.Other[0].Name.Local != "incoming-session-barring" {
		t.Errorf("isb %+v", isb)
	}

	if sss := a.SimultaneousSessionsSupport; sss.Active || sss.Attr[0].Value != "user" || sss.Other[0].Name != name("answer-mode") {
		t.Errorf("sss %+v", sss)
	}

	if b := s.Entities[1]; b.AnswerMode.AnswerMode != Automatic || len(b.AnswerMode.Other) != 1 || !b.IncomingPersonalAlertBarring.Active {
		t.Errorf("entity %+v", b)
	}

	stable(t, s)
}

// RFC 4354 §6.1: a malformed element kept, not decoded; the first of an element given once counting.
func TestMalformed(t *testing.T) {
	for _, c := range []struct {
		name, body string
		check      func(Settings) bool
	}{
		{"entity without id", `<entity/>`, func(s Settings) bool { return len(s.Entities) == 0 && len(s.Other) == 1 }},
		{"entity id twice", `<entity id="a"/><entity id="a"><am-settings><answer-mode>manual</answer-mode></am-settings></entity>`, func(s Settings) bool {
			return len(s.Entities) == 1 && s.Entities[0].AnswerMode == nil && len(s.Other) == 1
		}},
		{"settings without their element", `<entity id="a"><isb-settings/></entity>`, func(s Settings) bool {
			return s.Entities[0].IncomingSessionBarring == nil && len(s.Entities[0].Other) == 1
		}},
		{"settings of another element first", `<entity id="a"><sss-settings><incoming-session-barring active="true"/></sss-settings></entity>`, func(s Settings) bool {
			return s.Entities[0].SimultaneousSessionsSupport == nil && len(s.Entities[0].Other) == 1
		}},
		{"active", `<entity id="a"><ipab-settings><incoming-personal-alert-barring active="yes"/></ipab-settings></entity>`, func(s Settings) bool {
			return s.Entities[0].IncomingPersonalAlertBarring == nil && len(s.Entities[0].Other) == 1
		}},
		{"active missing", `<entity id="a"><isb-settings><incoming-session-barring/></isb-settings></entity>`, func(s Settings) bool {
			return s.Entities[0].IncomingSessionBarring == nil
		}},
		{"children of a value", `<entity id="a"><isb-settings><incoming-session-barring active="1"><x/></incoming-session-barring></isb-settings></entity>`, func(s Settings) bool {
			return s.Entities[0].IncomingSessionBarring == nil
		}},
		// TS 24.379 §7.3.2 6) a) names the settings "auto-answer" and "manual-answer"; RFC 4354 §6.1 codes them.
		{"answer mode", `<entity id="a"><am-settings><answer-mode>auto-answer</answer-mode></am-settings></entity>`, func(s Settings) bool {
			return s.Entities[0].AnswerMode == nil && len(s.Entities[0].Other) == 1
		}},
	} {
		s, err := Decode([]byte(`<poc-settings xmlns="urn:oma:params:xml:ns:poc:poc-settings">` + c.body + `</poc-settings>`))
		if !errors.Is(err, ErrValue) || !c.check(s) {
			t.Errorf("%s: %+v, %v", c.name, s, err)
			continue
		}

		stable(t, s)
	}

	// Given more than once: the first counts, malformed or not, the others kept, malformed.
	s, err := Decode([]byte(`<poc-settings xmlns="urn:oma:params:xml:ns:poc:poc-settings"><entity id="a">` +
		`<am-settings><answer-mode>manual</answer-mode></am-settings><am-settings><answer-mode>automatic</answer-mode></am-settings>` +
		`<isb-settings/><isb-settings><incoming-session-barring active="true"/></isb-settings>` +
		`</entity></poc-settings>`))
	if en := s.Entities[0]; !errors.Is(err, ErrValue) || en.AnswerMode.AnswerMode != Manual || en.IncomingSessionBarring != nil ||
		len(en.Other) != 3 {
		t.Errorf("%+v, %v", s, err)
	}

	stable(t, s)
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		``,
		`<poc-settings/>`,
		`<entity xmlns="urn:oma:params:xml:ns:poc:poc-settings" id="a"/>`,
		`<poc-settings xmlns="urn:oma:params:xml:ns:poc:poc-settings">` + strings.Repeat(" ", maxSize) + `</poc-settings>`,
	} {
		if s, err := Decode([]byte(in)); err == nil || errors.Is(err, ErrValue) {
			t.Errorf("Decode(%.60q) = %+v, %v", in, s, err)
		}
	}
}

func TestEncode(t *testing.T) {
	s := Settings{Entities: []Entity{{
		ID:                          "a",
		AnswerMode:                  &AnswerModeSettings{AnswerMode: Manual},
		SimultaneousSessionsSupport: &Toggle{Active: true},
		Other:                       []*xmltree.Element{xmltree.New("urn:3gpp:mcsSettings:1.0", "multiplex-support", xmltree.Text("true"))},
	}}}

	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<poc-settings xmlns="urn:oma:params:xml:ns:poc:poc-settings">` +
		`<entity id="a"><am-settings><answer-mode>manual</answer-mode></am-settings>` +
		`<sss-settings><simultaneous-sessions-support active="true"/></sss-settings>` +
		`<multiplex-support xmlns="urn:3gpp:mcsSettings:1.0">true</multiplex-support></entity></poc-settings>`
	if string(b) != want {
		t.Errorf("Encode\n%s\nwant\n%s", b, want)
	}

	for _, s := range []Settings{
		{Entities: []Entity{{ID: "a"}, {ID: "a"}}},
		{Entities: []Entity{{ID: "a", AnswerMode: &AnswerModeSettings{AnswerMode: "auto-answer"}}}},
		{Entities: []Entity{{ID: "a", Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: "b"}}}}},
	} {
		if b, err := Encode(s); err == nil {
			t.Errorf("Encode(%+v) = %s", s, b)
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
		s, err := Decode(b)
		if err != nil && !errors.Is(err, ErrValue) {
			return
		}

		stable(t, s)

		// A document decoded without error encodes to one decoded without error.
		if err == nil {
			out, _ := Encode(s)
			if _, err := Decode(out); err != nil {
				t.Fatalf("Decode(Encode): %v\n%s\n%s", err, b, out)
			}
		}
	})
}
