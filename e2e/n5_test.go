//go:build e2e && linux

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/testue"
)

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

func smfGet(t *testing.T, core *netns, path string) []byte {
	t.Helper()

	status, body := httpGet(t, core, smfMetrics, path, "")
	if status != http.StatusOK {
		t.Fatalf("SMF %s: %d %s", path, status, body)
	}

	return body
}

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

	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(c, "nsenter", append([]string{"-t", strconv.Itoa(pidOf(t, subscribers[i].pidEnv)), "-a"}, args...)...)
	out, err := cmd.CombinedOutput()

	return string(out), err
}

// TS 29.514 clause 4.2.2.2
// TS 29.513 clause 7.2.3
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

// GSMA IR.94 section 2.2.2
// GSMA NG.114 section 4.5.3
// TS 29.513 clause 7.2.3
func TestVideoCallQoSFlows(t *testing.T) {
	needs5G(t)

	core := enter(t, pidOf(t, "E2E_OPEN5GS_PID"))
	a, b := pairWith(t, sip.UDP, testue.Config{MMTel: testue.MMTelConfig{Video: true}})
	target := "sip:" + subscribers[1].msisdn + "@" + homeDomain
	before := qosFlows(t, core)

	t.Run("video call", func(t *testing.T) {
		ac, bc := connect(t, a, b, target, testue.CallOptions{Video: true})

		if _, ok := bc.Stream(sdp.Video); !ok {
			t.Fatalf("callee streams %+v, want the video", bc.Streams())
		}

		waitQoSFlows(t, core, before+4)
		hangUp(t, ac, bc)
		waitQoSFlows(t, core, before)
	})

	t.Run("video added", func(t *testing.T) {
		ac, bc := connect(t, a, b, target, testue.CallOptions{})

		waitQoSFlows(t, core, before+2)

		if err := bc.AddVideo(ctx(t)); err != nil {
			t.Fatalf("AddVideo: %v", err)
		}

		waitQoSFlows(t, core, before+4)
		hangUp(t, ac, bc)
		waitQoSFlows(t, core, before)
	})
}

func hangUp(t *testing.T, ac, bc *testue.Call) {
	t.Helper()

	if err := ac.Bye(ctx(t)); err != nil {
		t.Fatalf("Bye: %v", err)
	}

	ended(t, ac, testue.LocalBye)
	ended(t, bc, testue.RemoteBye)
}

// TS 29.514 clause 4.2.5.3
// TS 24.229 clause 5.2.8.1.2
func TestCallEndsWithThePDUSession(t *testing.T) {
	needs5G(t)
	t.Skip("the Open5GS PCF can crash on the delete that follows terminate (double free of the app session)")

	a, b := pair(t, sip.UDP)

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
