//go:build linux && (amd64 || arm64)

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/testue"
)

func (s *scene) addHSS(h *hsstest.HSS, priority int) string {
	s.t.Helper()

	return s.addPeer(api.DiameterPeerParams{
		Host: h.Host(), Address: h.Addr().Addr().String(), Port: int(h.Addr().Port()), Applications: []string{"cx"},
		Priority: &priority,
	})
}

func (s *scene) addPeer(p api.DiameterPeerParams) string {
	s.t.Helper()

	body, err := json.Marshal(p)
	if err != nil {
		s.t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodPost, "http://"+s.srv.APIAddr().String()+"/api/v1/diameter/peers",
		bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	var created struct {
		Result api.DiameterPeer `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil || res.StatusCode != http.StatusCreated {
		s.t.Fatalf("POST peer = %d, %v", res.StatusCode, err)
	}

	return created.Result.ID
}

func cxCommands(h *hsstest.HSS) []string {
	var out []string

	for {
		select {
		case r := <-h.Requests():
			switch {
			case r.UAR != nil:
				out = append(out, "UAR")
			case r.MAR != nil:
				out = append(out, "MAR")
			case r.SAR != nil:
				out = append(out, "SAR "+r.SAR.Type.String())
			case r.LIR != nil:
				out = append(out, "LIR")
			}
		default:
			return out
		}
	}
}

// RFC 6733 §5.5.4, TS 29.229 §5.5: with the HSS of a registration lost, its next Cx requests go to another node of
// the HSS realm, which then serves the registration.
func TestRegistrationMovesToAnotherHSSNode(t *testing.T) {
	s := newSceneWith(t, func(srv *server.Server) { srv.ReauthInterval = time.Nanosecond })

	replica := s.hss.Replica(t, "hss2."+domain, s.hss.Addr().Addr())
	s.diameterOpen("hss", s.addHSS(replica, 10))

	u := s.newUE(false, testue.Config{NoRegEvent: true})
	s.register(u)

	if got := cxCommands(replica); len(got) != 0 {
		t.Fatalf("the secondary HSS got %v while the primary was up", got)
	}

	s.hss.Stop(t)
	s.hss.Drain()

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatalf("Reregister with the primary HSS lost: %v", err)
	}

	got := slices.DeleteFunc(cxCommands(replica), func(c string) bool { return c == "UAR" || c == "LIR" })
	if want := []string{"MAR", "SAR " + cx.AssignmentReRegistration.String()}; !slices.Equal(got, want) {
		t.Fatalf("the secondary HSS got %v, want the re-registration's %v", got, want)
	}

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatalf("second Reregister: %v", err)
	}

	if got := cxCommands(replica); len(got) == 0 {
		t.Fatal("the next re-registration did not reach the HSS now serving the registration")
	}
}

func (s *scene) put(path string, body any) {
	s.t.Helper()

	b, err := json.Marshal(body)
	if err != nil {
		s.t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodPut, "http://"+s.srv.APIAddr().String()+path, bytes.NewReader(b))
	if err != nil {
		s.t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}

	_ = res.Body.Close()

	if res.StatusCode != http.StatusOK {
		s.t.Fatalf("PUT %s = %d", path, res.StatusCode)
	}
}

// newSLF starts an SLF that redirects every Cx request to an HSS, for each user (TS 29.228 §8.6, RFC 6733 §6.13).
func newSLF(t *testing.T, to *hsstest.HSS, redirected *commandLog) (string, netip.AddrPort) {
	t.Helper()

	id := diameter.Identity{OriginHost: "slf." + domain, OriginRealm: domain, HostIPAddresses: []netip.Addr{to.Addr().Addr()}, ProductName: "slf"}

	_, addr := diametertest.Listen(t, diametertest.Config{
		Identity: id,
		Peer:     diameter.Peer{ID: "ims", Host: imsHost, Applications: []diameter.Application{{ID: cx.ApplicationID, VendorID: tgpp.VendorID}}},
		Handler: diameter.HandlerFunc(func(_ context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
			redirected.add(req.CommandCode)

			ans, err := diameter.NewRedirectAnswer(req, id, diameter.Redirect{
				Hosts:        []diameter.URI{{Host: to.Host(), Port: to.Addr().Port(), Transport: diameter.TransportTCP}},
				Usage:        diameter.AllUser,
				MaxCacheTime: time.Minute,
			})
			if err != nil {
				panic(err)
			}

			return ans
		}),
	})

	return id.OriginHost, addr
}

// TS 29.229 §5.5: any node of a replicated HSS may terminate a registration, over its own connection.
func TestRTRFromAnotherHSSNode(t *testing.T) {
	s := newScene(t)

	replica := s.hss.Replica(t, "hss2."+domain, s.hss.Addr().Addr())
	s.diameterOpen("hss", s.addHSS(replica, 10))

	u := s.newUE(false, testue.Config{NoRegEvent: true})
	s.register(u)

	rta, err := replica.RTR(s.ctx(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi)
	if err != nil {
		t.Fatalf("RTR from the secondary HSS: %v", err)
	}

	if !rta.Result.Success() {
		t.Fatalf("RTA = %+v", rta)
	}

	eventually(t, "the registration to end", func() bool { return s.registrations("").TotalCount == 0 })
}

// TS 29.229 §5.5: requests for a user go to the HSS the SLF redirects them to, and the S-CSCF then addresses that HSS.
func TestRegistrationThroughAnSLF(t *testing.T) {
	s := newScene(t)

	var redirected commandLog

	host, addr := newSLF(t, s.hss, &redirected)

	low := 65535
	s.put("/api/v1/diameter/peers/hss", api.DiameterPeerParams{
		Host: s.hss.Host(), Address: s.hss.Addr().Addr().String(), Port: int(s.hss.Addr().Port()),
		Applications: []string{"cx"}, Priority: &low,
	})

	slf := s.addPeer(api.DiameterPeerParams{Host: host, Address: addr.Addr().String(), Port: int(addr.Port()), Applications: []string{"cx"}})
	s.diameterOpen("hss", slf)

	s.hss.Drain()

	u := s.newUE(false, testue.Config{NoRegEvent: true})
	s.register(u)

	if got := slices.DeleteFunc(redirected.take(), func(c uint32) bool { return c == cx.CommandLocationInfo }); !slices.Equal(got, []uint32{cx.CommandUserAuthorization}) {
		t.Fatalf("the SLF redirected %v, want the first UAR alone: the next requests of the user follow the cached route", got)
	}

	got := slices.DeleteFunc(cxCommands(s.hss), func(c string) bool { return c == "LIR" })
	if want := []string{"UAR", "MAR", "UAR", "SAR " + cx.AssignmentRegistration.String()}; !slices.Equal(got, want) {
		t.Fatalf("the HSS got %v, want %v", got, want)
	}
}

// commandLog is the commands of the requests a fake peer received. An LIR names no user (TS 29.229 §6.1.4), so no
// redirect is cached for it.
type commandLog struct {
	mu       sync.Mutex
	commands []uint32
}

func (l *commandLog) add(c uint32) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.commands = append(l.commands, c)
}

func (l *commandLog) take() []uint32 {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := l.commands
	l.commands = nil

	return out
}
