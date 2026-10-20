package conferenceinfo

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip/xmltree"
	"github.com/ellanetworks/ims/sip/xsd"
)

func decodeFile(t *testing.T, name string) Info {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	i, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}

	return i
}

// stable checks that i encodes, decodes to what encodes again the same: the elements kept, malformed, too.
func stable(t *testing.T, i Info) {
	t.Helper()

	b, err := Encode(i)
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

// RFC 4575 §7.1: a full document.
func TestExample71(t *testing.T) {
	i := decodeFile(t, "rfc4575-7.1.xml")

	if i.Entity != "sips:conf233@example.com" || i.State != Full || i.Version != 1 ||
		i.Description.Subject != "Agenda: This month's goals" || *i.ConferenceState.UserCount != 33 {
		t.Fatalf("%+v", i)
	}

	if u := i.Description.ServiceURIs.Entries; len(u) != 1 || u[0].URI != "http://sharepoint/salesgroup/" || u[0].Purpose != WebPage {
		t.Errorf("service uris %+v", u)
	}

	users := i.Users.Users
	if len(users) != 2 || users[0].Entity != "sip:bob@example.com" || users[0].State != Full || users[0].DisplayText != "Bob Hoskins" {
		t.Fatalf("users %+v", users)
	}

	ep := users[0].Endpoints[0]
	if ep.Entity != "sip:bob@pc33.example.com" || ep.Status != Disconnected || ep.DisconnectionMethod != Departed ||
		!ep.DisconnectionInfo.When.Equal(time.Date(2005, 3, 4, 20, 0, 0, 0, time.UTC)) ||
		ep.DisconnectionInfo.Reason != "bad voice quality" || ep.DisconnectionInfo.By != "sip:mike@example.com" {
		t.Errorf("endpoint %+v", ep)
	}

	want := Media{ID: "1", DisplayText: "main audio", Type: "audio", Label: "34567", SrcID: "432424", Status: SendRecv}
	if !reflect.DeepEqual(ep.Media, []Media{want}) {
		t.Errorf("media %+v", ep.Media)
	}

	if ep := users[1].Endpoints[0]; ep.Entity != "sip:4kfk4j392jsu@example.com;grid=433kj4j3u" || ep.JoiningMethod != DialedOut ||
		ep.JoiningInfo.Reason != "" || ep.JoiningInfo.By != "sip:mike@example.com" {
		t.Errorf("endpoint %+v", ep)
	}

	stable(t, i)
}

// RFC 4575 §7.2: a partial document of every element of the schema but <cascaded-focus>.
func TestExample72(t *testing.T) {
	i := decodeFile(t, "rfc4575-7.2.xml")

	d := i.Description
	if i.State != Partial || i.Version != 5 || d.DisplayText != "Weekly Sales Meeting" || d.FreeText != "We will start strict on time" ||
		!reflect.DeepEqual(d.Keywords, []string{"sales", "meeting", "weekly"}) || len(d.ConfURIs.Entries) != 3 ||
		d.ConfURIs.Entries[0].DisplayText != "TTI Bridge" || d.ConfURIs.Entries[2].Purpose != Streaming ||
		d.ServiceURIs.Entries[1].Purpose != Recording || *d.MaximumUserCount != 100 {
		t.Fatalf("%+v", d)
	}

	if !reflect.DeepEqual(d.AvailableMedia.Entries, []Medium{
		{Label: "34567", DisplayText: "main audio", Type: "audio", Status: SendRecv},
		{Label: "34569", DisplayText: "main video", Type: "video", Status: Inactive},
	}) {
		t.Errorf("available media %+v", d.AvailableMedia)
	}

	if h := i.Host; h.DisplayText != "Sales Host" || h.WebPage != "http://sharepoint/salesgroup/hosts/" || h.URIs.Entries[0].URI != "sip:sales@example.com" {
		t.Errorf("host %+v", h)
	}

	if s := i.ConferenceState; *s.UserCount != 32 || !*s.Active || *s.Locked {
		t.Errorf("conference state %+v", s)
	}

	u := i.Users.Users[0]
	if u.State != "" || u.AssociatedAORs.Entries[0].URI != "mailto:bob@example.com" || !reflect.DeepEqual(u.Roles, &Roles{Entries: []string{"participant"}}) ||
		!reflect.DeepEqual(u.Languages, []string{"en"}) {
		t.Errorf("user %+v", u)
	}

	ep := u.Endpoints[0]
	if ep.Referred.Reason != "expert required" || ep.Status != Disconnecting || ep.JoiningInfo.Reason != "invitation" ||
		ep.DisconnectionMethod != Booted {
		t.Errorf("endpoint %+v", ep)
	}

	if !reflect.DeepEqual(ep.CallInfo, &CallInfo{SIP: &SIPDialog{DisplayText: "full info", CallID: "hsjh8980vhsb78", FromTag: "vav738dvbs", ToTag: "8954jgjg8432"}}) {
		t.Errorf("call info %+v", ep.CallInfo.SIP)
	}

	if r := i.SidebarsByRef; r.State != Partial || len(r.Entries) != 2 || r.Entries[1].DisplayText != "private with Peter" {
		t.Errorf("sidebars by ref %+v", r)
	}

	if v := i.SidebarsByVal; v.State != Partial || len(v.Entries) != 1 || v.Entries[0].Entity != "sips:conf233@example.com;grid=77" ||
		v.Entries[0].State != Partial || len(v.Entries[0].Users.Users) != 3 || v.Entries[0].Users.Users[2].Entity != "sip:dan@example.com" {
		t.Errorf("sidebars by val %+v", v)
	}

	stable(t, i)
}

// TS 24.379 §6.3.3.4: the functional alias of §10.1.3.6.1 kept in the endpoint's Other.
func TestMCPTT(t *testing.T) {
	i := decodeFile(t, "ts24379-6.3.3.4.xml")

	alice := i.Users.Users[0]
	if i.Entity != "sip:group1@mcptt.example.com" || len(i.Users.Users) != 2 || !reflect.DeepEqual(alice.Roles.Entries, []string{"dispatcher"}) {
		t.Fatalf("%+v", i)
	}

	ep := alice.Endpoints[0]
	if ep.Status != Connected || len(ep.Other) != 1 || ep.Other[0].Name.Space != "urn:3gpp:ns:mcpttConfInfo:1.0" ||
		ep.Other[0].Text() != "sip:dispatch-1@mcptt.example.com" {
		t.Errorf("endpoint %+v", ep)
	}

	stable(t, i)

	b, err := Encode(i)
	if err != nil || !strings.Contains(string(b), `<functional-alias xmlns="urn:3gpp:ns:mcpttConfInfo:1.0">sip:dispatch-1@mcptt.example.com</functional-alias>`) {
		t.Errorf("%s, %v", b, err)
	}
}

// RFC 4575 §6: the extension elements kept where the schema allows them, the lexical forms of the schema's types.
func TestExtensions(t *testing.T) {
	i := decodeFile(t, "extensions.xml")

	d := i.Description
	if i.Version != 4294967295 || !reflect.DeepEqual(d.Keywords, []string{"a", "b", "c"}) || *d.MaximumUserCount != 10 ||
		len(d.Other) != 1 || len(d.ConfURIs.Entries[0].Other) != 1 || len(d.AvailableMedia.Entries[0].Other) != 1 {
		t.Fatalf("%+v", d)
	}

	if m := d.ConfURIs.Entries[0].Modified; m.When.UnixMilli() != time.Date(2026, 10, 20, 6, 0, 0, 500e6, time.UTC).UnixMilli() ||
		m.Reason != "Reason: SIP;cause=200" {
		t.Errorf("modified %+v", m)
	}

	if s := i.ConferenceState; !*s.Active || *s.Locked || len(s.Other) != 1 || len(i.Host.Other) != 1 {
		t.Errorf("conference state %+v, host %+v", s, i.Host)
	}

	u := i.Users.Users[0]
	if u.DisplayText != "A & co" || !reflect.DeepEqual(u.Languages, []string{"en", "fr-CA"}) || u.CascadedFocus != "sip:focus@other.example.com" ||
		len(u.Other) != 1 || len(i.Users.Other) != 1 {
		t.Errorf("user %+v", u)
	}

	ep := u.Endpoints[0]
	if ep.Status != MutedViaFocus || ep.JoiningMethod != FocusOwner || ep.JoiningInfo == nil || ep.DisconnectionMethod != Busy ||
		len(ep.Media) != 2 || len(ep.Media[0].Other) != 1 || ep.CallInfo.SIP != nil || len(ep.CallInfo.Other) != 1 || len(ep.Other) != 1 {
		t.Errorf("endpoint %+v", ep)
	}

	if s := u.Endpoints[1].CallInfo.SIP; s.CallID != "c" || len(s.Other) != 1 {
		t.Errorf("sip %+v", s)
	}

	if v := i.SidebarsByVal.Entries[0]; v.Users == nil || v.SidebarsByVal.Entries[0].State != Deleted {
		t.Errorf("sidebar %+v", v)
	}

	if len(i.Other) != 2 || i.Other[1].Text() != "again" {
		t.Errorf("other %+v", i.Other)
	}

	tag := xml.Name{Space: "urn:example:ext", Local: "tag"}
	if !reflect.DeepEqual(i.Attr, []xml.Attr{{Name: tag, Value: "conference"}}) || len(d.AvailableMedia.Entries[0].Attr) != 2 ||
		u.Attr[0].Value != "user" || u.Roles.Attr[0].Value != "roles" || len(u.Roles.Entries) != 2 || ep.Media[0].Attr[0].Value != "media" || ep.Referred.Attr[0].Value != "referred" {
		t.Errorf("attributes %+v", i.Attr)
	}

	stable(t, i)
}

// RFC 4575 §6: a malformed element kept, not decoded; the first of an element given once counting.
func TestMalformed(t *testing.T) {
	for _, c := range []struct {
		name, body string
		check      func(Info) bool
	}{
		{"state", `<users state="gone"/>`, func(i Info) bool { return i.Users == nil && i.Other[0].Name.Local == "users" }},
		{"user without entity", `<users><user/></users>`, func(i Info) bool {
			return len(i.Users.Users) == 0 && len(i.Users.Other) == 1
		}},
		{"user twice", `<users><user entity="sip:a@x"/><user entity=" sip:a@x "/></users>`, func(i Info) bool {
			return len(i.Users.Users) == 1 && len(i.Users.Other) == 1
		}},
		{"endpoint status", `<users><user entity="sip:a@x"><endpoint entity="e"><status>away</status></endpoint></user></users>`, func(i Info) bool {
			ep := i.Users.Users[0].Endpoints[0]
			return ep.Status == "" && ep.Other[0].Name.Local == "status"
		}},
		{"media without id", `<users><user entity="sip:a@x"><endpoint entity="e"><media/></endpoint></user></users>`, func(i Info) bool {
			ep := i.Users.Users[0].Endpoints[0]
			return len(ep.Media) == 0 && len(ep.Other) == 1
		}},
		{"execution of an unknown child", `<users><user entity="sip:a@x"><endpoint entity="e"><referred><who/></referred></endpoint></user></users>`, func(i Info) bool {
			ep := i.Users.Users[0].Endpoints[0]
			return ep.Referred == nil && len(ep.Other) == 1
		}},
		{"when", `<users><user entity="sip:a@x"><endpoint entity="e"><joining-info><when>today</when></joining-info></endpoint></user></users>`, func(i Info) bool {
			return i.Users.Users[0].Endpoints[0].JoiningInfo == nil
		}},
		{"sip without to-tag", `<users><user entity="sip:a@x"><endpoint entity="e"><call-info><sip><call-id>c</call-id><from-tag>f</from-tag></sip></call-info></endpoint></user></users>`, func(i Info) bool {
			c := i.Users.Users[0].Endpoints[0].CallInfo
			return c.SIP == nil && len(c.Other) == 1
		}},
		{"roles without entry", `<users><user entity="sip:a@x"><roles/></user></users>`, func(i Info) bool {
			return i.Users.Users[0].Roles == nil && len(i.Users.Users[0].Other) == 1
		}},
		{"roles of another element", `<users><user entity="sip:a@x"><roles><entry>a</entry><x/></roles></user></users>`, func(i Info) bool {
			return i.Users.Users[0].Roles == nil && len(i.Users.Users[0].Other) == 1
		}},
		{"uris without entry", `<sidebars-by-ref/>`, func(i Info) bool { return i.SidebarsByRef == nil && len(i.Other) == 1 }},
		{"entry without uri", `<sidebars-by-ref><entry><uri>sip:s@x</uri></entry><entry/></sidebars-by-ref>`, func(i Info) bool {
			return len(i.SidebarsByRef.Entries) == 1 && len(i.SidebarsByRef.Other) == 1
		}},
		{"uri twice", `<sidebars-by-ref><entry><uri>sip:s@x</uri></entry><entry><uri>sip:s@x</uri></entry></sidebars-by-ref>`, func(i Info) bool {
			return len(i.SidebarsByRef.Entries) == 1 && len(i.SidebarsByRef.Other) == 1
		}},
		{"medium without type", `<conference-description><available-media><entry label="1"/></available-media></conference-description>`, func(i Info) bool {
			return i.Description.AvailableMedia == nil && len(i.Description.Other) == 1
		}},
		{"user count", `<conference-state><user-count>-1</user-count><active>yes</active></conference-state>`, func(i Info) bool {
			return i.ConferenceState.UserCount == nil && i.ConferenceState.Active == nil && len(i.ConferenceState.Other) == 2
		}},
		{"sidebar without entity", `<sidebars-by-val><entry/></sidebars-by-val>`, func(i Info) bool {
			return len(i.SidebarsByVal.Entries) == 0 && len(i.SidebarsByVal.Other) == 1
		}},
		{"elements in a string", `<conference-description><subject><b/></subject></conference-description>`, func(i Info) bool {
			return i.Description.Subject == "" && len(i.Description.Other) == 1
		}},
	} {
		i, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1">` + c.body + `</conference-info>`))
		if !errors.Is(err, ErrValue) || !c.check(i) {
			t.Errorf("%s: %+v, %v", c.name, i, err)
			continue
		}

		stable(t, i)
	}

	// Given more than the schema allows: the first counts, malformed or not, the others kept, malformed.
	for _, c := range []struct {
		name, body string
		check      func(Info) bool
	}{
		{"subject", `<conference-description><subject>a</subject><subject>b</subject></conference-description>`, func(i Info) bool {
			return i.Description.Subject == "a" && len(i.Description.Other) == 1
		}},
		{
			"conference-description", `<conference-description><subject>a</subject></conference-description><conference-description/>`,
			func(i Info) bool { return i.Description.Subject == "a" && len(i.Other) == 1 },
		},
		{"users", `<users><user entity="sip:a@x"/></users><users/>`, func(i Info) bool {
			return len(i.Users.Users) == 1 && len(i.Other) == 1
		}},
		{
			"status", `<users><user entity="sip:a@x"><endpoint entity="e"><status>connected</status><status>pending</status></endpoint></user></users>`,
			func(i Info) bool {
				ep := i.Users.Users[0].Endpoints[0]
				return ep.Status == Connected && len(ep.Other) == 1
			},
		},
		{
			"status malformed first", `<users><user entity="sip:a@x"><endpoint entity="e"><status>away</status><status>pending</status></endpoint></user></users>`,
			func(i Info) bool {
				ep := i.Users.Users[0].Endpoints[0]
				return ep.Status == "" && len(ep.Other) == 2
			},
		},
		{"sip", `<users><user entity="sip:a@x"><endpoint entity="e"><call-info><sip><call-id>c</call-id><from-tag>f</from-tag><to-tag>t</to-tag></sip>` +
			`<sip><call-id>d</call-id><from-tag>f</from-tag><to-tag>t</to-tag></sip></call-info></endpoint></user></users>`, func(i Info) bool {
			c := i.Users.Users[0].Endpoints[0].CallInfo
			return c.SIP.CallID == "c" && len(c.Other) == 1
		}},
		{"when", `<users><user entity="sip:a@x"><endpoint entity="e"><joining-info><when>2005-03-04T20:00:00Z</when><when>2005-03-04T20:00:00Z</when>` +
			`</joining-info></endpoint></user></users>`, func(i Info) bool {
			return i.Users.Users[0].Endpoints[0].JoiningInfo == nil
		}},
	} {
		i, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1">` + c.body + `</conference-info>`))
		if !errors.Is(err, ErrValue) || !c.check(i) {
			t.Errorf("%s twice: %+v, %v", c.name, i, err)
			continue
		}

		stable(t, i)
	}

	// An optional element of the empty string is as none: one of its name may follow.
	i, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1">` +
		`<conference-description><subject/><subject>b</subject><keywords> </keywords><keywords>k</keywords></conference-description></conference-info>`))
	if err != nil || i.Description.Subject != "b" || !reflect.DeepEqual(i.Description.Keywords, []string{"k"}) || len(i.Description.Other) != 0 {
		t.Errorf("%+v, %v", i, err)
	}
}

// RFC 4575 §4.4: an element of state deleted SHOULD NOT have children, a uris-type of entries too.
func TestDeletedURIs(t *testing.T) {
	i, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1" state="partial">` +
		`<sidebars-by-ref state="deleted"/></conference-info>`))
	if err != nil || i.SidebarsByRef == nil || i.SidebarsByRef.State != Deleted || len(i.Other) != 0 {
		t.Fatalf("%+v, %v", i, err)
	}

	b, err := Encode(i)
	if err != nil || !strings.Contains(string(b), `<sidebars-by-ref state="deleted"/>`) {
		t.Errorf("%s, %v", b, err)
	}
}

// RFC 4575 §5.7.2: <when> of the XML dateTime type, of a timezone or not; one of a year Encode cannot write refused.
func TestWhen(t *testing.T) {
	i, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1">` +
		`<users><user entity="sip:a@x"><endpoint entity="e"><referred><when>2005-03-04T20:00:00</when></referred>` +
		`<joining-info><when> 2005-03-04T23:59:60.5+01:00 </when></joining-info></endpoint></user></users></conference-info>`))
	if err != nil {
		t.Fatal(err)
	}

	ep := i.Users.Users[0].Endpoints[0]
	if w := ep.Referred.When; w.Zoned || !w.Equal(time.Date(2005, 3, 4, 20, 0, 0, 0, time.UTC)) {
		t.Errorf("referred %+v", w)
	}

	if w := ep.JoiningInfo.When; !w.Zoned || !w.Equal(time.Date(2005, 3, 4, 23, 0, 0, 500e6, time.UTC)) {
		t.Errorf("joining %+v", w)
	}

	b, err := Encode(i)
	if err != nil || !strings.Contains(string(b), `<when>2005-03-04T20:00:00</when>`) ||
		!strings.Contains(string(b), `<when>2005-03-05T00:00:00.5+01:00</when>`) {
		t.Errorf("%s, %v", b, err)
	}

	stable(t, i)

	if _, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1">` +
		`<users><user entity="sip:a@x"><endpoint entity="e"><referred><when>9999-12-31T24:00:00</when></referred>` +
		`</endpoint></user></users></conference-info>`)); !errors.Is(err, ErrValue) {
		t.Errorf("year 10000: %v", err)
	}

	ep.Referred.When.Time = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

	if b, err := Encode(i); err == nil {
		t.Errorf("year 10000: %s", b)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		``,
		`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x"/>`,
		`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="-1"/>`,
		`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="4294967296"/>`,
		`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" version="1"/>`,
		`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1" state="none"/>`,
		`<conference-info entity="sip:c@x" version="1"/>`,
		`<users xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1"/>`,
		`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" version="1">` + strings.Repeat(" ", maxSize) + `</conference-info>`,
	} {
		if i, err := Decode([]byte(in)); err == nil || errors.Is(err, ErrValue) {
			t.Errorf("Decode(%.80q) = %+v, %v", in, i, err)
		}
	}
}

func TestEncode(t *testing.T) {
	n := uint32(3)
	yes := true
	when := xsd.DateTime{Time: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC), Zoned: true}
	i := Info{Version: 7, Conference: Conference{
		Entity:          "sip:c@x",
		State:           Full,
		Description:     &Description{Subject: "s", Keywords: []string{"a", "b"}, MaximumUserCount: &n},
		ConferenceState: &ConferenceState{Active: &yes},
		Users: &Users{Users: []User{{
			Entity: "sip:a@x",
			Roles:  &Roles{Entries: []string{"r"}},
			Endpoints: []Endpoint{{
				Entity: "e", Status: Connected, JoiningInfo: &Execution{When: &when},
				Media:    []Media{{ID: "1", Type: "audio", Status: SendRecv}},
				CallInfo: &CallInfo{SIP: &SIPDialog{CallID: "c", FromTag: "f", ToTag: "t"}},
				Other:    []*xmltree.Element{xmltree.New("urn:3gpp:ns:mcpttConfInfo:1.0", "functional-alias", xmltree.Text("sip:fa@x"))},
			}},
		}}},
	}}

	b, err := Encode(i)
	if err != nil {
		t.Fatal(err)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" entity="sip:c@x" state="full" version="7">` +
		`<conference-description><subject>s</subject><keywords>a b</keywords><maximum-user-count>3</maximum-user-count></conference-description>` +
		`<conference-state><active>true</active></conference-state>` +
		`<users><user entity="sip:a@x"><roles><entry>r</entry></roles><endpoint entity="e"><status>connected</status>` +
		`<joining-info><when>2026-10-20T08:00:00Z</when></joining-info><media id="1"><type>audio</type><status>sendrecv</status></media>` +
		`<call-info><sip><call-id>c</call-id><from-tag>f</from-tag><to-tag>t</to-tag></sip></call-info>` +
		`<functional-alias xmlns="urn:3gpp:ns:mcpttConfInfo:1.0">sip:fa@x</functional-alias></endpoint></user></users></conference-info>`
	if string(b) != want {
		t.Errorf("Encode\n%s\nwant\n%s", b, want)
	}

	for _, c := range []Conference{
		{},
		{Entity: "sip:c@x", State: "none"},
		{Entity: "sip:c@x", Users: &Users{Users: []User{{Entity: "a"}, {Entity: "a"}}}},
		{Entity: "sip:c@x", Users: &Users{Users: []User{{Entity: "a", Endpoints: []Endpoint{{Status: "away"}}}}}},
		{Entity: "sip:c@x", Users: &Users{Users: []User{{Entity: "a", Endpoints: []Endpoint{{Media: []Media{{ID: "1"}, {ID: "1"}}}}}}}},
		{Entity: "sip:c@x", SidebarsByRef: &URIs{}},
		{Entity: "sip:c@x", SidebarsByRef: &URIs{State: Partial}},
		{Entity: "sip:c@x", Users: &Users{Users: []User{{Entity: "a", Roles: &Roles{}}}}},
		{Entity: "sip:c@x", Users: &Users{Users: []User{{Entity: "a", Roles: &Roles{Entries: []string{"r"}, Attr: []xml.Attr{{Name: xml.Name{Local: "a"}}}}}}}},
		{Entity: "sip:c@x", SidebarsByRef: &URIs{Entries: []URI{{URI: "u"}, {URI: "u"}}}},
		{Entity: "sip:c@x", SidebarsByVal: &Sidebars{Entries: []Conference{{}}}},
		{Entity: "sip:c@x", Description: &Description{AvailableMedia: &AvailableMedia{}}},
		{Entity: "sip:c@x", Attr: []xml.Attr{{Name: xml.Name{Local: "version"}, Value: "1"}}},
		{Entity: "sip:c@x", Host: &Host{Attr: []xml.Attr{{Name: xml.Name{Space: Namespace, Local: "a"}}}}},
	} {
		if b, err := Encode(Info{Conference: c}); err == nil {
			t.Errorf("Encode(%+v) = %s", c, b)
		}
	}
}

// RFC 4575 §4.6: the conference state built from a full notification and the partial ones that follow.
func TestApply(t *testing.T) {
	var local Info

	partial := decodeFile(t, "ts24379-6.3.3.4-partial.xml")
	if err := local.Apply(partial); !errors.Is(err, ErrVersionGap) {
		t.Fatalf("partial first: %v", err)
	}

	full := decodeFile(t, "ts24379-6.3.3.4.xml")
	if err := local.Apply(full); err != nil || local.Version != 1 {
		t.Fatalf("%+v, %v", local, err)
	}

	if err := local.Apply(full); !errors.Is(err, ErrStale) {
		t.Fatalf("full again: %v", err)
	}

	if err := local.Apply(partial); err != nil || local.Version != 2 {
		t.Fatalf("%+v, %v", local, err)
	}

	users := local.Users.Users
	if len(users) != 2 || users[0].Entity != "sip:bob@mcptt.example.com" || users[0].State != "" ||
		users[0].Endpoints[0].Status != Connected || users[1].Entity != "sip:carol@mcptt.example.com" ||
		users[1].Endpoints[0].Status != DialingOut {
		t.Fatalf("users %+v", users)
	}

	// Version 4 does not follow 2; a full document of any newer version replaces the state.
	gap := partial
	gap.Version = 4

	if err := local.Apply(gap); !errors.Is(err, ErrVersionGap) {
		t.Errorf("gap: %v", err)
	}

	full.Version = 9
	if err := local.Apply(full); err != nil || local.Version != 9 || len(local.Users.Users) != 2 || local.Users.Users[0].Entity != "sip:alice@mcptt.example.com" {
		t.Errorf("full: %+v, %v", local, err)
	}

	stable(t, local)
}

// RFC 4575 §4.6 steps 3.1 to 3.3: keyed elements merged, those of no state attribute in the schema field by field,
// those without key replaced as a whole, the extensions by name.
func TestApplyMerge(t *testing.T) {
	decode := func(body string, version string, state State) Info {
		i, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" xmlns:x="urn:x" entity="sip:c@x" ` +
			`state="` + string(state) + `" version="` + version + `">` + body + `</conference-info>`))
		if err != nil {
			t.Fatal(err)
		}

		return i
	}

	local := decode(`<conference-description><subject>s</subject><display-text>d</display-text></conference-description>`+
		`<users><user entity="u"><display-text>U</display-text><endpoint entity="e"><status>connected</status>`+
		`<media id="1" x:m="1"><type>audio</type><status>sendrecv</status></media><media id="2"><type>video</type></media>`+
		`<x:a>1</x:a><x:b>1</x:b></endpoint><endpoint entity="f"/></user></users>`+
		`<sidebars-by-ref><entry><uri>sip:s1</uri><display-text>one</display-text></entry></sidebars-by-ref>`+
		`<sidebars-by-val><entry entity="sip:v"><users><user entity="u"/></users></entry></sidebars-by-val>`, "1", Full)

	update := decode(`<conference-description><subject>t</subject></conference-description>`+
		`<users state="partial"><user entity="u" state="partial"><endpoint entity="e" state="partial">`+
		`<media id="1" x:m="2"><status>recvonly</status></media><media id="3"><type>text</type></media><x:a>2</x:a></endpoint>`+
		`<endpoint entity="f" state="deleted"/><endpoint entity="g" state="deleted"/></user></users>`+
		`<sidebars-by-ref state="partial"><entry><uri>sip:s1</uri><purpose>event</purpose></entry><entry><uri>sip:s2</uri></entry></sidebars-by-ref>`+
		`<sidebars-by-val state="partial"><entry entity="sip:v" state="partial"><users state="partial"><user entity="w"/></users></entry></sidebars-by-val>`,
		"2", Partial)

	if err := local.Apply(update); err != nil {
		t.Fatal(err)
	}

	if d := local.Description; d.Subject != "t" || d.DisplayText != "" {
		t.Errorf("description replaced as a whole: %+v", d)
	}

	u := local.Users.Users[0]
	if u.DisplayText != "U" || len(u.Endpoints) != 1 {
		t.Fatalf("user %+v", u)
	}

	ep := u.Endpoints[0]
	if ep.Status != Connected || len(ep.Media) != 3 || ep.Media[0].Type != "audio" || ep.Media[0].Status != RecvOnly ||
		ep.Media[1].Type != "video" || ep.Media[2].Type != "text" {
		t.Errorf("endpoint %+v", ep)
	}

	if len(ep.Other) != 2 || ep.Other[0].Name.Local != "b" || ep.Other[1].Text() != "2" || len(ep.Media[0].Attr) != 1 ||
		ep.Media[0].Attr[0].Value != "2" {
		t.Errorf("extensions %+v", ep.Other)
	}

	if r := local.SidebarsByRef.Entries; len(r) != 2 || r[0].DisplayText != "one" || r[0].Purpose != Event || r[1].URI != "sip:s2" {
		t.Errorf("sidebars by ref %+v", r)
	}

	if v := local.SidebarsByVal.Entries; len(v) != 1 || len(v[0].Users.Users) != 2 || v[0].State != "" {
		t.Errorf("sidebars by val %+v", v)
	}

	stable(t, local)

	// A deleted document replaces the state: the conference has ended.
	if err := local.Apply(decode("", "3", Deleted)); err != nil || local.State != Deleted || local.Users != nil {
		t.Errorf("deleted: %+v, %v", local, err)
	}
}

// encoded is the document of i, to compare states.
func encoded(t *testing.T, i Info) string {
	t.Helper()

	b, err := Encode(i)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func decodeBody(t *testing.T, version string, state State, body string) Info {
	t.Helper()

	i, err := Decode([]byte(`<conference-info xmlns="urn:ietf:params:xml:ns:conference-info" xmlns:x="urn:x" entity="sip:c@x" ` +
		`state="` + string(state) + `" version="` + version + `">` + body + `</conference-info>`))
	if err != nil && !errors.Is(err, ErrValue) {
		t.Fatal(err)
	}

	return i
}

// Apply changes neither the states nor the notifications it was given: a snapshot of the state taken before is
// unchanged, as is the full notification it stored.
func TestApplyCopies(t *testing.T) {
	full := decodeBody(t, "1", Full, `<users><user entity="a"><endpoint entity="e"><media id="1"><type>audio</type></media></endpoint>`+
		`<x:a>1</x:a></user><user entity="b"/></users><sidebars-by-ref><entry><uri>sip:s</uri></entry></sidebars-by-ref>`)
	fullBefore := encoded(t, full)

	var local Info
	if err := local.Apply(full); err != nil {
		t.Fatal(err)
	}

	snap := local
	snapBefore := encoded(t, snap)

	update := decodeBody(t, "2", Partial, `<users state="partial"><user entity="a" state="deleted"/>`+
		`<user entity="b" state="partial"><endpoint entity="f"/><x:a>2</x:a></user></users>`+
		`<sidebars-by-ref state="partial"><entry><uri>sip:s</uri><display-text>s</display-text></entry></sidebars-by-ref>`)
	updateBefore := encoded(t, update)

	if err := local.Apply(update); err != nil {
		t.Fatal(err)
	}

	if u := local.Users.Users; len(u) != 1 || u[0].Entity != "b" || len(u[0].Endpoints) != 1 || local.SidebarsByRef.Entries[0].DisplayText != "s" {
		t.Errorf("users %+v", u)
	}

	if got := encoded(t, snap); got != snapBefore {
		t.Errorf("snapshot changed:\n%s\n%s", snapBefore, got)
	}

	if got := encoded(t, full); got != fullBefore {
		t.Errorf("full notification changed:\n%s\n%s", fullBefore, got)
	}

	// A partial notification on the state merges into copies of what the earlier one stored.
	again := decodeBody(t, "3", Partial, `<users state="partial"><user entity="b" state="partial"><endpoint entity="f" state="deleted"/>`+
		`</user></users>`)
	if err := local.Apply(again); err != nil || len(local.Users.Users[0].Endpoints) != 0 {
		t.Fatalf("%+v, %v", local, err)
	}

	if got := encoded(t, update); got != updateBefore {
		t.Errorf("partial notification changed:\n%s\n%s", updateBefore, got)
	}
}

// RFC 4575 §4.4, §4.6, §5.2: the notifications Apply refuses, leaving the state unchanged; the children of a deleted
// root ignored.
func TestApplyRefused(t *testing.T) {
	var local Info
	if err := local.Apply(decodeBody(t, "1", Full, `<users><user entity="a"><endpoint entity="e"><status>connected</status></endpoint></user></users>`)); err != nil {
		t.Fatal(err)
	}

	before := encoded(t, local)

	for _, c := range []struct {
		name string
		n    Info
		err  error
	}{
		{"malformed status", decodeBody(t, "2", Partial, `<users state="partial"><user entity="a" state="partial">`+
			`<endpoint entity="e" state="partial"><status>bogus</status></endpoint></user></users>`), ErrIncoherent},
		{"users twice", decodeBody(t, "2", Partial, `<users state="partial"/><users/>`), ErrIncoherent},
		{"user without entity", decodeBody(t, "2", Partial, `<users state="partial"><user/></users>`), ErrIncoherent},
		{"malformed new endpoint", decodeBody(t, "2", Partial, `<users state="partial"><user entity="a" state="partial">`+
			`<endpoint entity="f" state="partial"><media/></endpoint></user></users>`), ErrIncoherent},
		{"gap", decodeBody(t, "3", Partial, ``), ErrVersionGap},
		{"stale", decodeBody(t, "1", Full, ``), ErrStale},
	} {
		if err := local.Apply(c.n); !errors.Is(err, c.err) {
			t.Errorf("%s: %v", c.name, err)
		}

		if got := encoded(t, local); got != before {
			t.Errorf("%s: state changed:\n%s\n%s", c.name, before, got)
		}
	}

	other := decodeBody(t, "2", Full, ``)
	other.Entity = "sip:d@x"

	if err := local.Apply(other); !errors.Is(err, ErrEntity) {
		t.Errorf("another entity: %v", err)
	}

	other.State = Partial
	if err := local.Apply(other); !errors.Is(err, ErrEntity) {
		t.Errorf("another entity: %v", err)
	}

	// An element replaced as a whole keeps what it has, as in a full document; the extensions merged.
	if err := local.Apply(decodeBody(t, "2", Partial, `<conference-description><subject>a</subject><subject>b</subject></conference-description>`+
		`<x:a/>`)); err != nil || len(local.Description.Other) != 1 || len(local.Other) != 1 {
		t.Errorf("%+v, %v", local, err)
	}

	// The children of a deleted root are ignored (§4.4); after it, no partial notification applies, a full one does.
	if err := local.Apply(decodeBody(t, "3", Deleted, `<users><user entity="a"/></users><x:a/>`)); err != nil ||
		local.State != Deleted || local.Users != nil || local.Other != nil || local.Version != 3 {
		t.Errorf("deleted: %+v, %v", local, err)
	}

	if err := local.Apply(decodeBody(t, "4", Partial, `<users state="partial"><user entity="a"/></users>`)); !errors.Is(err, ErrIncoherent) ||
		local.Users != nil {
		t.Errorf("partial after deleted: %+v, %v", local, err)
	}

	if err := local.Apply(decodeBody(t, "5", Full, `<users><user entity="b"/></users>`)); err != nil || local.State != Full ||
		local.Users.Users[0].Entity != "b" {
		t.Errorf("full after deleted: %+v, %v", local, err)
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
		i, err := Decode(b)
		if err != nil && !errors.Is(err, ErrValue) {
			return
		}

		stable(t, i)

		// A document decoded without error encodes to one decoded without error.
		if err == nil {
			out, _ := Encode(i)
			if _, err := Decode(out); err != nil {
				t.Fatalf("Decode(Encode): %v\n%s\n%s", err, b, out)
			}
		}
	})
}
