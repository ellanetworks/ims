//go:build integration && linux

package cluster

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type routes struct {
	t    *testing.T
	peer string
	next atomic.Int64
}

func newRoutes(t *testing.T, c *cluster) *routes {
	t.Helper()

	var peer struct {
		ID string `json:"id"`
	}

	c.leader().must(http.MethodPost, "/api/v1/diameter/peers", map[string]any{
		"host": "smsc.example.org", "address": "192.0.2.10", "applications": []string{"sgd"},
	}, &peer)

	for _, n := range c.nodes {
		eventually(t, n.name+" to know the SMSC peer", func() error {
			_, err := n.request(http.MethodGet, "/api/v1/diameter/peers/"+peer.ID, nil, nil)
			return err
		})
	}

	return &routes{t: t, peer: peer.ID}
}

func (r *routes) create(via *node) (string, error) {
	var route struct {
		ID string `json:"id"`
	}

	centre := fmt.Sprintf("1555%07d", r.next.Add(1))

	if _, err := via.request(http.MethodPost, "/api/v1/ip-sm-gw/routes",
		map[string]any{"service_centre": centre, "peers": []string{r.peer}}, &route); err != nil {
		return "", err
	}

	return route.ID, nil
}

func (r *routes) holds(n *node, ids []string) error {
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}

	if _, err := n.request(http.MethodGet, "/api/v1/ip-sm-gw/routes", nil, &list); err != nil {
		return err
	}

	have := map[string]bool{}
	for _, it := range list.Items {
		have[it.ID] = true
	}

	missing := 0

	for _, id := range ids {
		if !have[id] {
			missing++
		}
	}

	if missing > 0 {
		return fmt.Errorf("%s misses %d of %d accepted routes", n.name, missing, len(ids))
	}

	return nil
}

func (r *routes) everywhere(c *cluster, ids []string) {
	r.t.Helper()

	for _, n := range c.nodes {
		if !n.running() {
			continue
		}

		eventually(r.t, n.name+" to hold every accepted route", func() error { return r.holds(n, ids) })
	}
}

func writeLoad(r *routes, nodes []*node, count int, every time.Duration) []string {
	var (
		mu  sync.Mutex
		ids []string
		wg  sync.WaitGroup
	)

	for i := range count {
		via := nodes[i%len(nodes)]

		wg.Go(func() {
			if id, err := r.create(via); err == nil {
				mu.Lock()

				ids = append(ids, id)

				mu.Unlock()
			}
		})

		time.Sleep(every)
	}

	wg.Wait()

	return ids
}

func TestLeaderCrashLosesNoAcceptedWrite(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	r := newRoutes(t, cl)

	done := make(chan []string)

	go func() {
		done <- writeLoad(r, cl.nodes, 300, 20*time.Millisecond)
	}()

	time.Sleep(2 * time.Second)
	leader.kill()

	ids := <-done

	if len(ids) < 150 {
		t.Fatalf("only %d of 300 writes accepted around the crash", len(ids))
	}

	cl.leader()
	r.everywhere(cl, ids)

	leader.start()
	leader.logIn()
	cl.waitStable()
	r.everywhere(cl, ids)
}

func TestFrozenLeaderLosesNoAcceptedWrite(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	followers := cl.followers(leader)
	r := newRoutes(t, cl)

	var ids []string

	for range 10 {
		id, err := r.create(followers[0])
		if err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	leader.freeze()

	survivors := &cluster{t: t, nodes: followers}
	if survivors.leader() == leader {
		t.Fatal("the frozen node is still the leader")
	}

	for range 10 {
		var id string

		eventually(t, "a write while the leader is frozen", func() error {
			var err error

			id, err = r.create(followers[1])

			return err
		})

		ids = append(ids, id)
	}

	leader.thaw()

	eventually(t, "the thawed node to follow", func() error {
		st, err := leader.status()
		if err != nil {
			return err
		}

		if st.LeaderID == "" || st.LeaderID == leader.id {
			return errors.New("it still claims the leadership")
		}

		return nil
	})

	cl.waitStable()
	r.everywhere(cl, ids)
}

func TestRemoveAndRejoin(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	gone := cl.followers(leader)[0]
	other := cl.followers(leader)[1]

	if code, _ := leader.request(http.MethodDelete, "/api/v1/cluster/members/"+gone.id, nil, nil); code != http.StatusConflict {
		t.Fatalf("removing an active member = %d, want %d", code, http.StatusConflict)
	}

	cl.drain(gone)

	code, err := other.request(http.MethodDelete, "/api/v1/cluster/members/"+gone.id, nil, nil)
	if code != http.StatusMisdirectedRequest {
		t.Fatalf("removing a member on a follower = %d, want %d: %v", code, http.StatusMisdirectedRequest, err)
	}

	if got := leaderOf(err); got != leader.apiAddress() {
		t.Fatalf("a follower points at %q, want the leader %q", got, leader.apiAddress())
	}

	leader.must(http.MethodDelete, "/api/v1/cluster/members/"+gone.id, nil, nil)

	eventually(t, "the removed node to fail readiness", func() error {
		if code, _, _ := gone.readyz(""); code != http.StatusServiceUnavailable {
			return fmt.Errorf("readyz = %d", code)
		}

		return nil
	})

	gone.kill()

	replacement := newNode(t, "replacement")
	cl.nodes = append(cl.followers(nil), replacement)
	cl.join(replacement)
	replacement.identify()

	cl.waitStable()

	cl.createUser(leader, "ops@example.com")

	eventually(t, "the user on the replacement", func() error {
		_, err := replacement.request(http.MethodGet, "/api/v1/users/ops@example.com", nil, nil)
		return err
	})
}
