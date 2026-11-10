package server

import (
	"net/http"
	"net/netip"
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func hssPeer(id string, h *hsstest.HSS) settings.Peer {
	return settings.Peer{
		ID: id, Host: h.Host(), Address: h.Addr().Addr().String(), Port: int(h.Addr().Port()),
		Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationCx},
	}
}

func scscfNameOf(t *testing.T, n *clusterNode, label string) string {
	t.Helper()

	return n.srv.settings.Get().Operator.SCSCFName(label, int(sipListener(t, n.srv, roleSCSCF, loopback).Port()))
}

type twoNodes struct {
	hss  *hsstest.HSS
	a, b *clusterNode
}

func startTwoNodes(t *testing.T) twoNodes {
	t.Helper()

	hssA := hsstest.New(t, hsstest.Config{Realm: imsRealm, IMSHost: "ims-a." + imsRealm})
	hssA.SupportRestoration(true)
	hssB := hssA.ReplicaFor(t, "hss-b."+imsRealm, "ims-b."+imsRealm)

	for _, s := range []struct{ impi, tel string }{{aliceIMPI, aliceTel}, {bobIMPI, bobTel}, {carolIMPI, carolTel}} {
		hssA.Add(hsstest.Subscriber{
			IMPI: s.impi, IMSI: s.impi[:15], K: testK, OPc: testOPc, SQN: 32,
			IMPUs: []cx.ProfileIdentity{{Identity: "sip:" + s.impi}, {Identity: s.tel}},
		})
	}

	cfgA := clusterConfig(t, "ims-a")
	cfgA.SIP.Addresses = []netip.Addr{loopback}
	cfgA.Peers = []settings.Peer{hssPeer("hss-a", hssA), hssPeer("hss-b", hssB)}

	a := startNode(t, cfgA)
	a.waitStarted(t)

	token := a.mintToken(t)

	cfgB := joinerConfig(t, "ims-b")
	cfgB.SIP.Addresses = []netip.Addr{loopback}

	b := startNode(t, cfgB)
	b.waitStarted(t)

	if code := b.join(t, token); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	eventuallyFor(t, "a login on the joined node", func() bool {
		_, code := b.login(t, apitest.Email)
		return code == http.StatusOK
	})

	waitOpen(t, a.srv, "hss-a")
	waitOpen(t, b.srv, "hss-b")

	for _, pair := range []struct {
		from  *clusterNode
		to    *clusterNode
		label string
	}{{a, b, "ims-b"}, {b, a, "ims-a"}} {
		name, err := sip.ParseURI(scscfNameOf(t, pair.to, pair.label))
		if err != nil {
			t.Fatal(err)
		}

		eventuallyFor(t, "the S-CSCF of "+pair.label+" to resolve", func() bool {
			servers, err := pair.from.srv.peers.Resolve(t.Context(), name)
			return err == nil && len(servers) > 0 && servers[0].Addr.Port() == sipListener(t, pair.to.srv, roleSCSCF, loopback).Port()
		})
	}

	return twoNodes{hss: hssA, a: a, b: b}
}

func callFrom(t *testing.T, from *siptest.Peer, route, target string) *sip.Request {
	t.Helper()

	req := siptest.NewRequest("INVITE", target, sip.UDP, from.Addr())
	req.Header.Set("From", "<"+aliceTel+">;tag="+sip.NewTag())
	req.Header.Prepend("Route", route)
	req.Header.Add("P-Asserted-Identity", "<"+aliceTel+">")
	req.Header.Add("P-Preferred-Service", "urn:urn-7:3gpp-service.ims.icsi.mmtel")

	return req
}

func answerInvite(t *testing.T, p *siptest.Peer, code int) *sip.Request {
	t.Helper()

	got, f := p.RecvRequest()
	if got.Method != "INVITE" {
		t.Fatalf("got %q, want an INVITE", got.StartLine())
	}

	res := sip.NewResponse(got, code, "")
	_ = res.Header.SetToTag(sip.NewTag())
	p.Send(sip.UDP, f.Remote, res)

	return got
}

func TestCallsAcrossNodesAndRestoration(t *testing.T) {
	n := startTwoNodes(t)

	scscfA := sipListener(t, n.a.srv, roleSCSCF, loopback)
	scscfB := sipListener(t, n.b.srv, roleSCSCF, loopback)
	icscfB := sipListener(t, n.b.srv, roleICSCF, loopback)

	nameA, nameB := scscfNameOf(t, n.a, "ims-a"), scscfNameOf(t, n.b, "ims-b")

	bobAtA := newPeer(t, n.a.srv, netip.AddrPortFrom(loopback, 6201))
	carolAtA := newPeer(t, n.a.srv, netip.AddrPortFrom(loopback, 6202))
	alice := newPeer(t, n.b.srv, netip.AddrPortFrom(loopback, 6200))

	registerAtSCSCF(t, scscfA, bobAtA, bobIMPI, "sip:bob@"+loopback.String()+":7201")
	registerAtSCSCF(t, scscfA, carolAtA, carolIMPI, "sip:carol@"+loopback.String()+":7202")
	aliceRoute := registerAtSCSCF(t, scscfB, alice, aliceIMPI, "sip:alice@"+loopback.String()+":7200")

	for impi, want := range map[string]string{bobIMPI: nameA, carolIMPI: nameA, aliceIMPI: nameB} {
		if s, _ := n.hss.Subscriber(impi); s.ServerName != want {
			t.Fatalf("%s is served by %s, want %s", impi, s.ServerName, want)
		}
	}

	invite := callFrom(t, alice, aliceRoute, bobTel)
	alice.Send(sip.UDP, scscfB, invite)
	answerInvite(t, bobAtA, 486)

	if res := failedInvite(t, alice, scscfB, invite); res.StatusCode != 486 {
		t.Fatalf("call across nodes = %q, want 486 from bob", res.StartLine())
	}

	n.a.stop()

	invite = callFrom(t, alice, aliceRoute, carolTel)
	alice.Send(sip.UDP, scscfB, invite)

	if res := failedInvite(t, alice, scscfB, invite); res.StatusCode < 400 {
		t.Fatalf("call to a phone of the stopped node = %q, want a failure", res.StartLine())
	}

	if s, _ := n.hss.Subscriber(carolIMPI); s.ServerName != nameB || s.State != hsstest.Unregistered {
		t.Fatalf("carol = %s at %s, want unregistered at %s", s.State, s.ServerName, nameB)
	}

	bobAtB := newPeer(t, n.b.srv, netip.AddrPortFrom(loopback, 6201))
	registerAtSCSCF(t, icscfB, bobAtB, bobIMPI, "sip:bob@"+loopback.String()+":7201")

	if s, _ := n.hss.Subscriber(bobIMPI); s.ServerName != nameB || s.State != hsstest.Registered {
		t.Fatalf("bob = %s at %s, want registered at %s", s.State, s.ServerName, nameB)
	}

	invite = callFrom(t, alice, aliceRoute, bobTel)
	alice.Send(sip.UDP, scscfB, invite)
	answerInvite(t, bobAtB, 486)

	if res := failedInvite(t, alice, scscfB, invite); res.StatusCode != 486 {
		t.Fatalf("call after bob moved = %q, want 486 from bob", res.StartLine())
	}
}
