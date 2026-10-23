package client_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/pidf"
	"github.com/ellanetworks/ims/sip/ua"
)

// registrarESC is a minimal registrar and event state compositor (RFC 3903 §6) for the client's tests: it answers
// each REGISTER and PUBLISH 200, a PUBLISH with a new entity-tag and the Expires it grants, unless told to answer
// the next modification 412 or to hold the next REGISTER.
type registrarESC struct {
	addr netip.AddrPort

	mu   sync.Mutex
	reqs []*sip.Request
	tags int
	// grant is the Expires of the 2xx to a PUBLISH; lose answers the next PUBLISH with a SIP-If-Match 412; hold
	// keeps the next REGISTER unanswered, in held.
	grant int
	lose  bool
	hold  bool
	held  *ua.Request
}

func (e *env) registrarESC() *registrarESC {
	e.t.Helper()

	s := &registrarESC{addr: e.n.Addr(), grant: 60}

	e.agent(s.addr, netip.AddrPort{}, func(cfg *ua.Config) {
		cfg.Allow, cfg.OnRequest = []string{"REGISTER", "PUBLISH"}, s.onRequest
	})

	return s
}

func (s *registrarESC) onRequest(r *ua.Request) {
	req := r.Message()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.reqs = append(s.reqs, req.Clone())

	switch req.Method {
	case "REGISTER":
		if s.hold {
			s.hold, s.held = false, r
			return
		}

		_ = r.Respond(registered(req))
	case "PUBLISH":
		if s.lose && req.Header.Has("SIP-If-Match") {
			s.lose = false
			_ = r.Reject(412)

			return
		}

		s.tags++

		expires := strconv.Itoa(s.grant)
		if req.Header.Get("Expires") == "0" {
			expires = "0"
		}

		_ = r.Respond(ua.Reply{Code: 200, Header: sip.Header{
			{Name: "SIP-ETag", Value: "e" + strconv.Itoa(s.tags)},
			{Name: "Expires", Value: expires},
		}})
	default:
		_ = r.Reject(405)
	}
}

// registered is the 200 of a REGISTER, its contact bound for the Expires asked.
func registered(req *sip.Request) ua.Reply {
	rp := ua.Reply{Code: 200}
	if e := req.Header.Get("Expires"); e != "0" {
		rp.Header = sip.Header{{Name: "Contact", Value: req.Header.Get("Contact") + ";expires=" + e}}
	}

	return rp
}

// take returns the requests of method since the last take.
func (s *registrarESC) take(method string) []*sip.Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out, rest []*sip.Request

	for _, r := range s.reqs {
		if r.Method == method {
			out = append(out, r)
		} else {
			rest = append(rest, r)
		}
	}

	s.reqs = rest

	return out
}

// results record the final responses of requests.
type results struct {
	mu    sync.Mutex
	codes []int
}

func (r *results) done(res *sip.Response, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	code := -1
	if err == nil {
		code = res.StatusCode
	}

	r.codes = append(r.codes, code)
}

func (r *results) take() []int {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := r.codes
	r.codes = nil

	return out
}

// groupsOf are the groups of the affiliation PUBLISH req, and whether it carries the mcptt-info naming the user
// (TS 24.379 §9.2.1.2 step 2).
func groupsOf(t *testing.T, req *sip.Request, user string) (string, bool) {
	t.Helper()

	part, ok, err := req.Content().Find(mcpttinfo.ContentType)
	named := false

	if ok && err == nil {
		if info, err := mcpttinfo.Decode(part.Body); err == nil && info.Params != nil {
			uri, _ := info.Params.RequestURI.URI()
			named = uri == user
		}
	}

	part, ok, err = req.Content().Find(pidf.ContentType)
	if !ok || err != nil {
		return "", named
	}

	var groups []string

	for _, g := range []string{fire, police} {
		if strings.Contains(string(part.Body), `group="`+g+`"`) {
			groups = append(groups, g)
		}
	}

	return strings.Join(groups, " "), named
}

// TS 24.379 §9.2.1.2, RFC 3903: the affiliations published, every PUBLISH with the mcptt-info naming the user; the
// publication refreshed before the expiry its 2xx granted, without its state; a modification answered 412 published
// anew, its caller told the final response; changes serialised, each conditional on the last entity-tag; removed
// with Expires 0; removal without a publication refused locally.
func TestPublication(t *testing.T) {
	e := newEnv(t)
	s := e.registrarESC()
	a := e.user("alice", 1, s.addr, nil)

	var r results

	if err := a.Affiliate(nil, r.done); !errors.Is(err, client.ErrNotPublished) {
		t.Fatalf("de-affiliation without a publication: %v", err)
	}

	fireOnly := []sip.URI{mustURI(t, fire)}
	both := []sip.URI{mustURI(t, fire), mustURI(t, police)}

	if err := a.Affiliate(fireOnly, r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	type want struct {
		ifMatch, expires, groups string
	}

	check := func(what string, codes []int, ws ...want) {
		t.Helper()

		if got := r.take(); len(got) != len(codes) || len(codes) > 0 && got[0] != codes[0] {
			t.Errorf("%s: results %v, want %v", what, got, codes)
		}

		reqs := s.take("PUBLISH")
		if len(reqs) != len(ws) {
			t.Fatalf("%s: %d PUBLISHes, want %d", what, len(reqs), len(ws))
		}

		for i, w := range ws {
			req := reqs[i]
			groups, named := groupsOf(t, req, a.id)

			if req.Header.Get("SIP-If-Match") != w.ifMatch || req.Header.Get("Expires") != w.expires || groups != w.groups || !named {
				t.Errorf("%s: PUBLISH %d: SIP-If-Match %q Expires %q groups %q named %v, want %+v", what, i,
					req.Header.Get("SIP-If-Match"), req.Header.Get("Expires"), groups, named, w)
			}
		}
	}

	check("initial", []int{200}, want{"", "4294967295", fire})

	// Refreshed halfway through the 60 s granted (RFC 3903 §4.1).
	e.n.Advance(31 * time.Second)
	check("refresh", nil, want{"e1", "4294967295", ""})

	s.mu.Lock()
	s.lose = true
	s.mu.Unlock()

	if err := a.Affiliate(both, r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()
	check("412", []int{200}, want{"e2", "4294967295", fire + " " + police}, want{"", "4294967295", fire + " " + police})

	// Two changes at once: the second after the first, conditional on its entity-tag.
	if err := a.Affiliate(fireOnly, r.done); err != nil {
		t.Fatal(err)
	}

	if err := a.Affiliate(both, r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()
	check("serialised", []int{200, 200}, want{"e3", "4294967295", fire}, want{"e4", "4294967295", fire + " " + police})

	if err := a.Affiliate(nil, r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()
	check("removal", []int{200}, want{"e5", "0", ""})

	if err := a.Affiliate(nil, r.done); !errors.Is(err, client.ErrNotPublished) {
		t.Errorf("de-affiliation once removed: %v", err)
	}

	// No refresh once removed.
	e.n.Advance(time.Hour)
	check("removed", nil)
}

// RFC 3261 §10.2.4: the 200 to a refresh in flight as the client unregisters does not bring the registration back.
func TestRefreshAfterUnregister(t *testing.T) {
	e := newEnv(t)
	s := e.registrarESC()
	a := e.user("alice", 1, s.addr, nil)

	var r results

	t0 := e.n.Clock().Now()

	if err := a.Register(600*time.Second, r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	s.mu.Lock()
	s.hold = true
	s.mu.Unlock()

	e.n.Advance(301 * time.Second)

	if err := a.Unregister(r.done); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	s.mu.Lock()
	held := s.held
	s.mu.Unlock()

	if held == nil {
		t.Fatal("no refresh")
	}

	_ = held.Respond(registered(held.Message()))

	e.n.Quiesce()

	if got := r.take(); len(got) != 2 || got[0] != 200 || got[1] != 200 {
		t.Errorf("results %v", got)
	}

	if a.expect(t, "registration")[0].Response.StatusCode != 200 {
		t.Error("refresh not answered 200")
	}

	e.n.Advance(time.Hour)

	reqs := s.take("REGISTER")
	if len(reqs) != 3 || reqs[2].Header.Get("Expires") != "0" {
		t.Fatalf("%d REGISTERs", len(reqs))
	}

	a.expect(t)

	// TS 24.379 §7.2.1, TS 33.180 Tables B.2.2.2-1, B.2.2.3-1: the access token of each REGISTER, made for it.
	for i, req := range reqs[:2] {
		part, ok, err := req.Content().Find(mcpttinfo.ContentType)
		if !ok || err != nil {
			t.Fatalf("REGISTER %d: no mcptt-info", i)
		}

		info, err := mcpttinfo.Decode(part.Body)
		if err != nil || info.Params == nil {
			t.Fatal(err)
		}

		token, _ := info.Params.AccessToken.Text()
		claims := tokenClaims(t, token)

		// Sent at once, refreshed at 300 s.
		sent := t0.Add(time.Duration(i) * 300 * time.Second)

		if claims.MCPTTID != a.id || claims.ClientID != "urn:uuid:00000000-0000-1000-8000-000000000001" ||
			claims.Scope != "3gpp:mc:ptt_service" || claims.Exp != sent.Add(time.Hour).Unix() {
			t.Errorf("REGISTER %d: claims %+v", i, claims)
		}
	}
}

type claims struct {
	Exp      int64  `json:"exp"`
	Scope    string `json:"scope"`
	ClientID string `json:"client_id"`
	MCPTTID  string `json:"mcptt_id"`
}

// tokenClaims are the claims of an Unsecured JWT (RFC 7519 §6.1).
func tokenClaims(t *testing.T, token string) claims {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[2] != "" {
		t.Fatalf("token %q", token)
	}

	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}

	var c claims
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}

	return c
}

// TS 33.180 Tables B.2.2.2-1, B.2.2.3-1: the claims of an UnsignedToken.
func TestUnsignedToken(t *testing.T) {
	exp := time.Unix(1700000000, 0)

	c := tokenClaims(t, client.UnsignedToken("sip:alice@mcptt.example.com", "urn:uuid:1", exp))
	if c != (claims{Exp: 1700000000, Scope: "3gpp:mc:ptt_service", ClientID: "urn:uuid:1", MCPTTID: "sip:alice@mcptt.example.com"}) {
		t.Errorf("claims %+v", c)
	}
}
