package server

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/mcptt"
	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

const (
	mcpttPolice = "sip:police@mcptt.example.com"
	mcpttWait   = 20 * time.Second
)

func freeUDPBlock(t *testing.T, a netip.Addr, n int) int {
	t.Helper()

	for range 100 {
		base := unusedPort(t) &^ 1
		if base+n > 65535 {
			continue
		}

		free := true

		for p := base; p < base+n && free; p++ {
			free = !udpInUse(netip.AddrPortFrom(a, uint16(p)))
		}

		if free {
			return base
		}
	}

	t.Fatal("no block of free UDP ports")

	return 0
}

func mcpttRelayConfig(t *testing.T, a netip.Addr) *config.MCPTT {
	t.Helper()

	legs := freeUDPBlock(t, a, 16)

	control := unusedPort(t)
	for control >= legs && control < legs+16 {
		control = unusedPort(t)
	}

	return &config.MCPTT{
		Mode: config.MCPTTModeIMS, Port: unusedPort(t),
		Media: config.MCPTTMedia{Address: a, MinPort: legs, MaxPort: legs + 15, ControlPort: control},
	}
}

// TS 29.228 clause B.2.2
func mcpttIFCs() []cx.InitialFilterCriteria {
	server := "sip:" + mcptt.Host(imsRealm)
	register, orig := "REGISTER", cx.SessionCaseOriginatingRegistered
	header, icsi := "^P-Asserted-Service$", "^urn:urn-7:3gpp-service\\.ims\\.icsi\\.mcptt$"
	continued, terminated := cx.DefaultHandlingSessionContinued, cx.DefaultHandlingSessionTerminated

	return []cx.InitialFilterCriteria{
		{
			Priority: 0,
			Trigger: &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{
				{Groups: []int32{0}, Method: &register},
			}},
			ApplicationServer: cx.ApplicationServer{
				ServerName: server, DefaultHandling: &continued, IncludeRegisterRequest: true, IncludeRegisterResponse: true,
			},
		},
		{
			Priority: 1,
			Trigger: &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{
				{Groups: []int32{0}, SessionCase: &orig},
				{Groups: []int32{1}, SIPHeader: &cx.HeaderMatch{Header: header, Content: &icsi}},
			}},
			ApplicationServer: cx.ApplicationServer{ServerName: server, DefaultHandling: &terminated},
		},
	}
}

type mcpttUE struct {
	name   string
	route  string
	events chan client.Event

	*client.Client
}

func newMCPTTUE(t *testing.T, n *clusterNode, ip netip.Addr, name, impi string, i int) *mcpttUE {
	t.Helper()

	scscf := sipListener(t, n.srv, roleSCSCF, ip)
	port := uint16(6300 + 2*i)
	addr := netip.AddrPortFrom(ip, port)
	contact := sip.URI{Scheme: "sip", User: name, Host: ip.String(), Port: port}
	impu := "sip:" + impi
	u := &mcpttUE{name: name, events: make(chan client.Event, 1024)}

	agent, err := ua.New(ua.Config{
		Transaction: transaction.Config{Network: n.srv.ases.network},
		Contact:     sip.Address{URI: contact},
		Prepare: func(_ context.Context, req *sip.Request) error {
			req.Flow = sip.Flow{Transport: sip.UDP, Local: addr, Remote: scscf}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, addr).String())

			return nil
		},
		OnInvite: func(_ context.Context, r *ua.Request) ua.Handler { return u.OnInvite(r) },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := agent.Layer().Serve(addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = agent.Shutdown(ctx)
	})

	psi, err := sip.ParseURI(mcptt.System(imsRealm).ParticipatingPSI)
	if err != nil {
		t.Fatal(err)
	}

	ports := freeUDPBlock(t, ip, 8)

	c, err := client.New(client.Config{
		Agent: agent,
		Originate: func(req *sip.Request) error {
			req.Header.Set("Route", u.route)
			req.Header.Set("From", "<"+impu+">")
			req.Header.Set("P-Asserted-Identity", "<"+impu+">")

			return nil
		},
		IMPU:             mustParseURI(t, impu),
		MCPTTID:          mustParseURI(t, "sip:"+name+"@mcptt.example.com"),
		ClientID:         fmt.Sprintf("urn:uuid:00000000-0000-1000-8000-%012d", i+1),
		Contact:          client.FixedContact(sip.Address{URI: contact}),
		ParticipatingPSI: psi,
		Media:            media.UDP{},
		Addr:             ip,
		MinPort:          uint16(ports),
		MaxPort:          uint16(ports + 7),
		Floor:            floor.ParticipantTimers{T100: time.Second, T101: time.Second},
		OnEvent: func(e client.Event) {
			select {
			case u.events <- e:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	u.Client = c

	pcscf := newPeer(t, n.srv, netip.AddrPortFrom(ip, port+1))
	u.route = registerThrough(t, scscf, pcscf, impi, func(r *sip.Request) {
		r.Header.Set("Contact", identity.WithFeatures(sip.Address{URI: contact}, client.Features()).String())
		r.Header.Add("Path", "<sip:term@"+addr.String()+";lr>")
		r.SetBodies(c.RegisterBodies()...)
	})

	return u
}

func mustParseURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

func (u *mcpttUE) await(t *testing.T, what string, match func(client.Event) bool) client.Event {
	t.Helper()

	deadline := time.After(mcpttWait)

	var seen []string

	for {
		select {
		case e := <-u.events:
			if match(e) {
				return e
			}

			seen = append(seen, e.Kind.String())
		case <-deadline:
			t.Fatalf("%s: no %s, events %v", u.name, what, seen)
		}
	}
}

func (u *mcpttUE) awaitKind(t *testing.T, k client.EventKind) {
	t.Helper()

	u.await(t, k.String(), func(e client.Event) bool { return e.Kind == k })
}

func (u *mcpttUE) established(t *testing.T) {
	t.Helper()

	e := u.await(t, "the outcome of the call", func(e client.Event) bool {
		return e.Kind == client.EventEstablished || e.Kind == client.EventFailed
	})

	if e.Kind != client.EventEstablished || e.Response == nil || e.Response.StatusCode != 200 {
		var res string
		if e.Response != nil {
			res = e.Response.StartLine() + " " + fmt.Sprint(e.Response.Header.Values("Warning"))
		}

		t.Fatalf("%s: call %s %s %v", u.name, e.Kind, res, e.Err)
	}
}

func (u *mcpttUE) awaitFloor(t *testing.T, k floor.NoticeKind) {
	t.Helper()

	u.await(t, fmt.Sprintf("floor notice %d", k), func(e client.Event) bool { return e.Kind == client.EventFloor && e.Floor.Kind == k })
}

type mcpttCluster3 struct {
	nodes [3]*clusterNode
	ips   [3]netip.Addr
}

func startMCPTTCluster(t *testing.T) mcpttCluster3 {
	t.Helper()

	labels := [3]string{"ims-a", "ims-b", "ims-c"}
	ips := [3]netip.Addr{loopback, netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.3")}

	hss := hsstest.New(t, hsstest.Config{Realm: imsRealm, IMSHost: labels[0] + "." + imsRealm})
	replicas := [3]*hsstest.HSS{hss, hss.ReplicaFor(t, "hss-b."+imsRealm, labels[1]+"."+imsRealm), hss.ReplicaFor(t, "hss-c."+imsRealm, labels[2]+"."+imsRealm)}

	for _, impi := range []string{aliceIMPI, bobIMPI, carolIMPI} {
		hss.Add(hsstest.Subscriber{
			IMPI: impi, IMSI: impi[:15], K: testK, OPc: testOPc, SQN: 32,
			IMPUs: []cx.ProfileIdentity{{Identity: "sip:" + impi}}, IFCs: mcpttIFCs(),
		})
	}

	var out mcpttCluster3

	out.ips = ips

	for i := range 3 {
		cfg := clusterConfig(t, labels[i])
		if i > 0 {
			cfg = joinerConfig(t, labels[i])
		} else {
			cfg.Peers = []settings.Peer{hssPeer("hss-a", replicas[0]), hssPeer("hss-b", replicas[1]), hssPeer("hss-c", replicas[2])}
		}

		cfg.SIP.Addresses = []netip.Addr{ips[i]}
		cfg.MCPTT = mcpttRelayConfig(t, ips[i])
		cfg.MCPTTOrphanAfter = time.Second

		n := startNode(t, cfg)
		n.waitStarted(t)

		if i > 0 {
			if code := n.join(t, out.nodes[0].mintToken(t)); code != http.StatusOK {
				t.Fatalf("POST /api/v1/cluster/join = %d", code)
			}

			eventuallyFor(t, "a login on the joined node", func() bool {
				_, code := n.login(t, apitest.Email)
				return code == http.StatusOK
			})
		}

		out.nodes[i] = n
	}

	for i, n := range out.nodes {
		waitOpen(t, n.srv, []string{"hss-a", "hss-b", "hss-c"}[i])
	}

	for _, n := range out.nodes[1:] {
		eventuallyFor(t, "a voter of the joined node", func() bool {
			m, ok := out.nodes[0].member(t, n.srv.node.ID())
			return ok && m.Suffrage == "voter"
		})
	}

	return out
}

func configureMCPTTGroup(t *testing.T, n *clusterNode) {
	t.Helper()

	live := n.srv.mcptt.Load().Settings()
	ctx := t.Context()

	var entries []mcpttsettings.Entry

	implicit := mcpttsettings.Implicit{}

	for _, name := range []string{"alice", "bob", "carol"} {
		impi := map[string]string{"alice": aliceIMPI, "bob": bobIMPI, "carol": carolIMPI}[name]
		id := "sip:" + name + "@mcptt.example.com"

		if _, err := live.CreateUser(ctx, mcpttsettings.User{MCPTTUserID: id, IMPUs: []string{"sip:" + impi}}); err != nil {
			t.Fatal(err)
		}

		entries = append(entries, mcpttsettings.Entry{URI: id})
		implicit[id] = true
	}

	if _, err := live.CreateGroup(ctx, mcpttsettings.Group{
		URI: mcpttPolice, InviteMembers: true, TNG3: 10 * time.Minute, List: mcpttsettings.List{Entries: entries},
	}, implicit); err != nil {
		t.Fatal(err)
	}
}

func groupCallOwner(t *testing.T, n *clusterNode) string {
	t.Helper()

	owner, ok, err := n.srv.mcpttCluster.GroupCall(t.Context(), mustParseURI(t, mcpttPolice))
	if err != nil || !ok {
		return ""
	}

	return owner
}

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 10.1.1.4.1.1
// TS 24.379 clause 10.1.1.3.2
// TS 23.380 clause 1
func TestMCPTTGroupCallFailoverAcrossThreeNodes(t *testing.T) {
	c := startMCPTTCluster(t)
	a, b, cn := c.nodes[0], c.nodes[1], c.nodes[2]
	idA, idB, idC := a.srv.node.ID(), b.srv.node.ID(), cn.srv.node.ID()
	police := mustParseURI(t, mcpttPolice)

	configureMCPTTGroup(t, a)

	for _, n := range c.nodes {
		eventuallyFor(t, "the MCPTT group on every node", func() bool {
			s := n.srv.mcptt.Load().Settings().Get()
			return len(s.Users) == 3 && len(s.Groups) == 1
		})
	}

	eventuallyFor(t, "the MCPTT routes between the nodes", func() bool {
		for _, from := range c.nodes {
			for _, to := range c.nodes {
				if _, _, ok := from.srv.mcpttCluster.Route(to.srv.node.ID()); ok != (from != to) {
					return false
				}
			}
		}

		return true
	})

	alice := newMCPTTUE(t, a, c.ips[0], "alice", aliceIMPI, 0)
	bob := newMCPTTUE(t, b, c.ips[1], "bob", bobIMPI, 1)
	carol := newMCPTTUE(t, cn, c.ips[2], "carol", carolIMPI, 2)

	eventuallyFor(t, "the affiliations of the three nodes", func() bool {
		rows, err := b.srv.mcpttCluster.GroupAffiliations(t.Context(), police)
		if err != nil {
			return false
		}

		var nodes []string
		for _, r := range rows {
			nodes = append(nodes, r.Node)
		}

		want := []string{idA, idB, idC}

		slices.Sort(nodes)
		slices.Sort(want)

		return slices.Equal(nodes, want)
	})

	ca, err := alice.Call(context.Background(), police)
	if err != nil {
		t.Fatal(err)
	}

	alice.established(t)

	bob.awaitKind(t, client.EventIncoming)
	bob.awaitKind(t, client.EventEstablished)
	carol.awaitKind(t, client.EventIncoming)
	carol.awaitKind(t, client.EventEstablished)

	eventuallyFor(t, "the claim of node A, of the initiator, on every node", func() bool {
		return groupCallOwner(t, a) == idA && groupCallOwner(t, b) == idA && groupCallOwner(t, cn) == idA
	})

	ca.PressPTT()
	alice.awaitFloor(t, floor.NoticeGranted)
	bob.awaitFloor(t, floor.NoticeTaken)
	ca.ReleasePTT()

	a.stop()

	bob.awaitKind(t, client.EventEnded)
	carol.awaitKind(t, client.EventEnded)

	eventuallyFor(t, "the release of the call of the stopped node", func() bool {
		return groupCallOwner(t, b) == "" && groupCallOwner(t, cn) == ""
	})

	eventuallyFor(t, "a claim left by the stopped node", func() bool {
		_, err := b.srv.database.ClaimMCPTTGroupCall(t.Context(), police, idA, "lost")
		return err == nil
	})

	eventuallyFor(t, "the release of the claim of the lost node by the leader", func() bool {
		return groupCallOwner(t, b) == "" && groupCallOwner(t, cn) == ""
	})

	cb, err := bob.Call(context.Background(), police)
	if err != nil {
		t.Fatal(err)
	}

	bob.established(t)

	carol.awaitKind(t, client.EventIncoming)
	carol.awaitKind(t, client.EventEstablished)

	eventuallyFor(t, "the claim of node B, of the new initiator, on node C", func() bool {
		return groupCallOwner(t, b) == idB && groupCallOwner(t, cn) == idB
	})

	cb.PressPTT()
	bob.awaitFloor(t, floor.NoticeGranted)
	carol.awaitFloor(t, floor.NoticeTaken)
	cb.ReleasePTT()

	cb.Leave(context.Background())
	bob.awaitKind(t, client.EventEnded)
	carol.awaitKind(t, client.EventEnded)
}
