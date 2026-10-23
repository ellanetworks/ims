//go:build e2e && linux

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The SMF's metrics, in the network namespace of the Open5GS container (open5gs/config/open5gs/smf.yaml).
const smfMetrics = "127.0.0.4:9090"

// smfGet returns the body of the response of the SMF's metrics server to a GET of path.
func smfGet(t *testing.T, core *netns, path string) []byte {
	t.Helper()

	var (
		res []byte
		err error
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

		if _, err = io.WriteString(c, "GET "+path+" HTTP/1.0\r\n\r\n"); err == nil {
			res, err = io.ReadAll(c)
		}
	})

	if err != nil {
		t.Fatalf("SMF %s: %v", path, err)
	}

	_, body, ok := bytes.Cut(res, []byte("\r\n\r\n"))
	if !ok {
		t.Fatalf("SMF %s: no HTTP response in %q", path, res)
	}

	return body
}

// qosFlows returns the number of QoS flows at the SMF. Open5GS labels the gauge with the 5QI of the PDU session, not
// of the flow, so only the total says that flows were added (open5gs src/smf/context.c smf_qos_flow_add), as in
// ims/e2e/n5_test.go.
func qosFlows(t *testing.T, core *netns) int {
	t.Helper()

	body := smfGet(t, core, "/metrics")

	n, found := 0, false

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

// fiveQIs returns the 5QIs of the QoS flows of the PDU sessions of each SUPI at the SMF (open5gs src/smf/pdu-info.c).
func fiveQIs(t *testing.T, core *netns) map[string][]int {
	t.Helper()

	var info struct {
		Items []struct {
			SUPI string `json:"supi"`
			PDU  []struct {
				QoSFlows []struct {
					FiveQI int `json:"5qi"`
				} `json:"qos_flows"`
			} `json:"pdu"`
		} `json:"items"`
	}

	body := smfGet(t, core, "/pdu-info")
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("SMF pdu-info %q: %v", body, err)
	}

	out := map[string][]int{}

	for _, it := range info.Items {
		for _, p := range it.PDU {
			for _, f := range p.QoSFlows {
				out[it.SUPI] = append(out[it.SUPI], f.FiveQI)
			}
		}
	}

	return out
}

func waitQoSFlows(t *testing.T, core *netns, want int) {
	t.Helper()

	deadline := time.Now().Add(wait)

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

// TS 23.379 §5.7.3; TS 29.514 §4.2.2.2, TS 29.513 §7.2.3: each participant of a group call gets a QoS flow for the
// voice and one for the floor control channel, whose application media component the P-CSCF authorises with the
// PCF (open5gs/qos.patch), released with the call. In 4G the call itself shows that the PCRF authorised them: the
// P-CSCF releases a call whose media it refuses.
func TestCallQoSFlows(t *testing.T) {
	if os.Getenv("E2E_RAT") != "5g" {
		t.Skip("QoS flows: run with E2E_RAT=5g")
	}

	pid, err := strconv.Atoi(os.Getenv("E2E_OPEN5GS_PID"))
	if err != nil {
		t.Skip("E2E_OPEN5GS_PID not set: run through e2e/run.sh")
	}

	core := enter(t, pid)
	alice, bob, carol := users(t)
	before := qosFlows(t, core)

	// police releases its calls when its initiator leaves.
	ca, _, _ := call(t, police, alice, bob, carol)

	waitQoSFlows(t, core, before+3*2)

	// TS 23.379 §5.7.3: voice on 65, floor control on 69 (open5gs/qos.patch), the caller's as the members': its
	// P-CSCF has the MCPTT ICSI from the Feature-Caps of the 200 (TS 29.514 Annex B.1 NOTE 3).
	qis := fiveQIs(t, core)
	for i, u := range []*user{alice, bob, carol} {
		if got := qis["imsi-"+subscribers[i].imsi]; !slices.Contains(got, 65) || !slices.Contains(got, 69) {
			t.Errorf("5QIs of %s's QoS flows %v, want 65 and 69 among them", u.name, got)
		}
	}

	ca.Leave()

	for _, u := range []*user{alice, bob, carol} {
		u.expect(t, "ended")
	}

	waitQoSFlows(t, core, before)
}
