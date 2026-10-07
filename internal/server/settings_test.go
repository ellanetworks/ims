package server

import (
	"errors"
	"testing"
	"time"

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
