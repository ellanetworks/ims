//go:build integration && linux

package cluster

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

const upgradeSettle = 13 * time.Second

type traffic struct {
	routes *routes
	mu     sync.Mutex
	up     []*node
	down   map[*node]bool
	ids    []string
	sent   int
	last   time.Time
	gap    time.Duration
	quit   chan struct{}
	wg     sync.WaitGroup
}

func startTraffic(r *routes, nodes []*node) *traffic {
	tr := &traffic{routes: r, up: nodes, down: map[*node]bool{}, last: time.Now(), quit: make(chan struct{})}

	tr.wg.Go(tr.write)

	return tr
}

func (tr *traffic) pick(i int) *node {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	var live []*node

	for _, n := range tr.up {
		if !tr.down[n] {
			live = append(live, n)
		}
	}

	if len(live) == 0 {
		return nil
	}

	return live[i%len(live)]
}

func (tr *traffic) set(n *node, up bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	tr.down[n] = !up
}

func (tr *traffic) write() {
	var wg sync.WaitGroup

	defer wg.Wait()

	for i := 0; ; i++ {
		select {
		case <-tr.quit:
			return
		case <-time.After(50 * time.Millisecond):
		}

		via := tr.pick(i)
		if via == nil {
			continue
		}

		tr.mu.Lock()
		tr.sent++
		tr.mu.Unlock()

		wg.Go(func() {
			id, err := tr.routes.create(via)
			if err != nil {
				return
			}

			tr.mu.Lock()
			defer tr.mu.Unlock()

			tr.ids = append(tr.ids, id)

			if gap := time.Since(tr.last); gap > tr.gap {
				tr.gap = gap
			}

			tr.last = time.Now()
		})
	}
}

func (tr *traffic) finish() ([]string, int, time.Duration) {
	close(tr.quit)
	tr.wg.Wait()

	tr.mu.Lock()
	defer tr.mu.Unlock()

	return tr.ids, tr.sent, tr.gap
}

func (c *cluster) schemas() map[string]int {
	out := map[string]int{}

	for _, n := range c.nodes {
		if !n.running() {
			continue
		}

		if st, err := n.status(); err == nil {
			out[n.name] = st.Schema
		} else {
			out[n.name] = -1
		}
	}

	return out
}

func (c *cluster) expectSchema(want int) error {
	for name, s := range c.schemas() {
		if s != want {
			return fmt.Errorf("%s runs schema %d, want %d", name, s, want)
		}
	}

	return nil
}

func (c *cluster) holdsSchema(want int, d time.Duration) {
	c.t.Helper()

	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(time.Second) {
		if err := c.expectSchema(want); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *cluster) upgrade(n *node) {
	c.t.Helper()

	n.stop()

	n.bin = upgraded
	n.start()
	n.logIn()
}

func TestRollingUpgradeUnderLoad(t *testing.T) {
	cl := newCluster(t)

	st, err := cl.leader().status()
	if err != nil {
		t.Fatal(err)
	}

	from := st.Schema
	r := newRoutes(t, cl)
	tr := startTraffic(r, cl.nodes)

	time.Sleep(2 * time.Second)

	leader := cl.leader()
	order := append(cl.followers(leader), leader)

	for i, n := range order {
		tr.set(n, false)
		cl.drain(n)

		if i == len(order)-1 {
			n.stop()
			cl.holdsSchema(from, upgradeSettle)

			n.bin = upgraded
			n.start()
			n.logIn()
		} else {
			cl.upgrade(n)
		}

		cl.resume(n)
		cl.waitStable()
		tr.set(n, true)

		if i < len(order)-1 {
			cl.holdsSchema(from, 2*time.Second)
		}
	}

	eventually(t, "the cluster on the next schema", func() error { return cl.expectSchema(from + 1) })

	time.Sleep(2 * time.Second)

	ids, sent, gap := tr.finish()

	t.Logf("%d of %d writes accepted, longest write gap %s", len(ids), sent, gap)

	if len(ids) < sent*3/4 {
		t.Fatalf("only %d of %d writes accepted during the upgrade", len(ids), sent)
	}

	if gap > 15*time.Second {
		t.Fatalf("the cluster took no write for %s", gap)
	}

	r.everywhere(cl, ids)

	old := cl.nodes[0]
	old.stop()
	old.bin = binary
	old.launch()

	select {
	case <-old.exited:
	case <-time.After(waitTimeout):
		t.Fatal("the previous binary started on a database of the next schema")
	}

	old.bin = upgraded
	old.start()
	old.logIn()
	cl.waitStable()
}

func TestSchemaWaitsForADownMemberUntilRemoved(t *testing.T) {
	cl := newCluster(t)

	st, err := cl.leader().status()
	if err != nil {
		t.Fatal(err)
	}

	from := st.Schema
	leader := cl.leader()
	followers := cl.followers(leader)
	behind := followers[1]

	cl.upgrade(followers[0])
	cl.waitStable()

	cl.drain(leader)
	cl.upgrade(leader)
	cl.resume(leader)
	cl.waitStable()

	behind.kill()
	cl.holdsSchema(from, upgradeSettle)

	cl.leader().must(http.MethodDelete, "/api/v1/cluster/members/"+behind.id+"?force=true", nil, nil)

	eventually(t, "the cluster on the next schema", func() error { return cl.expectSchema(from + 1) })

	cl.createUser(cl.leader(), "after@example.com")
}
