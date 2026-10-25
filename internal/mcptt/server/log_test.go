package server_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/client"
)

// logs is a log written to from the strands of the agent.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(b)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

// records are the records of message msg.
func (l *logs) records(t *testing.T, msg string) []map[string]any {
	t.Helper()

	var out []map[string]any

	for line := range strings.Lines(l.String()) {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%v: %s", err, line)
		}

		if r["msg"] == msg {
			out = append(out, r)
		}
	}

	return out
}

// The records of a call carry its group, its MCPTT session identity and the Call-ID of the originator's INVITE, the
// records of a member its MCPTT ID; the access token of a service authorisation is never logged.
func TestCallLogs(t *testing.T) {
	var token string

	w := newWorld(t, func(name string, c *client.Config) {
		if name == "alice" {
			c.Token = client.UnsignedToken(c.MCPTTID.String(), c.ClientID, time.Now().Add(time.Hour))
			token = c.Token
		}
	})

	ca, _, _ := w.call(police)

	ca.Leave()
	w.n.Quiesce()
	w.users["alice"].expect(t, "ended")

	alice := w.users["alice"].id

	auth := w.logs.records(t, "service authorised")
	if len(auth) != 4 || auth[0]["mcptt_id"] != alice || auth[0]["impu"] != "sip:alice@ims.example.com" || auth[0]["client_id"] == "" {
		t.Errorf("service authorised: %v", auth)
	}

	started := w.logs.records(t, "group call started")
	if len(started) != 1 {
		t.Fatalf("group call started: %v", started)
	}

	call := started[0]
	session, _ := call["session"].(string)

	if call["level"] != "INFO" || call["group"] != police || !strings.Contains(session, ";gr=") || call["call_id"] == "" ||
		call["originator"] != alice || call["participants"].(float64) < 2 || call["invited"] != 2.0 {
		t.Errorf("group call started: %v", call)
	}

	released := w.logs.records(t, "group call released")
	if len(released) != 1 || released[0]["session"] != session || released[0]["call_id"] != call["call_id"] ||
		released[0]["cause"] != "initiator left" {
		t.Errorf("group call released: %v", released)
	}

	var invite bool

	for _, r := range w.logs.records(t, "SIP transaction") {
		if r["method"] == "INVITE" && r["call_id"] == call["call_id"] && r["status"] == 200.0 && r["level"] == "INFO" {
			invite = true
		}
	}

	if !invite {
		t.Error("no SIP transaction of the INVITE of alice")
	}

	joined := w.logs.records(t, "member joined")
	if len(joined) != 2 {
		t.Fatalf("member joined: %v", joined)
	}

	for _, r := range joined {
		m, _ := r["member"].(map[string]any)
		if r["session"] != session || m == nil || m["mcptt_id"] == alice || m["call_id"] == "" || m["call_id"] == call["call_id"] {
			t.Errorf("member joined: %v", r)
		}
	}

	if token == "" || strings.Contains(w.logs.String(), token) {
		t.Error("the access token is logged")
	}
}
