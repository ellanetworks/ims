//go:build integration && linux

package cluster

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const heartbeatTimeout = time.Second

func (c *cluster) writeOnALeader(r *routes, among []*node, old *node) (string, error) {
	survivors := &cluster{t: c.t, nodes: among}

	st, err := survivors.leaderStatus()
	if err != nil {
		return "", err
	}

	if st.LeaderID == old.id {
		return "", errors.New("the old leader still leads")
	}

	for _, n := range among {
		if n.id == st.LeaderID {
			return r.create(n)
		}
	}

	return "", errors.New("no follower leads")
}

func TestStoppingLeaderHandsOver(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	followers := cl.followers(leader)
	r := newRoutes(t, cl)

	stopping := time.Now()

	_ = leader.cmd.Process.Signal(syscall.SIGTERM)

	var (
		id  string
		err error
	)

	for deadline := stopping.Add(waitTimeout); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if id, err = cl.writeOnALeader(r, followers, leader); err == nil {
			break
		}
	}

	if err != nil {
		t.Fatal(err)
	}

	served := time.Since(stopping)

	select {
	case <-leader.exited:
	case <-time.After(waitTimeout):
		t.Fatal("the old leader did not exit")
	}

	t.Logf("a new leader took a write %s after SIGTERM", served.Round(time.Millisecond))

	if served >= heartbeatTimeout {
		t.Fatalf("a new leader took its first write %s after SIGTERM: an election, not a handover", served)
	}

	log, err := os.ReadFile(filepath.Join(leader.dir, "ims.log"))
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(log, []byte("failed to hand the leadership over")) {
		t.Fatal("the old leader failed to hand the leadership over")
	}

	cl.nodes = followers
	r.everywhere(cl, []string{id})
}
