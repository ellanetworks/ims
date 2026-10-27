//go:build sqlite_trace

package scscf

import (
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

// TestTraceRegistrationStatements lists the SQL statements one registration, a reg-event SUBSCRIBE, a
// re-registration and a deregistration run, by the method of internal/db that ran them. Investigation only:
// go test -tags sqlite_trace -run TestTraceRegistrationStatements -v ./internal/scscf
func TestTraceRegistrationStatements(t *testing.T) {
	probe, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	drv := probe.Driver().(*sqlite3.SQLiteDriver)
	_ = probe.Close()

	var (
		mu    sync.Mutex
		lines []string
		phase string
	)

	drv.ConnectHook = func(c *sqlite3.SQLiteConn) error {
		return c.SetTrace(&sqlite3.TraceConfig{
			EventMask: sqlite3.TraceStmt,
			Callback: func(info sqlite3.TraceInfo) int {
				pcs := make([]uintptr, 256)
				n := runtime.Callers(2, pcs)
				frames := runtime.CallersFrames(pcs[:n])

				var method, caller string

				for {
					f, more := frames.Next()
					if i := strings.Index(f.Function, "internal/db."); i >= 0 && method == "" &&
						strings.Contains(f.Function, "(*DB).") {
						method = f.Function[i+len("internal/db."):]
					} else if method != "" && caller == "" && strings.Contains(f.Function, "internal/") {
						caller = f.Function[strings.LastIndex(f.Function, "/")+1:]
					}

					if !more {
						break
					}
				}

				s := strings.Join(strings.Fields(info.StmtOrTrigger), " ")
				if len(s) > 110 {
					s = s[:110]
				}

				mu.Lock()
				lines = append(lines, fmt.Sprintf("[%s] %-38s %-40s %s", phase, method, caller, s))
				mu.Unlock()

				return 0
			},
		})
	}

	t.Cleanup(func() { drv.ConnectHook = nil })

	setPhase := func(p string) {
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		phase = p
		mu.Unlock()
	}

	h := newHarness(t)
	u := h.newUE()

	setPhase("register")
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	setPhase("subscribe")
	h.ueSubscriber(u).subscribed()

	setPhase("reregister")
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)

	setPhase("end")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	for _, l := range lines {
		if !strings.HasPrefix(l, "[]") && !strings.HasPrefix(l, "[end]") {
			t.Log(l)
		}
	}
}
