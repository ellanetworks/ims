//go:build integration && linux

package cluster

import (
	"fmt"
	"net/http"
	"testing"
)

func TestAMemberReturnsOnANewAddress(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	moved := cl.followers(leader)[0]

	before, err := cl.member(moved)
	if err != nil {
		t.Fatal(err)
	}

	moved.stop()

	moved.clusterPort++
	moved.configure()
	moved.start()
	moved.logIn()

	want := moved.clusterAddress()

	eventually(t, moved.name+" known at its new address", func() error {
		m, err := cl.member(moved)
		if err != nil {
			return err
		}

		if m.Address != want {
			return fmt.Errorf("address %s, was %s, want %s", m.Address, before.Address, want)
		}

		return nil
	})

	cl.waitStable()

	cl.createUser(leader, "moved@example.com")

	eventually(t, "the write on the moved node", func() error {
		_, err := moved.request(http.MethodGet, "/api/v1/users/moved@example.com", nil, nil)
		return err
	})
}

func TestRestoreABackupInACluster(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	followers := cl.followers(leader)
	source, target := followers[0], followers[1]

	cl.createUser(leader, "before@example.com")

	eventually(t, "the first write on "+source.name, func() error {
		_, err := source.request(http.MethodGet, "/api/v1/users/before@example.com", nil, nil)
		return err
	})

	backup, err := source.raw(http.MethodGet, "/api/v1/backup", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	cl.createUser(leader, "after@example.com")

	_, err = target.raw(http.MethodPost, "/api/v1/restore", "application/gzip", backup)
	if got := leaderOf(err); got != leader.apiAddress() {
		t.Fatalf("restoring on a follower = %v, want a pointer to the leader %s", err, leader.apiAddress())
	}

	if _, err := leader.raw(http.MethodPost, "/api/v1/restore", "application/gzip", backup); err != nil {
		t.Fatal(err)
	}

	for _, n := range cl.nodes {
		n.logIn()

		eventually(t, n.name+" to hold the backup's data", func() error {
			if _, err := n.request(http.MethodGet, "/api/v1/users/before@example.com", nil, nil); err != nil {
				return err
			}

			if code, _ := n.request(http.MethodGet, "/api/v1/users/after@example.com", nil, nil); code != http.StatusNotFound {
				return fmt.Errorf("a write made after the backup = %d", code)
			}

			return nil
		})
	}

	cl.waitStable()

	for _, n := range cl.nodes {
		if _, err := cl.member(n); err != nil {
			t.Fatal(err)
		}
	}

	cl.createUser(cl.leader(), "restored@example.com")
	cl.everyNodeHasUser("restored@example.com")
}
