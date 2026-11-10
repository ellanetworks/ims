//go:build integration && linux

package cluster

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
)

func TestBackToBackJoins(t *testing.T) {
	for round := range 3 {
		t.Run("round"+strconv.Itoa(round), func(t *testing.T) {
			first := newNode(t, "node0")
			first.initialize()

			cl := &cluster{t: t, nodes: []*node{first, newNode(t, "node1"), newNode(t, "node2")}}

			for _, n := range cl.nodes[1:] {
				token := first.mintToken()

				if _, err := n.redeem(token); err != nil {
					t.Fatalf("%s failed to join: %v", n.name, err)
				}
			}

			for _, n := range cl.nodes[1:] {
				n.logIn()
			}

			for _, n := range cl.nodes {
				n.identify()
			}

			cl.waitStable()

			for _, n := range cl.nodes[1:] {
				if _, err := cl.member(n); err != nil {
					t.Fatal(fmt.Errorf("after back-to-back joins: %w", err))
				}
			}
		})
	}
}

func TestConcurrentJoinsWithSingleUseTokens(t *testing.T) {
	first := newNode(t, "node0")
	first.initialize()
	first.identify()

	cl := &cluster{t: t, nodes: []*node{first, newNode(t, "node1"), newNode(t, "node2")}}
	tokens := []string{first.mintToken(), first.mintToken()}

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for i, n := range cl.nodes[1:] {
		wg.Go(func() {
			_, errs[i] = n.redeem(tokens[i])
		})
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("node%d failed to join: %v", i+1, err)
		}
	}

	for _, n := range cl.nodes[1:] {
		n.logIn()
		n.identify()
	}

	cl.waitStable()

	late := newNode(t, "late")

	for _, token := range tokens {
		code, err := late.redeem(token)
		if err == nil {
			t.Fatal("a join token was redeemed twice")
		}

		if code != http.StatusConflict {
			t.Fatalf("reusing a join token = %d, want %d: %v", code, http.StatusConflict, err)
		}
	}

	cl.waitStable()

	cl.nodes = append(cl.nodes, late)
	cl.join(late)
	late.identify()

	eventually(t, "the late node a member", func() error {
		m, err := cl.member(late)
		if err != nil {
			return err
		}

		if m.Healthy == nil || !*m.Healthy {
			return fmt.Errorf("%s is %s and not healthy", late.name, m.Suffrage)
		}

		return nil
	})
}
