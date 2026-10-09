//go:build e2e && linux

package e2e

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/testue"
)

// The SMF's metrics, in the network namespace of the Open5GS container (open5gs/config/open5gs/smf.yaml).
const smfMetrics = "127.0.0.4:9090"

func needs5G(t *testing.T) {
	t.Helper()

	if os.Getenv("E2E_RAT") != "5g" {
		t.Skip("N5: run with E2E_RAT=5g")
	}
}

func pidOf(t *testing.T, env string) int {
	t.Helper()

	pid, err := strconv.Atoi(os.Getenv(env))
	if err != nil {
		t.Skipf("%s not set: run through e2e/run.sh", env)
	}

	return pid
}

// qosFlows returns the number of QoS flows at the SMF. Open5GS labels the gauge with the 5QI of the PDU session,
// not of the flow, so only the total says that a flow was added (open5gs src/smf/context.c smf_qos_flow_add).
func qosFlows(t *testing.T, core *netns) int {
	t.Helper()

	var (
		body []byte
		err  error
	)

	// The dial happens on the thread in the namespace: no http.Client, which dials on goroutines of its own.
	core.Do(func() {
		var c net.Conn

		d := net.Dialer{Timeout: 5 * time.Second}

		if c, err = d.DialContext(t.Context(), "tcp", smfMetrics); err != nil {
			return
		}

		defer func() { _ = c.Close() }()

		_ = c.SetDeadline(time.Now().Add(5 * time.Second))

		if _, err = io.WriteString(c, "GET /metrics HTTP/1.0\r\n\r\n"); err == nil {
			body, err = io.ReadAll(c)
		}
	})

	if err != nil {
		t.Fatalf("SMF metrics: %v", err)
	}

	n := 0
	found := false

	for sc := bufio.NewScanner(strings.NewReader(string(body))); sc.Scan(); {
		line := sc.Text()
		if !strings.HasPrefix(line, "fivegs_smffunction_sm_qos_flow_nbr{") {
			continue
		}

		v, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
		if err != nil {
			t.Fatalf("SMF metric %q: %v", line, err)
		}

		n += int(v)
		found = true
	}

	if !found {
		t.Fatalf("no fivegs_smffunction_sm_qos_flow_nbr in the SMF metrics:\n%s", body)
	}

	return n
}

func waitQoSFlows(t *testing.T, core *netns, want int) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)

	for {
		n := qosFlows(t, core)
		if n == want {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("%d QoS flows at the SMF, want %d", n, want)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// inUE runs a command of the UE container, in its namespaces.
func inUE(t *testing.T, i int, args ...string) string {
	t.Helper()

	out, err := tryInUE(t, i, args...)
	if err != nil {
		t.Fatalf("%s: %v: %s", strings.Join(args, " "), err, out)
	}

	return out
}

func tryInUE(t *testing.T, i int, args ...string) (string, error) {
	t.Helper()

	// Not t.Context(): cleanups run after it is done.
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(c, "nsenter", append([]string{"-t", strconv.Itoa(pidOf(t, subscribers[i].pidEnv)), "-a"}, args...)...)
	out, err := cmd.CombinedOutput()

	return string(out), err
}

// TS 29.514 §4.2.2.2, TS 29.513 §7.2.3: the call's audio gets a QoS flow on each side, released with the call. The
// Open5GS PCF gives AUDIO the 5QI 1 (src/pcf/npcf-handler.c), the PCC rule e2e/provision.js provisions.
func TestCallQoSFlow(t *testing.T) {
	needs5G(t)

	core := enter(t, pidOf(t, "E2E_OPEN5GS_PID"))
	a, b := pair(t, sip.UDP)
	before := qosFlows(t, core)

	ac, bc := connect(t, a, b, "sip:"+subscribers[1].msisdn+"@"+homeDomain, testue.CallOptions{})

	waitQoSFlows(t, core, before+2)

	if err := ac.Bye(ctx(t)); err != nil {
		t.Fatalf("Bye: %v", err)
	}

	ended(t, ac, testue.LocalBye)
	ended(t, bc, testue.RemoteBye)

	waitQoSFlows(t, core, before)
}

// TS 29.514 §4.2.5.3, TS 24.229 §5.2.8.1.2: the release of the callee's PDU session terminates its application
// session context, and the P-CSCF releases the call toward the caller.
//
// Skipped: the Open5GS PCF can abort on this delete (open5gs 4107085, src/pcf). Its SM policy delete sends terminate,
// but frees the session only after the BSF deregistration; a delete in between defers freeing the app session until
// the SMF answers its notification (client_delete_notify_cb, src/pcf/sbi-path.c), and the session removal frees it
// first: a double free.
func TestCallEndsWithThePDUSession(t *testing.T) {
	needs5G(t)
	t.Skip("the Open5GS PCF can crash on the delete that follows terminate (double free of the app session)")

	a, b := pair(t, sip.UDP)

	// UERANSIM establishes the session again by itself, with another address: the route to the IMS goes with
	// the old one.
	t.Cleanup(func() {
		tun := os.Getenv("E2E_TUN")

		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
			if out, err := tryInUE(t, 1, "ip", "-4", "-o", "addr", "show", "dev", tun); err == nil && strings.Contains(out, "inet ") {
				break
			}

			if time.Now().After(deadline) {
				t.Errorf("UE 2: no PDU session again on %s", tun)
				return
			}
		}

		inUE(t, 1, "ip", "route", "replace", "10.80.0.0/24", "dev", tun)
	})

	ac, _ := connect(t, a, b, "sip:"+subscribers[1].msisdn+"@"+homeDomain, testue.CallOptions{})

	t.Log(inUE(t, 1, "nr-cli", "imsi-"+subscribers[1].imsi, "-e", "ps-release 1"))

	ended(t, ac, testue.RemoteBye)
}
