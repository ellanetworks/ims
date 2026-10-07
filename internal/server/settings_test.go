package server

import (
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/settings"
)

func TestOperatorChangeRenamesTheIMS(t *testing.T) {
	srv := startIMS(t, testConfig(t))
	before := srv.core.Load()

	op := srv.settings.Get().Operator
	op.MNC = "02"

	if err := srv.settings.UpdateOperator(t.Context(), op); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	eventually(t, "the core under the new identity", func() bool {
		c := srv.core.Load()
		return c != nil && c != before
	})

	if got, want := getDiameter(t, srv).Host, "ims.ims.mnc002.mcc001.3gppnetwork.org"; got != want {
		t.Fatalf("Diameter host = %q, want %q", got, want)
	}

	c := srv.core.Load()
	if len(c.sip.Listeners()) == 0 {
		t.Fatal("no SIP listeners after the restart")
	}

	for _, alias := range []string{"ims.mnc002.mcc001.3gppnetwork.org", "scscf.ims.mnc002.mcc001.3gppnetwork.org"} {
		if !c.sip.placeholder.aliases[alias] {
			t.Fatalf("SIP aliases %v without %s", c.sip.placeholder.aliases, alias)
		}
	}

	if c.sip.placeholder.aliases["ims.mnc001.mcc001.3gppnetwork.org"] {
		t.Fatal("the old home domain is still an alias")
	}
}

func TestNumberingChangeKeepsTheCore(t *testing.T) {
	srv := startIMS(t, testConfig(t))
	before := srv.core.Load()

	op := srv.settings.Get().Operator
	op.Numbering = settings.Numbering{CountryCode: "44", NationalPrefix: "0", InternationalPrefix: "00"}

	if err := srv.settings.UpdateOperator(t.Context(), op); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	if got := srv.numbering(); got.CountryCode != "44" || got.NationalPrefix != "0" || got.InternationalPrefix != "00" {
		t.Fatalf("numbering = %+v, want the new plan", got)
	}

	// The core follows changes asynchronously: give it the time to restart, if it wrongly would.
	time.Sleep(200 * time.Millisecond)

	if srv.core.Load() != before {
		t.Fatal("a numbering change restarted the core")
	}
}

func TestPeerCreatedAtRuntime(t *testing.T) {
	hss := newFakePeer(t, "hss.ims.mnc001.mcc001.3gppnetwork.org", imsRealm, settings.ApplicationCx)

	cfg := testConfig(t)
	cfg.Peers = nil

	srv := startIMS(t, cfg)

	if peers := getPeers(t, srv); len(peers) != 0 {
		t.Fatalf("peers = %+v, want none", peers)
	}

	p := hss.config("")

	created, err := srv.settings.CreatePeer(t.Context(), p)
	if err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	waitOpen(t, srv, created.ID)

	if err := srv.settings.DeletePeer(t.Context(), created.ID); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}

	eventually(t, "the HSS to be dropped", func() bool { return len(coreView{srv}.Peers()) == 0 })
}

func TestPolicyChangedAtRuntime(t *testing.T) {
	pcrf := newFakePeer(t, "pcrf.epc.mnc001.mcc001.3gppnetwork.org", "epc.mnc001.mcc001.3gppnetwork.org", settings.ApplicationRx)

	cfg := testConfig(t)
	cfg.Peers = append(cfg.Peers, pcrf.config("pcrf"))
	cfg.Peers[0].Applications = []settings.Application{settings.ApplicationCx}

	srv := startIMS(t, cfg)

	if got := (coreView{srv}).PolicyStatus().Interface; got != "" {
		t.Fatalf("policy function = %q, want none", got)
	}

	if err := srv.settings.UpdatePolicy(t.Context(), settings.Policy{Interface: settings.PolicyRx}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	eventually(t, "the Rx policy function", func() bool { return coreView{srv}.PolicyStatus().Interface == "rx" })
}

func TestN5PolicyAgainstTheConfigurationFile(t *testing.T) {
	srv := startIMS(t, testConfig(t))

	err := srv.settings.UpdatePolicy(t.Context(), settings.Policy{Interface: settings.PolicyN5, PCFURI: "http://127.0.0.1:7777"})
	if !errors.Is(err, settings.ErrInvalid) || err.Error() != "n5 requires n5 in the configuration file" {
		t.Fatalf("UpdatePolicy = %v, want it rejected", err)
	}
}

func TestN5PolicyOutOfTheConfigurationFile(t *testing.T) {
	// A policy the configuration file no longer allows leaves the P-CSCF without a policy function, and its API up
	// to fix it.
	cfg := testConfig(t)
	cfg.Policy = settings.Policy{Interface: settings.PolicyN5, PCFURI: "http://127.0.0.1:7777"}

	srv := startIMS(t, cfg)

	if got := (coreView{srv}).PolicyStatus().Interface; got != "" {
		t.Fatalf("policy function = %q, want none", got)
	}

	if err := srv.settings.UpdatePolicy(t.Context(), settings.Policy{Interface: settings.PolicyNone}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
}

func TestPeerMovedWithoutRestart(t *testing.T) {
	const host = "hss.ims.mnc001.mcc001.3gppnetwork.org"

	old := newFakePeer(t, host, imsRealm, settings.ApplicationCx)
	moved := newFakePeer(t, host, imsRealm, settings.ApplicationCx)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(old.config("hss"))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "hss")

	before := srv.core.Load()

	if err := srv.settings.UpdatePeer(t.Context(), moved.config("hss")); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}

	eventually(t, "the HSS at its new address", func() bool { return moved.sawState(diameter.PeerOpen) })

	if c := srv.core.Load(); c.node != before.node || c.sip != before.sip {
		t.Fatal("moving a peer restarted the core")
	}
}

func TestCoreRetriedAfterAFailedRestart(t *testing.T) {
	notify, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	cfg.N5 = &config.N5{Notify: config.N5Notify{Address: loopback, Port: notify.Addr().(*net.TCPAddr).Port}}

	srv := startIMS(t, cfg)

	// The notification port is taken, which no check of the settings foresees: the core fails to start.
	if err := srv.settings.UpdatePolicy(t.Context(), settings.Policy{Interface: settings.PolicyN5, PCFURI: "http://127.0.0.1:7777"}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	eventually(t, "the core to fail", func() bool { return srv.core.Load() == nil })

	if err := (coreView{srv}).Reauthenticate(t.Context(), "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"); !errors.Is(err, api.ErrUnavailable) {
		t.Fatalf("Reauthenticate without a core = %v, want %v", err, api.ErrUnavailable)
	}

	_ = notify.Close()

	eventually(t, "the core to be retried", func() bool {
		c := srv.core.Load()
		return c != nil && c.policy.PolicyStatus().Interface == "n5"
	})
}

func TestAPIDuringRestarts(t *testing.T) {
	srv := startIMS(t, testConfig(t))

	done := make(chan struct{})

	go func() {
		defer close(done)

		for range 20 {
			for _, path := range []string{"/api/v1/diameter/peers", "/api/v1/diameter", "/api/v1/policy", "/api/v1/sip"} {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+srv.APIAddr().String()+path, nil)
				if err != nil {
					t.Error(err)
					return
				}

				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Errorf("GET %s: %v", path, err)
					return
				}

				_ = res.Body.Close()

				if res.StatusCode != http.StatusOK {
					t.Errorf("GET %s = %d during a restart", path, res.StatusCode)
				}
			}
		}
	}()

	op := srv.settings.Get().Operator

	for _, mnc := range []string{"02", "03", "01"} {
		op.MNC = mnc

		if err := srv.settings.UpdateOperator(t.Context(), op); err != nil {
			t.Fatalf("UpdateOperator: %v", err)
		}
	}

	<-done

	eventually(t, "the core under the last identity", func() bool {
		c := srv.core.Load()
		return c != nil && c.settings.Operator.MNC == "01"
	})
}
