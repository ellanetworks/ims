package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/settings"
	"golang.org/x/crypto/bcrypt"
)

const clusterWait = time.Minute

type clusterNode struct {
	srv     *Server
	cfg     testIMS
	addr    string
	stopped bool
}

func clusterConfig(t *testing.T, label string) testIMS {
	t.Helper()

	cfg := testConfig(t)
	cfg.Diameter.HostLabel = label
	cfg.Cluster = &config.Cluster{Address: loopback, Port: unusedPort(t)}

	return cfg
}

func joinerConfig(t *testing.T, label string) testIMS {
	t.Helper()

	cfg := clusterConfig(t, label)
	cfg.Peers = nil

	return cfg
}

func startNode(t *testing.T, cfg testIMS) *clusterNode {
	t.Helper()

	cfg.seed(t)

	srv := &Server{
		Config: cfg.Config, Logger: slog.New(slog.DiscardHandler), BcryptCost: bcrypt.MinCost, IPsec: ipsectest.NewKernel(),
		MCPTTOrphanAfter: cfg.MCPTTOrphanAfter,
	}

	started := make(chan error, 1)

	go func() { started <- srv.Start(context.Background()) }()

	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return")
	}

	n := &clusterNode{srv: srv, cfg: cfg, addr: srv.APIAddr().String()}

	t.Cleanup(n.stop)

	return n
}

func (n *clusterNode) waitStarted(t *testing.T) {
	t.Helper()

	eventuallyFor(t, "SIP, Diameter and the API to start", func() bool {
		n.srv.mu.Lock()
		defer n.srv.mu.Unlock()

		return n.srv.running
	})
}

func (n *clusterNode) stop() {
	if n.stopped {
		return
	}

	n.stopped = true

	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()

	n.srv.Shutdown(ctx)
	apitest.CloseIdleConnections(n.addr)
}

func (n *clusterNode) restart(t *testing.T) *clusterNode {
	t.Helper()

	n.stop()

	return startNode(t, n.cfg)
}

func (n *clusterNode) do(t *testing.T, client *http.Client, token, method, path string, body, out any) int {
	t.Helper()

	var r io.Reader

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}

		r = bytes.NewReader(b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, "http://"+n.addr+path, r)
	if err != nil {
		t.Fatal(err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Logf("%s %s: %v", method, path, err)
		return 0
	}

	defer func() { _ = resp.Body.Close() }()

	if out != nil {
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&envelope); err == nil && len(envelope.Result) > 0 {
			_ = json.Unmarshal(envelope.Result, out)
		}
	}

	return resp.StatusCode
}

func (n *clusterNode) api(t *testing.T, method, path string, body, out any) int {
	t.Helper()

	return n.do(t, apitest.Client(n.addr), "", method, path, body, out)
}

func (n *clusterNode) apiError(t *testing.T, method, path string) (int, api.ErrorResponse) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, "http://"+n.addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := apitest.Client(n.addr).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	var res api.ErrorResponse

	_ = json.NewDecoder(resp.Body).Decode(&res)

	return resp.StatusCode, res
}

func (n *clusterNode) public(t *testing.T, method, path string, body, out any) int {
	t.Helper()

	return n.do(t, http.DefaultClient, "", method, path, body, out)
}

func (n *clusterNode) as(t *testing.T, token, method, path string, body, out any) int {
	t.Helper()

	return n.do(t, http.DefaultClient, token, method, path, body, out)
}

func (n *clusterNode) login(t *testing.T, email string) (string, int) {
	t.Helper()

	var res api.LoginResponse

	code := n.public(t, http.MethodPost, "/api/v1/auth/login", api.LoginParams{Email: email, Password: apitest.Password}, &res)

	return res.Token, code
}

func (n *clusterNode) readyz(t *testing.T) (int, api.Readiness) {
	t.Helper()

	var res api.Readiness

	code := n.public(t, http.MethodGet, "/api/v1/readyz", nil, &res)

	return code, res
}

func (n *clusterNode) check(t *testing.T, name string) (int, cluster.Check, bool) {
	t.Helper()

	code, res := n.readyz(t)

	i := slices.IndexFunc(res.Checks, func(c cluster.Check) bool { return c.Name == name })
	if i < 0 {
		return code, cluster.Check{}, false
	}

	return code, res.Checks[i], true
}

func (n *clusterNode) member(t *testing.T, id string) (cluster.MemberStatus, bool) {
	t.Helper()

	var st cluster.Status

	if n.api(t, http.MethodGet, "/api/v1/cluster", nil, &st) != http.StatusOK {
		return cluster.MemberStatus{}, false
	}

	i := slices.IndexFunc(st.Members, func(m cluster.MemberStatus) bool { return m.NodeID == id })
	if i < 0 {
		return cluster.MemberStatus{}, false
	}

	return st.Members[i], true
}

func (n *clusterNode) mintToken(t *testing.T) string {
	t.Helper()

	var token cluster.Token
	if code := n.api(t, http.MethodPost, "/api/v1/cluster/tokens", api.CreateJoinTokenParams{}, &token); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/cluster/tokens = %d", code)
	}

	return token.Token
}

func (n *clusterNode) join(t *testing.T, token string) int {
	t.Helper()

	return n.public(t, http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token}, nil)
}

func (n *clusterNode) diameterListener(t *testing.T) string {
	t.Helper()

	c := n.srv.core.Load()
	if c == nil || len(c.diameterLns) == 0 {
		return ""
	}

	return c.diameterLns[0].Addr().String()
}

func eventuallyFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(clusterWait)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func joinedPair(t *testing.T, aLabel string) (*clusterNode, *clusterNode) {
	t.Helper()

	a := startNode(t, clusterConfig(t, aLabel))
	a.waitStarted(t)

	token := a.mintToken(t)

	b := startNode(t, joinerConfig(t, "ims-b"))
	b.waitStarted(t)

	if code := b.join(t, token); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	eventuallyFor(t, "a login on the joined node", func() bool {
		_, code := b.login(t, apitest.Email)
		return code == http.StatusOK
	})

	return a, b
}

func TestClusterJoinThroughTheAPI(t *testing.T) {
	a, b := joinedPair(t, settings.DefaultDiameterLabel)

	if code := b.join(t, a.mintToken(t)); code != http.StatusForbidden {
		t.Fatalf("second join = %d, want %d", code, http.StatusForbidden)
	}

	var status cluster.Status

	eventuallyFor(t, "two members", func() bool {
		return b.api(t, http.MethodGet, "/api/v1/cluster", nil, &status) == http.StatusOK && len(status.Members) == 2
	})

	eventuallyFor(t, "the joined node to be ready", func() bool {
		code, _ := b.readyz(t)
		return code == http.StatusOK
	})

	eventuallyFor(t, "a Diameter host per node", func() bool {
		da, dbs := getDiameter(t, a.srv), getDiameter(t, b.srv)

		return da.Host == settings.DefaultDiameterLabel+"."+imsRealm && dbs.Host == "ims-b."+imsRealm
	})

	if b.srv.core.Load() == nil || len(b.srv.core.Load().sip.Listeners()) == 0 {
		t.Fatal("the joined node does not serve SIP")
	}

	if code := b.api(t, http.MethodPost, "/api/v1/users", api.CreateUserParams{
		Email: "ops@example.com", Password: "correct horse", RoleID: db.RoleReadOnly,
	}, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/users on the follower = %d", code)
	}

	eventuallyFor(t, "the user on the leader", func() bool {
		return a.api(t, http.MethodGet, "/api/v1/users/ops@example.com", nil, nil) == http.StatusOK
	})

	if _, code := a.login(t, "ops@example.com"); code != http.StatusOK {
		t.Fatalf("login on the leader as a user created on the follower = %d", code)
	}

	var member string

	for _, m := range status.Members {
		if m.DiameterLabel == "ims-b" {
			member = m.NodeID
		}
	}

	var bs api.Status
	if code := b.public(t, http.MethodGet, "/api/v1/status", nil, &bs); code != http.StatusOK ||
		bs.NodeID != member || bs.DiameterLabel != "ims-b" || bs.Generated || bs.DiameterHost != "ims-b."+imsRealm || !bs.Clustered {
		t.Fatalf("GET /api/v1/status on the joined node = %d, %+v, want node %s", code, bs, member)
	}

	if got := b.srv.Config.Diameter.HostLabel; got != "ims-b" {
		t.Fatalf("the joined node's Diameter host label = %s", got)
	}

	for _, m := range status.Members {
		want := settings.DefaultDiameterLabel
		if m.NodeID == member {
			want = "ims-b"
		}

		if m.DiameterLabel != want || m.DiameterHost != want+"."+imsRealm || m.Generated {
			t.Fatalf("member %+v, want the Diameter host label %s", m, want)
		}
	}

	if code, res := b.apiError(t, http.MethodDelete, "/api/v1/cluster/members/nobody?force=true"); code != http.StatusMisdirectedRequest ||
		res.Leader != "http://"+a.addr {
		t.Fatalf("DELETE member on a follower = %d, %+v, want %d and the leader http://%s", code, res,
			http.StatusMisdirectedRequest, a.addr)
	}

	c := startNode(t, joinerConfig(t, "ims-b"))
	c.waitStarted(t)

	if code := c.join(t, a.mintToken(t)); code != http.StatusConflict {
		t.Fatalf("join with a member's Diameter host label = %d, want %d", code, http.StatusConflict)
	}

	if code := a.api(t, http.MethodPut, "/api/v1/cluster/members/"+member, api.UpdateMemberParams{DisplayName: "paris-1"}, nil); code != http.StatusOK {
		t.Fatalf("PUT member = %d", code)
	}

	eventuallyFor(t, "the display name on the joined node", func() bool {
		var st api.Status

		return b.public(t, http.MethodGet, "/api/v1/status", nil, &st) == http.StatusOK && st.DisplayName == "paris-1"
	})

	if code := a.api(t, http.MethodPut, "/api/v1/cluster/members/"+member, api.UpdateMemberParams{DisplayName: strings.Repeat("a", 64)}, nil); code != http.StatusBadRequest {
		t.Fatalf("PUT a long display name = %d, want %d", code, http.StatusBadRequest)
	}

	if code := a.api(t, http.MethodPut, "/api/v1/cluster/members/"+member, api.UpdateMemberParams{}, nil); code != http.StatusOK {
		t.Fatalf("PUT an empty display name = %d", code)
	}

	eventuallyFor(t, "the cleared display name", func() bool {
		m, ok := a.member(t, member)
		return ok && m.DisplayName == ""
	})

	if code := a.api(t, http.MethodDelete, "/api/v1/cluster/members/"+member, nil, nil); code != http.StatusConflict {
		t.Fatalf("DELETE an active member = %d, want %d", code, http.StatusConflict)
	}

	if code := a.api(t, http.MethodPost, "/api/v1/cluster/members/"+member+"/drain", nil, nil); code != http.StatusOK {
		t.Fatalf("POST drain = %d", code)
	}

	eventuallyFor(t, "the member to drain", func() bool {
		m, ok := a.member(t, member)
		return ok && m.DrainState == string(db.DrainDrained)
	})

	if code := a.api(t, http.MethodDelete, "/api/v1/cluster/members/"+member, nil, nil); code != http.StatusOK {
		t.Fatalf("DELETE member = %d", code)
	}

	eventuallyFor(t, "the removed node to fail readiness", func() bool {
		code, _ := b.readyz(t)
		return code == http.StatusServiceUnavailable
	})

	eventuallyFor(t, "the removed node to close SIP and Diameter", func() bool {
		return b.srv.core.Load() == nil
	})
}

func TestDrainingTheLeaderHandsItOver(t *testing.T) {
	a, b := joinedPair(t, "ims-a")

	var st cluster.Status
	if code := a.api(t, http.MethodGet, "/api/v1/cluster", nil, &st); code != http.StatusOK {
		t.Fatalf("GET /api/v1/cluster = %d", code)
	}

	if st.LeaderID != st.NodeID {
		t.Fatalf("leader %s, want the first node %s", st.LeaderID, st.NodeID)
	}

	if a.srv.refusing.Load() {
		t.Fatal("the active leader refuses registrations")
	}

	if code := a.api(t, http.MethodPost, "/api/v1/cluster/members/"+st.NodeID+"/drain", nil, nil); code != http.StatusOK {
		t.Fatalf("POST drain = %d", code)
	}

	eventuallyFor(t, "the draining node to refuse registrations", a.srv.refusing.Load)

	var after cluster.Status

	eventuallyFor(t, "the drained node to hand the leadership over", func() bool {
		if a.api(t, http.MethodGet, "/api/v1/cluster", nil, &after) != http.StatusOK {
			return false
		}

		i := slices.IndexFunc(after.Members, func(m cluster.MemberStatus) bool { return m.NodeID == st.NodeID })

		return i >= 0 && after.Members[i].DrainState == string(db.DrainDrained) && after.LeaderID != "" && after.LeaderID != st.NodeID
	})

	if !a.srv.refusing.Load() {
		t.Fatal("the drained node accepts registrations")
	}

	if b.srv.refusing.Load() {
		t.Fatal("the new leader refuses registrations")
	}

	token, code := a.login(t, apitest.Email)
	if code != http.StatusOK {
		t.Fatalf("login on the drained node = %d", code)
	}

	if code := b.as(t, token, http.MethodGet, "/api/v1/cluster", nil, nil); code != http.StatusOK {
		t.Fatalf("GET /api/v1/cluster on the new leader = %d", code)
	}

	if code := a.api(t, http.MethodPost, "/api/v1/cluster/members/"+st.NodeID+"/resume", nil, nil); code != http.StatusOK {
		t.Fatalf("POST resume = %d", code)
	}

	eventuallyFor(t, "the resumed node to accept registrations", func() bool { return !a.srv.refusing.Load() })

	eventuallyFor(t, "the resumed node to be active", func() bool {
		m, ok := a.member(t, st.NodeID)
		return ok && m.DrainState == string(db.DrainActive)
	})
}

func TestNodeRemovedWhileDownIsFencedOnRestart(t *testing.T) {
	a, b := joinedPair(t, "ims-a")

	var bs api.Status
	if code := b.public(t, http.MethodGet, "/api/v1/status", nil, &bs); code != http.StatusOK || bs.NodeID == "" {
		t.Fatalf("GET /api/v1/status = %d, %+v", code, bs)
	}

	eventuallyFor(t, "the joined node to report", func() bool {
		m, ok := a.member(t, bs.NodeID)
		return ok && m.Healthy != nil
	})

	b.stop()

	if code := a.api(t, http.MethodDelete, "/api/v1/cluster/members/"+bs.NodeID+"?force=true", nil, nil); code != http.StatusOK {
		t.Fatalf("DELETE member = %d", code)
	}

	b = b.restart(t)

	diameterAddr := b.diameterListener(t)

	eventuallyFor(t, "the restarted node to fail its member check", func() bool {
		code, c, ok := b.check(t, cluster.CheckMember)
		return ok && code == http.StatusServiceUnavailable && !c.OK
	})

	eventuallyFor(t, "the fenced node to close SIP and Diameter", func() bool {
		return b.srv.core.Load() == nil
	})

	if diameterAddr != "" {
		var d net.Dialer

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if conn, err := d.DialContext(ctx, "tcp", diameterAddr); err == nil {
			_ = conn.Close()

			t.Fatalf("the fenced node accepts Diameter on %s", diameterAddr)
		}
	}

	b = b.restart(t)

	if b.srv.core.Load() != nil {
		t.Fatal("the fenced node serves SIP and Diameter")
	}

	if code := b.public(t, http.MethodGet, "/api/v1/livez", nil, nil); code != http.StatusNoContent {
		t.Fatalf("GET /api/v1/livez on the fenced node = %d", code)
	}

	code, res := b.readyz(t)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/readyz on the fenced node = %d", code)
	}

	if i := slices.IndexFunc(res.Checks, func(c cluster.Check) bool { return c.Name == cluster.CheckMember }); i < 0 ||
		res.Checks[i].OK || !strings.Contains(res.Checks[i].Detail, "removed") {
		t.Fatalf("checks of the fenced node = %+v", res.Checks)
	}

	var st api.Status
	if code := b.public(t, http.MethodGet, "/api/v1/status", nil, &st); code != http.StatusOK ||
		st.Formation.State != cluster.FormationRemoved || st.NodeID != bs.NodeID {
		t.Fatalf("GET /api/v1/status on the fenced node = %d, %+v", code, st)
	}

	if code := b.public(t, http.MethodPost, "/api/v1/init", map[string]string{"email": "a@b.c", "password": "p"}, nil); code != http.StatusGone {
		t.Fatalf("POST /api/v1/init on the fenced node = %d, want %d", code, http.StatusGone)
	}
}

func TestDiameterStaysClosedUntilTheClusterRecordsTheLabel(t *testing.T) {
	_, b := joinedPair(t, "ims-a")

	eventuallyFor(t, "Diameter open on the joined node", func() bool {
		return b.diameterListener(t) != "" && b.srv.Config.Diameter.HostLabel == "ims-b"
	})

	b.stop()

	b.cfg.Diameter.HostLabel = "ims-a"
	b = b.restart(t)

	eventuallyFor(t, "the label check to fail on the restarted node", func() bool {
		code, c, ok := b.check(t, cluster.CheckLabel)
		return ok && code == http.StatusServiceUnavailable && !c.OK && strings.Contains(c.Detail, "ims-a")
	})

	if c := b.srv.core.Load(); c != nil {
		t.Fatalf("SIP and Diameter open with another member's label: %v", c.attrs())
	}

	b.cfg.Diameter.HostLabel = "ims-new"
	b = b.restart(t)

	eventuallyFor(t, "Diameter open with the new label once the cluster records it", func() bool {
		_, c, ok := b.check(t, cluster.CheckLabel)
		return ok && c.OK && b.diameterListener(t) != "" && b.srv.Config.Diameter.HostLabel == "ims-new"
	})

	if got := getDiameter(t, b.srv).Host; got != "ims-new."+imsRealm {
		t.Fatalf("Diameter host = %s", got)
	}
}

func TestRestartedLeaderRejoins(t *testing.T) {
	a := startNode(t, clusterConfig(t, "ims-a"))
	a.waitStarted(t)

	var joined []*clusterNode

	for _, label := range []string{"ims-b", "ims-c"} {
		token := a.mintToken(t)

		n := startNode(t, joinerConfig(t, label))
		n.waitStarted(t)

		if code := n.join(t, token); code != http.StatusOK {
			t.Fatalf("POST /api/v1/cluster/join = %d", code)
		}

		joined = append(joined, n)
	}

	for _, n := range joined {
		eventuallyFor(t, "a voter of the joined node", func() bool {
			m, ok := a.member(t, n.srv.node.ID())
			return ok && m.Suffrage == "voter"
		})
	}

	a.cfg.Peers = nil
	a = a.restart(t)
	a.waitStarted(t)

	eventuallyFor(t, "the restarted node to be ready", func() bool {
		code, _ := a.readyz(t)
		return code == http.StatusOK
	})
}
