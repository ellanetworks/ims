//go:build integration && linux

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type readiness struct {
	Ready  bool    `json:"ready"`
	Checks []check `json:"checks"`
}

func (r readiness) failing() string {
	var out []string

	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c.Name+": "+c.Detail)
		}
	}

	return strings.Join(out, "; ")
}

func (r readiness) check(name string) (check, bool) {
	for _, c := range r.Checks {
		if c.Name == name {
			return c, true
		}
	}

	return check{}, false
}

func (n *node) readyz(query string) (int, readiness, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.url("/api/v1/readyz"+query), nil)
	if err != nil {
		return 0, readiness{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, readiness{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Result readiness `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return resp.StatusCode, readiness{}, err
	}

	return resp.StatusCode, body.Result, nil
}

func (n *node) waitReady(query string) {
	n.t.Helper()

	eventually(n.t, n.name+" ready", func() error {
		code, r, err := n.readyz(query)

		switch {
		case err != nil:
			return err
		case code != http.StatusOK || !r.Ready:
			return fmt.Errorf("readyz = %d: %s", code, r.failing())
		}

		return nil
	})
}

func (n *node) waitFailing(query, name string) {
	n.t.Helper()

	n.waitFailingWithin(waitTimeout, query, name)
}

func (n *node) waitFailingWithin(timeout time.Duration, query, name string) {
	n.t.Helper()

	within(n.t, timeout, n.name+" failing "+name, func() error {
		code, r, err := n.readyz(query)
		if err != nil {
			return err
		}

		c, ok := r.check(name)

		switch {
		case !ok:
			return fmt.Errorf("no %s check in %+v", name, r.Checks)
		case code != http.StatusServiceUnavailable || r.Ready || c.OK:
			return fmt.Errorf("readyz = %d, %s = %+v", code, name, c)
		}

		return nil
	})
}

func newStandaloneNode(t *testing.T, name string) *node {
	t.Helper()

	n := newIdleNode(t, name)
	n.standalone = true
	n.configure()
	n.start()

	return n
}

func TestReadinessOfASingleNode(t *testing.T) {
	n := newStandaloneNode(t, "single")
	n.waitReady("")

	_, r, err := n.readyz("?cluster=true")
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, c := range r.Checks {
		names = append(names, c.Name)
	}

	want := "shutdown,member,initialized,diameter_label,drain,consistent,database,leader,caught_up,cluster"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("checks = %s, want %s", got, want)
	}

	if !r.Ready {
		t.Fatalf("not ready with cluster=true: %s", r.failing())
	}
}

func TestClusteredNodeIsUnreadyUntilInitialized(t *testing.T) {
	n := newNode(t, "fresh")

	n.waitFailing("", "initialized")

	if err := n.live(); err != nil {
		t.Fatal(err)
	}

	n.initialize()
	n.waitReady("")

	joiner := newNode(t, "joiner")
	joiner.waitFailing("", "initialized")

	if _, err := joiner.redeem(n.mintToken()); err != nil {
		t.Fatal(err)
	}

	joiner.waitReady("")
}

func TestReadinessFollowsTheDrain(t *testing.T) {
	cl := newCluster(t)
	n := cl.followers(cl.leader())[0]

	n.waitReady("")

	cl.drain(n)
	n.waitFailing("", "drain")

	cl.resume(n)
	n.waitReady("")
}

func TestSurvivorOfAQuorumLossIsUnreadyButLive(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	followers := cl.followers(leader)

	for _, n := range cl.nodes {
		n.waitReady("")
	}

	for _, n := range followers {
		n.stop()
	}

	leader.waitFailing("", "leader")

	if err := leader.live(); err != nil {
		t.Fatal(err)
	}

	for _, n := range followers {
		n.start()
	}

	for _, n := range cl.nodes {
		n.waitReady("")
	}
}

func TestFrozenLeaderLeavesTheOthersReady(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	followers := cl.followers(leader)

	leader.freeze()

	if _, _, err := leader.readyz(""); err == nil {
		t.Fatal("a frozen node answered readyz")
	}

	for _, n := range followers {
		n.waitReady("")
	}

	leader.thaw()
	leader.waitReady("")
}

func TestRestartedNodeIsUnreadyUntilCaughtUp(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	n := cl.followers(leader)[0]

	n.stop()

	for i := range 50 {
		leader.must(http.MethodPut, "/api/v1/cluster/members/"+leader.id,
			map[string]string{"display_name": fmt.Sprintf("leader-%d", i)}, nil)
	}

	n.launch()

	var (
		first    string
		answered bool
	)

	for deadline := time.Now().Add(waitTimeout); ; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s not ready after its restart", n.name)
		}

		code, r, err := n.readyz("")
		if err != nil {
			continue
		}

		if !answered {
			answered, first = true, r.failing()

			if code == http.StatusOK {
				t.Fatal("the restarted node was ready from its first answer")
			}
		}

		if code == http.StatusOK {
			break
		}
	}

	t.Logf("first answer after the restart: %s", first)
}

func TestClusterCheckWithAVoterDown(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	down := cl.followers(leader)[0]
	up := cl.followers(leader)[1]

	leader.waitReady("?cluster=true")
	up.waitFailing("?cluster=true", "cluster")
	up.waitReady("")

	down.stop()

	leader.waitFailing("?cluster=true", "cluster")
	up.waitFailing("?cluster=true", "cluster")

	leader.waitReady("")
	up.waitReady("")

	down.start()

	leader.waitReady("?cluster=true")
}
