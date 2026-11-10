//go:build integration && linux

package cluster

import (
	"net/http"
	"testing"
)

func TestALoginWorksOnEveryNodeRightAway(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()

	cl.createUser(leader, "ops@example.com")
	cl.everyNodeHasUser("ops@example.com")

	for i := range 6 {
		for _, via := range cl.nodes {
			token, err := via.login("ops@example.com", "correct horse")
			if err != nil {
				t.Fatalf("round %d: logging in on %s: %v", i, via.name, err)
			}

			for _, other := range cl.nodes {
				if _, err := other.requestAs(token, http.MethodGet, "/api/v1/cluster", nil, nil); err != nil {
					t.Fatalf("round %d: a login on %s used on %s: %v", i, via.name, other.name, err)
				}
			}
		}
	}
}

func TestAFollowerReadsItsOwnWrites(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	r := newRoutes(t, cl)

	for _, f := range cl.followers(leader) {
		for range 20 {
			id, err := r.create(f)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := f.request(http.MethodGet, "/api/v1/ip-sm-gw/routes/"+id, nil, nil); err != nil {
				t.Fatalf("%s wrote route %s but does not see it: %v", f.name, id, err)
			}
		}

		email := f.name + "@example.com"
		cl.createUser(f, email)

		if _, err := f.request(http.MethodGet, "/api/v1/users/"+email, nil, nil); err != nil {
			t.Fatalf("%s wrote user %s but does not see it: %v", f.name, email, err)
		}
	}
}
