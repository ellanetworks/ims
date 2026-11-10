//go:build integration && linux

package cluster

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

type formation struct {
	State    string `json:"state"`
	Expected int    `json:"expected"`
	Found    int    `json:"found"`
	Detail   string `json:"detail"`
}

type nodeStatus struct {
	NodeID    string    `json:"node_id"`
	Formation formation `json:"formation"`
}

func (n *node) formation() (nodeStatus, error) {
	var st nodeStatus

	_, err := n.requestAs("", http.MethodGet, "/api/v1/status", nil, &st)

	return st, err
}

func newFormingCluster(t *testing.T) *cluster {
	t.Helper()

	const size = 3

	c := &cluster{t: t}

	for i := range size {
		c.nodes = append(c.nodes, newIdleNode(t, "node"+strconv.Itoa(i)))
	}

	var join []string
	for _, n := range c.nodes {
		join = append(join, n.clusterAddress())
	}

	for _, n := range c.nodes {
		n.join, n.expect = join, size
		n.configure()
	}

	return c
}

func (c *cluster) startTogether() {
	c.t.Helper()

	var wg sync.WaitGroup

	for _, n := range c.nodes {
		wg.Go(n.launch)
	}

	wg.Wait()

	for _, n := range c.nodes {
		eventually(c.t, n.name+" to answer", n.live)
	}
}

func (c *cluster) waitFormed() {
	c.t.Helper()

	eventually(c.t, "every node to be a member", func() error {
		for _, n := range c.nodes {
			st, err := n.formation()
			if err != nil {
				return err
			}

			if st.Formation.State != "member" {
				return fmt.Errorf("%s is %s: %s", n.name, st.Formation.State, st.Formation.Detail)
			}

			n.id = st.NodeID
		}

		return nil
	})
}

func (c *cluster) initializeFormed() {
	c.t.Helper()

	eventually(c.t, "the first admin", func() error {
		var login struct {
			Token string `json:"token"`
		}

		if _, err := c.nodes[0].requestAs("", http.MethodPost, "/api/v1/init",
			map[string]string{"email": adminEmail, "password": adminPassword}, &login); err != nil {
			return err
		}

		c.nodes[0].authenticate(login.Token)

		return nil
	})

	for _, n := range c.nodes[1:] {
		n.logIn()
	}

	c.waitStable()

	clusters := map[string]bool{}

	for _, n := range c.nodes {
		st, err := n.status()
		if err != nil {
			c.t.Fatal(err)
		}

		clusters[st.ClusterID] = true

		if len(st.Members) != len(c.nodes) {
			c.t.Fatalf("%s knows %d members", n.name, len(st.Members))
		}
	}

	if len(clusters) != 1 {
		c.t.Fatalf("cluster IDs %v", clusters)
	}

	for _, n := range c.nodes {
		n.waitReady("")
	}
}

func TestIdenticalConfigsStartedTogetherFormOneCluster(t *testing.T) {
	c := newFormingCluster(t)
	c.startTogether()
	c.waitFormed()
	c.initializeFormed()
}

func TestFormingNodeExplainsWhatItWaitsFor(t *testing.T) {
	c := newFormingCluster(t)

	first := c.nodes[0]
	first.start()

	eventually(t, "the forming status", func() error {
		st, err := first.formation()
		if err != nil {
			return err
		}

		if st.Formation.State != "forming" || st.Formation.Expected != 3 || st.Formation.Found != 1 {
			return fmt.Errorf("formation %+v", st.Formation)
		}

		return nil
	})

	if err := first.live(); err != nil {
		t.Fatal(err)
	}

	first.waitFailing("", "member")

	code, err := first.requestAs("", http.MethodPost, "/api/v1/init",
		map[string]string{"email": adminEmail, "password": adminPassword}, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/v1/init while forming = %d, %v", code, err)
	}

	time.Sleep(5 * time.Second)

	for _, n := range c.nodes[1:] {
		n.start()
	}

	c.waitFormed()
	c.initializeFormed()
}

func TestKillDuringFormation(t *testing.T) {
	c := newFormingCluster(t)
	c.startTogether()

	var first *node

	eventually(t, "a bootstrapper", func() error {
		for _, n := range c.nodes {
			if st, err := n.formation(); err == nil && st.Formation.State == "member" {
				first = n
				return nil
			}
		}

		return errors.New("no member yet")
	})

	first.kill()
	first.start()

	c.waitFormed()
	c.initializeFormed()
}

func TestAFourthNodeJoinsAFormedCluster(t *testing.T) {
	c := newFormingCluster(t)
	c.startTogether()
	c.waitFormed()
	c.initializeFormed()

	fourth := newIdleNode(t, "node3")
	fourth.join = []string{c.nodes[0].clusterAddress(), c.nodes[1].clusterAddress(), c.nodes[2].clusterAddress()}
	fourth.configure()
	fourth.start()

	eventually(t, "the fourth node to join", func() error {
		st, err := fourth.formation()
		if err != nil {
			return err
		}

		if st.Formation.State != "member" {
			return fmt.Errorf("formation %+v", st.Formation)
		}

		return nil
	})

	fourth.logIn()
	fourth.waitReady("")
}

func TestRemovedNodeStaysUpFenced(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	gone := cl.followers(leader)[0]

	gone.stop()

	leader.must(http.MethodDelete, "/api/v1/cluster/members/"+gone.id+"?force=true", nil, nil)

	gone.start()

	gone.waitFailing("", "member")

	eventually(t, "the removed state in the status", func() error {
		st, err := gone.formation()
		if err != nil {
			return err
		}

		if st.Formation.State != "removed" {
			return fmt.Errorf("formation %+v", st.Formation)
		}

		return nil
	})

	gone.stop()
	gone.start()

	time.Sleep(3 * time.Second)

	if !gone.running() {
		t.Fatal("the removed node exited")
	}

	if err := gone.live(); err != nil {
		t.Fatal(err)
	}

	gone.waitFailing("", "member")

	st, err := gone.formation()
	if err != nil || st.Formation.State != "removed" {
		t.Fatalf("status of the restarted removed node = %+v, %v", st, err)
	}
}
