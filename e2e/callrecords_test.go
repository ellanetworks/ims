//go:build e2e && linux

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/testue"
)

// imsAPI is the API of the IMS, on the core network (compose.yaml, ims/ims-*.yaml).
const imsAPI = "10.80.0.5:5020"

// callRecords lists the call records, from the namespace of the Open5GS container, on the core network.
func callRecords(t *testing.T, core *netns) []api.CallRecordResponse {
	t.Helper()

	var (
		raw []byte
		err error
	)

	// The dial happens on the thread in the namespace: no http.Client, which dials on goroutines of its own.
	core.Do(func() {
		var c net.Conn

		d := net.Dialer{Timeout: 5 * time.Second}

		if c, err = d.DialContext(t.Context(), "tcp", imsAPI); err != nil {
			return
		}

		defer func() { _ = c.Close() }()

		_ = c.SetDeadline(time.Now().Add(5 * time.Second))

		if _, err = io.WriteString(c, "GET /api/v1/call-records?per_page=100 HTTP/1.0\r\n\r\n"); err == nil {
			raw, err = io.ReadAll(c)
		}
	})

	if err != nil {
		t.Fatalf("call records: %v", err)
	}

	_, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		t.Fatalf("call records: not an HTTP response:\n%s", raw)
	}

	var resp struct {
		Result api.ListCallRecordsResponse `json:"result"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("call records: %v\n%s", err, body)
	}

	return resp.Result.Items
}

// wantCallRecord waits for the one record of the call c made to end, and checks that it was answered and ended by
// endedBy.
func wantCallRecord(t *testing.T, core *netns, c *testue.Call, endedBy string) {
	t.Helper()

	callID := c.Invite().Header.CallID()
	deadline := time.Now().Add(15 * time.Second)

	for {
		var found []api.CallRecordResponse

		for _, r := range callRecords(t, core) {
			if r.SessionID == callID {
				found = append(found, r)
			}
		}

		if len(found) == 1 && !found[0].InProgress {
			if r := found[0]; r.Outcome != "answered" || r.EndedBy != endedBy || r.DurationMS == nil {
				t.Fatalf("call record %+v, want answered, ended by the %s", r, endedBy)
			}

			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("call records of %s: %+v, want one that ended", callID, found)
		}

		time.Sleep(200 * time.Millisecond)
	}
}
