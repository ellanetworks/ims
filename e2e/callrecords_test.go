//go:build e2e && linux

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ellanetworks/ims/testue"
)

// imsAPI is the API of the IMS, on the core network (compose.yaml, ims/ims-*.yaml).
const imsAPI = "10.80.0.5:5020"

// httpGet GETs path of the HTTP server at addr from the namespace of the Open5GS container, on the core network: the
// status code and the body. The test binary runs with no network of its own (run.sh).
func httpGet(t *testing.T, core *netns, addr, path string) (int, []byte) {
	t.Helper()

	var (
		raw []byte
		err error
	)

	// The dial happens on the thread in the namespace: no http.Client, which dials on goroutines of its own.
	core.Do(func() {
		var c net.Conn

		d := net.Dialer{Timeout: 5 * time.Second}

		if c, err = d.DialContext(t.Context(), "tcp", addr); err != nil {
			return
		}

		defer func() { _ = c.Close() }()

		_ = c.SetDeadline(time.Now().Add(5 * time.Second))

		if _, err = io.WriteString(c, "GET "+path+" HTTP/1.0\r\n\r\n"); err == nil {
			raw, err = io.ReadAll(c)
		}
	})

	if err != nil {
		t.Fatalf("GET %s%s: %v", addr, path, err)
	}

	res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		t.Fatalf("GET %s%s: no HTTP response in %q: %v", addr, path, raw, err)
	}

	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("GET %s%s: %v", addr, path, err)
	}

	return res.StatusCode, body
}

// apiResult is the result of the response body of a GET of path of the IMS's API (openapi.yaml).
func apiResult[T any](t *testing.T, path string, body []byte) T {
	t.Helper()

	var resp struct {
		Result T `json:"result"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("GET %s: %v\n%s", path, err, body)
	}

	return resp.Result
}

// poll GETs path of the IMS's API every 200 ms until pred holds of the result, and returns it; for at most wait, a
// 503 retried: the MCPTT AS not answering, restarting or its calls busy. The sip: IDs of path's query are escaped by
// the caller (url.QueryEscape).
func poll[T any](t *testing.T, core *netns, path string, pred func(T) bool) T {
	t.Helper()

	deadline := time.Now().Add(wait)

	for {
		var (
			v    T
			ok   bool
			last []byte
		)

		switch status, body := httpGet(t, core, imsAPI, path); status {
		case http.StatusOK:
			v = apiResult[T](t, path, body)
			ok = pred(v)
			last, _ = json.Marshal(v)
		case http.StatusServiceUnavailable:
			last = body
		default:
			t.Fatalf("GET %s: %d %s", path, status, body)
		}

		if ok {
			return v
		}

		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %s after %v", path, last, wait)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// page is a page of a list of the API, the fields the tests read.
type page[T any] struct {
	Items []T `json:"items"`
}

// callRecord is a call record of the API (openapi.yaml), the fields the tests read.
type callRecord struct {
	SessionID  string `json:"session_id"`
	Outcome    string `json:"outcome"`
	EndedBy    string `json:"ended_by"`
	InProgress bool   `json:"in_progress"`
	DurationMS *int64 `json:"duration_ms"`
}

// callRecordsPath lists the call records.
const callRecordsPath = "/api/v1/call-records?per_page=100"

// wantCallRecord waits for the one record of the call c made to end, and checks that it was answered and ended by
// endedBy.
func wantCallRecord(t *testing.T, core *netns, c *testue.Call, endedBy string) {
	t.Helper()

	callID := c.Invite().Header.CallID()

	var found []callRecord

	poll(t, core, callRecordsPath, func(p page[callRecord]) bool {
		found = found[:0]

		for _, r := range p.Items {
			if r.SessionID == callID {
				found = append(found, r)
			}
		}

		return len(found) == 1 && !found[0].InProgress
	})

	if r := found[0]; r.Outcome != "answered" || r.EndedBy != endedBy || r.DurationMS == nil {
		t.Fatalf("call record %+v, want answered, ended by the %s", r, endedBy)
	}
}
