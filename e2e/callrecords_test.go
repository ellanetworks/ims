//go:build e2e && linux

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/ellanetworks/ims/testue"
)

const imsAPI = "10.80.0.5:5020"

func httpGet(t *testing.T, core *netns, addr, path, token string) (int, []byte) {
	t.Helper()

	var (
		raw []byte
		err error
	)

	core.Do(func() {
		var c net.Conn

		d := net.Dialer{Timeout: 5 * time.Second}

		if c, err = d.DialContext(t.Context(), "tcp", addr); err != nil {
			return
		}

		defer func() { _ = c.Close() }()

		_ = c.SetDeadline(time.Now().Add(5 * time.Second))

		request := "GET " + path + " HTTP/1.0\r\n"
		if token != "" {
			request += "Authorization: Bearer " + token + "\r\n"
		}

		if _, err = io.WriteString(c, request+"\r\n"); err == nil {
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

func poll[T any](t *testing.T, core *netns, path string, pred func(T) bool) T {
	t.Helper()

	deadline := time.Now().Add(wait)

	for {
		var (
			v    T
			ok   bool
			last []byte
		)

		switch status, body := httpGet(t, core, imsAPI, path, os.Getenv("E2E_IMS_TOKEN")); status {
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

type page[T any] struct {
	Items []T `json:"items"`
}

type callRecord struct {
	SessionID  string `json:"session_id"`
	Outcome    string `json:"outcome"`
	EndedBy    string `json:"ended_by"`
	InProgress bool   `json:"in_progress"`
	DurationMS *int64 `json:"duration_ms"`
}

const callRecordsPath = "/api/v1/call-records?per_page=100"

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
