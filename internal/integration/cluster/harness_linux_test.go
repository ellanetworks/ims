//go:build integration && linux

package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/netnstest"
)

const (
	adminEmail    = "admin@example.com"
	adminPassword = "integration password"
	waitTimeout   = 60 * time.Second
	apiPort       = 5020
	clusterPort   = 7000
)

var binary, upgraded, sharedSecret string

func TestMain(m *testing.M) {
	if !netnstest.Inside() {
		netnstest.Main(m)
	}

	os.Exit(run(m))
}

func run(m *testing.M) int {
	if out, err := exec.CommandContext(context.Background(), "ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "bring up the loopback interface: %v: %s\n", err, out)
		return 1
	}

	dir, err := os.MkdirTemp("", "ims-cluster-integration-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	defer func() { _ = os.RemoveAll(dir) }()

	binary, upgraded = filepath.Join(dir, "ims"), filepath.Join(dir, "ims-next")
	sharedSecret = filepath.Join(dir, "cluster-secret")

	if err := writeSecret(sharedSecret); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	builds := []*exec.Cmd{
		exec.CommandContext(context.Background(), "go", "build", "-o", binary, "../../../cmd/ims"),
		exec.CommandContext(context.Background(), "go", "build", "-tags", "upgradetest", "-o", upgraded, "../../../cmd/ims"),
	}

	for _, build := range builds {
		build.Stdout, build.Stderr = os.Stdout, os.Stderr

		if err := build.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	for _, build := range builds {
		if err := build.Wait(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	return m.Run()
}

type fabric struct {
	host   *netnstest.Netns
	bridge string
	subnet int
	hosts  int
}

var (
	fabricsMu sync.Mutex
	fabrics   = map[*testing.T]*fabric{}
	subnets   int
)

func fabricOf(t *testing.T) *fabric {
	t.Helper()

	fabricsMu.Lock()
	defer fabricsMu.Unlock()

	if f, ok := fabrics[t]; ok {
		return f
	}

	subnets++

	f := &fabric{host: netnstest.Current(t), bridge: "br" + strconv.Itoa(subnets), subnet: subnets%250 + 1, hosts: 1}

	netnstest.Bridge(t, f.host, f.bridge, []netip.Prefix{f.prefix(1)})

	fabrics[t] = f

	t.Cleanup(func() {
		fabricsMu.Lock()
		delete(fabrics, t)
		fabricsMu.Unlock()
	})

	return f
}

func (f *fabric) prefix(host int) netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 200, byte(f.subnet), byte(host)}), 24)
}

func (f *fabric) attach(t *testing.T) (*netnstest.Netns, netip.Addr) {
	t.Helper()

	fabricsMu.Lock()
	f.hosts++
	host := f.hosts
	fabricsMu.Unlock()

	ns := netnstest.New(t)
	p := f.prefix(host)

	netnstest.Attach(t, f.host, f.bridge, fmt.Sprintf("v%d-%d", f.subnet, host), ns, []netip.Prefix{p})

	return ns, p.Addr()
}

type node struct {
	t           *testing.T
	name        string
	dir         string
	ns          *netnstest.Netns
	addr        netip.Addr
	clusterPort int
	bin         string
	cmd         *exec.Cmd
	exited      chan struct{}
	session     atomic.Pointer[string]
	id          string
	secret      string
	join        []string
	expect      int
	standalone  bool
}

func writeSecret(path string) error {
	b := make([]byte, 32)
	_, _ = rand.Read(b)

	return os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600)
}

func newIdleNode(t *testing.T, name string) *node {
	t.Helper()

	ns, addr := fabricOf(t).attach(t)

	n := &node{
		t: t, name: name, bin: binary, dir: t.TempDir(), ns: ns, addr: addr, clusterPort: clusterPort,
		secret: sharedSecret,
	}

	t.Cleanup(func() {
		n.kill()

		if t.Failed() {
			if out, err := os.ReadFile(filepath.Join(n.dir, "ims.log")); err == nil {
				fmt.Fprintf(os.Stderr, "--- %s log ---\n%s\n", n.name, tail(out, 8<<10))
			}
		}
	})

	return n
}

func (n *node) clusterAddress() string {
	return netip.AddrPortFrom(n.addr, uint16(n.clusterPort)).String()
}

func (n *node) apiAddress() string {
	return "http://" + netip.AddrPortFrom(n.addr, apiPort).String()
}

func newNode(t *testing.T, name string) *node {
	t.Helper()

	n := newIdleNode(t, name)
	n.configure()
	n.start()

	return n
}

func (n *node) configure() {
	n.t.Helper()

	config := fmt.Sprintf(`logging:
  level: debug
db:
  path: %s
api:
  address: %s
  port: %d
sip:
  addresses: [%s]
diameter:
  address: %s
`, filepath.Join(n.dir, "ims.db"), n.addr, apiPort, n.addr, n.addr)

	if !n.standalone {
		config += fmt.Sprintf(`cluster:
  address: %s
  port: %d
  secret_file: %s
`, n.addr, n.clusterPort, n.secret)
	}

	if len(n.join) > 0 {
		config += "  join: [" + strings.Join(n.join, ", ") + "]\n"
	}

	if n.expect > 0 {
		config += "  bootstrap_expect: " + strconv.Itoa(n.expect) + "\n"
	}

	if err := os.WriteFile(filepath.Join(n.dir, "ims.yaml"), []byte(config), 0o600); err != nil {
		n.t.Fatal(err)
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}

	return b
}

func (n *node) start() {
	n.t.Helper()

	n.launch()

	eventually(n.t, n.name+" to answer", n.live)
}

func (n *node) launch() {
	n.t.Helper()

	log, err := os.OpenFile(filepath.Join(n.dir, "ims.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		n.t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), n.bin, "--config", filepath.Join(n.dir, "ims.yaml"))
	cmd.Stdout, cmd.Stderr = log, log

	n.ns.Do(func() { err = cmd.Start() })

	if err != nil {
		_ = log.Close()
		n.t.Fatal(err)
	}

	exited := make(chan struct{})

	go func() {
		_ = cmd.Wait()
		_ = log.Close()

		close(exited)
	}()

	n.cmd, n.exited = cmd, exited
}

func (n *node) running() bool {
	if n.cmd == nil {
		return false
	}

	select {
	case <-n.exited:
		return false
	default:
		return true
	}
}

func (n *node) kill() {
	if !n.running() {
		return
	}

	_ = n.cmd.Process.Signal(syscall.SIGCONT)
	_ = n.cmd.Process.Kill()
	<-n.exited
}

func (n *node) stop() {
	if !n.running() {
		return
	}

	_ = n.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-n.exited:
	case <-time.After(30 * time.Second):
		n.kill()
	}
}

func (n *node) freeze() { _ = n.cmd.Process.Signal(syscall.SIGSTOP) }

func (n *node) thaw() { _ = n.cmd.Process.Signal(syscall.SIGCONT) }

func (n *node) token() string {
	if t := n.session.Load(); t != nil {
		return *t
	}

	return ""
}

func (n *node) authenticate(token string) {
	n.session.Store(&token)
}

func (n *node) url(path string) string {
	return n.apiAddress() + path
}

func (n *node) request(method, path string, body, out any) (int, error) {
	return n.requestAs(n.token(), method, path, body, out)
}

type apiError struct {
	code   int
	msg    string
	leader string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%d: %s", e.code, e.msg)
}

func (n *node) requestAs(token, method, path string, body, out any) (int, error) {
	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}

		reader = bytes.NewReader(raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, n.url(path), reader)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
		Leader string          `json:"leader"`
	}

	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &envelope)

	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s on %s = %w", method, path, n.name,
			&apiError{code: resp.StatusCode, msg: envelope.Error, leader: envelope.Leader})
	}

	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return resp.StatusCode, err
		}
	}

	return resp.StatusCode, nil
}

func leaderOf(err error) string {
	var e *apiError
	if errors.As(err, &e) {
		return e.leader
	}

	return ""
}

func (n *node) must(method, path string, body, out any) {
	n.t.Helper()

	if _, err := n.request(method, path, body, out); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) raw(method, path, contentType string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, n.url(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	req.Header.Set("Authorization", "Bearer "+n.token())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 300 {
		var envelope struct {
			Error  string `json:"error"`
			Leader string `json:"leader"`
		}

		_ = json.Unmarshal(out, &envelope)

		return out, fmt.Errorf("%s %s on %s = %w", method, path, n.name,
			&apiError{code: resp.StatusCode, msg: envelope.Error, leader: envelope.Leader})
	}

	return out, nil
}

func (n *node) login(email, password string) (string, error) {
	var login struct {
		Token string `json:"token"`
	}

	if _, err := n.requestAs("", http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": password}, &login); err != nil {
		return "", err
	}

	return login.Token, nil
}

func (n *node) logIn() {
	n.t.Helper()

	eventually(n.t, n.name+" to accept the admin", func() error {
		token, err := n.login(adminEmail, adminPassword)
		if err != nil {
			return err
		}

		if _, err := n.requestAs(token, http.MethodGet, "/api/v1/cluster", nil, nil); err != nil {
			return err
		}

		n.authenticate(token)

		return nil
	})
}

func (n *node) initialize() {
	n.t.Helper()

	var login struct {
		Token string `json:"token"`
	}

	eventually(n.t, n.name+" to initialize", func() error {
		code, err := n.requestAs("", http.MethodPost, "/api/v1/init",
			map[string]string{"email": adminEmail, "password": adminPassword}, &login)
		if code != http.StatusServiceUnavailable {
			return nil
		}

		return err
	})

	if login.Token == "" {
		n.t.Fatalf("%s did not initialize", n.name)
	}

	n.authenticate(login.Token)
}

func (n *node) live() error {
	code, err := n.requestAs("", http.MethodGet, "/api/v1/livez", nil, nil)
	if err != nil {
		return err
	}

	if code != http.StatusNoContent {
		return fmt.Errorf("livez = %d", code)
	}

	return nil
}

type member struct {
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	APIAddress  string `json:"api_address"`
	Suffrage    string `json:"suffrage"`
	Leader      bool   `json:"leader"`
	CaughtUp    bool   `json:"caught_up"`
	Healthy     *bool  `json:"healthy"`
	DrainState  string `json:"drain_state"`
	DisplayName string `json:"display_name"`
}

type status struct {
	NodeID           string   `json:"node_id"`
	ClusterID        string   `json:"cluster_id"`
	Schema           int      `json:"schema"`
	LeaderID         string   `json:"leader_id"`
	Healthy          *bool    `json:"healthy"`
	FailureTolerance *int     `json:"failure_tolerance"`
	Members          []member `json:"members"`
}

func (n *node) status() (status, error) {
	var st status

	_, err := n.request(http.MethodGet, "/api/v1/cluster", nil, &st)

	return st, err
}

func (n *node) identify() {
	n.t.Helper()

	st, err := n.status()
	if err != nil {
		n.t.Fatal(err)
	}

	n.id = st.NodeID
}

func (n *node) redeem(token string) (int, error) {
	deadline := time.Now().Add(waitTimeout)

	for {
		code, err := n.request(http.MethodPost, "/api/v1/cluster/join", map[string]string{"token": token}, nil)
		if code != http.StatusServiceUnavailable || time.Now().After(deadline) {
			return code, err
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func (n *node) mintToken() string {
	n.t.Helper()

	var token struct {
		Token string `json:"token"`
	}

	n.must(http.MethodPost, "/api/v1/cluster/tokens", map[string]int{}, &token)

	return token.Token
}

type cluster struct {
	t     *testing.T
	nodes []*node
}

func newCluster(t *testing.T) *cluster {
	t.Helper()

	const size = 3

	c := &cluster{t: t}

	for i := range size {
		c.nodes = append(c.nodes, newNode(t, "node"+strconv.Itoa(i)))
	}

	c.nodes[0].initialize()

	for _, n := range c.nodes[1:] {
		c.join(n)
	}

	for _, n := range c.nodes {
		n.identify()
	}

	c.waitStable()

	return c
}

func (c *cluster) join(n *node) {
	c.t.Helper()

	if _, err := n.redeem(c.live().mintToken()); err != nil {
		c.t.Fatal(err)
	}

	n.logIn()
}

func (c *cluster) live() *node {
	for _, n := range c.nodes {
		if n.running() && n.token() != "" {
			return n
		}
	}

	c.t.Fatal("no live node")

	return nil
}

func (c *cluster) leaderStatus() (status, error) {
	for _, n := range c.nodes {
		if !n.running() || n.token() == "" {
			continue
		}

		st, err := n.status()
		if err == nil && st.LeaderID != "" && st.LeaderID == st.NodeID {
			return st, nil
		}
	}

	return status{}, errors.New("no node answers as the leader")
}

func (c *cluster) waitStable() {
	const voters = 3

	c.t.Helper()

	eventually(c.t, fmt.Sprintf("%d healthy voters", voters), func() error {
		st, err := c.leaderStatus()
		if err != nil {
			return err
		}

		count := 0

		for _, m := range st.Members {
			if m.Suffrage == "voter" && m.Healthy != nil && *m.Healthy {
				count++
			}
		}

		if count != voters {
			return fmt.Errorf("%d healthy voters", count)
		}

		return nil
	})
}

func (c *cluster) leader() *node {
	c.t.Helper()

	var leader *node

	eventually(c.t, "a leader", func() error {
		st, err := c.leaderStatus()
		if err != nil {
			return err
		}

		for _, n := range c.nodes {
			if n.id == st.LeaderID && n.running() {
				leader = n
				return nil
			}
		}

		return fmt.Errorf("the leader %s is not a known node", st.LeaderID)
	})

	return leader
}

func (c *cluster) followers(leader *node) []*node {
	var out []*node

	for _, n := range c.nodes {
		if n != leader && n.running() {
			out = append(out, n)
		}
	}

	return out
}

func (c *cluster) otherThan(n *node) *node {
	c.t.Helper()

	for _, o := range c.nodes {
		if o != n && o.running() && o.token() != "" {
			return o
		}
	}

	c.t.Fatal("no other live node")

	return nil
}

func (c *cluster) member(n *node) (member, error) {
	st, err := c.leaderStatus()
	if err != nil {
		return member{}, err
	}

	for _, m := range st.Members {
		if m.NodeID == n.id {
			return m, nil
		}
	}

	return member{}, fmt.Errorf("%s is not a member", n.name)
}

func (c *cluster) drain(n *node) {
	c.t.Helper()

	via := c.otherThan(n)
	via.must(http.MethodPost, "/api/v1/cluster/members/"+n.id+"/drain", nil, nil)

	eventually(c.t, n.name+" drained", func() error {
		st, err := via.status()
		if err != nil {
			return err
		}

		for _, m := range st.Members {
			if m.NodeID == n.id && m.DrainState != "drained" {
				return fmt.Errorf("%s", m.DrainState)
			}
		}

		return nil
	})
}

func (c *cluster) resume(n *node) {
	c.t.Helper()

	eventually(c.t, n.name+" resumed", func() error {
		_, err := n.request(http.MethodPost, "/api/v1/cluster/members/"+n.id+"/resume", nil, nil)
		return err
	})
}

func (c *cluster) createUser(via *node, email string) {
	c.t.Helper()

	via.must(http.MethodPost, "/api/v1/users",
		map[string]any{"email": email, "password": "correct horse", "role_id": 3}, nil)
}

func (c *cluster) everyNodeHasUser(email string) {
	c.t.Helper()

	for _, n := range c.nodes {
		if !n.running() {
			continue
		}

		eventually(c.t, n.name+" to know "+email, func() error {
			_, err := n.request(http.MethodGet, "/api/v1/users/"+email, nil, nil)
			return err
		})
	}
}

func eventually(t *testing.T, what string, f func() error) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	var err error

	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s: %v", what, err)
}
