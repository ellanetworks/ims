package mobile

import (
	"bytes"
	"encoding/json"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ellanetworks/ims/mcptt/floor"
)

// The JSON of the session, in testdata/contract, the app's tests read too (android/app/src/test): the events as the
// session encodes them, the settings as the app does.
var update = flag.Bool("update", false, "write testdata/contract")

func ptr[T any](v T) *T { return &v }

// contractEvents is an event of each type and kind, in the order of the app's EventTest.
var contractEvents = []event{
	{Type: "registration", Registered: true, IMPU: "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org"},
	{Type: "registration", Error: "mobile: register: timeout"},
	{Type: "deregistered", Reason: "probation", RetryAfter: 30},
	{Type: "deregistered", Reason: "unregistered"},
	{Type: "stopped", Reason: "rejected"},
	{Type: "stopped", Reason: "stop"},
	{Type: "affiliation", Status: 200},
	{Type: "affiliation", Error: "mobile: affiliation: 403"},
	{Type: "call", Call: "in-1", Kind: "incoming", Group: "sip:fire@mcptt.example", Caller: "sip:alice@mcptt.example", Manual: true},
	{Type: "call", Call: "c1", Kind: "established"},
	{Type: "call", Call: "c1", Kind: "ended", Cause: "remote"},
	{Type: "call", Call: "c2", Kind: "ended", Cause: "rejected", Status: 480, Error: "client: INVITE rejected"},
	{Type: "call", Call: "c3", Kind: "ended", Cause: "local"},
	{Type: "call", Call: "in-1", Kind: "ended", Cause: "missed"},
	{Type: "call", Call: "c4", Kind: "ended", Cause: "error", Error: "client: INVITE: timeout"},
	{Type: "floor", Call: "c1", Notice: "taken", Party: "sip:bob@mcptt.example"},
	{Type: "floor", Call: "c1", Notice: "granted", Duration: ptr(30)},
	{Type: "floor", Call: "c1", Notice: "denied"},
	{Type: "floor", Call: "c1", Notice: "idle"},
	{Type: "floor", Call: "c1", Notice: "revoked"},
	{Type: "floor", Call: "c1", Notice: "timeout"},
	{Type: "floor", Call: "c1", Notice: "media-ended"},
	{Type: "transmit", Call: "c1", On: ptr(true), Mode: ptr(8)},
	{Type: "transmit", Call: "c1", On: ptr(false)},
	{Type: "mode", Call: "c1", Mode: ptr(0)},
	{Type: "heard", Call: "c1", Party: "sip:bob@mcptt.example"},
}

func TestContractEvents(t *testing.T) {
	var b bytes.Buffer

	for _, e := range contractEvents {
		e.Version = schemaVersion

		j, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}

		b.Write(j)
		b.WriteByte('\n')
	}

	golden(t, "events.jsonl", b.Bytes())
}

// causes are the ends of a call, as cause's doc and EventFailed's name them.
var causes = []string{"local", "remote", "rejected", "missed", "error"}

// TestContractExhaustive: the contract's events have every floor notice and every cause, so that the app decodes
// each (its EventTest asserts its enums' entries all decoded).
func TestContractExhaustive(t *testing.T) {
	notice, cause := map[string]bool{}, map[string]bool{}

	for _, e := range contractEvents {
		notice[e.Notice], cause[e.Cause] = true, true
	}

	for k := floor.NoticeGranted; k <= floor.NoticeMediaEnded; k++ {
		name, ok := notices[k]
		if !ok {
			t.Errorf("notice %d without a name", k)
		} else if !notice[name] {
			t.Errorf("notice %q not in the contract", name)
		}
	}

	for _, c := range causes {
		if !cause[c] {
			t.Errorf("cause %q not in the contract", c)
		}
	}
}

// TestContractSettings: the app's settings are the session's, every field and none other.
func TestContractSettings(t *testing.T) {
	text, err := os.ReadFile(filepath.Join("testdata", "contract", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	d := json.NewDecoder(bytes.NewReader(text))
	d.DisallowUnknownFields()

	var set settings
	if err := d.Decode(&set); err != nil {
		t.Fatal(err)
	}

	if set.Version != schemaVersion {
		t.Errorf("version %d", set.Version)
	}

	var app, ours map[string]any

	all, _ := json.Marshal(set)
	if json.Unmarshal(text, &app) != nil || json.Unmarshal(all, &ours) != nil {
		t.Fatal("settings")
	}

	if got, want := slices.Sorted(maps.Keys(app)), slices.Sorted(maps.Keys(ours)); !slices.Equal(got, want) {
		t.Errorf("the app's settings %v, want %v", got, want)
	}

	// On this host's address, not the device's.
	set.Local = "127.0.0.1"

	s, err := newSession(newPlatform(), set)
	if err != nil {
		t.Fatalf("session of the app's settings: %v", err)
	}

	s.Stop()
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", "contract", name)

	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("%s:\n%s\nwant (go test -update)\n%s", path, got, want)
	}
}
