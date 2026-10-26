package server

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/sh"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/ipsmgw"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
)

const smscRealm = "epc.mnc001.mcc001.3gppnetwork.org"

// A short message goes to the SMSC of its service centre address, at its host and realm (TS 29.338 §6.3.1.1),
// whose answer is final.
func TestSMSCForward(t *testing.T) {
	report := []byte{0x01, 0x00}

	var got *diameter.Message

	smsc := newFakePeerWithHandler(t, "smsc.epc.mnc001.mcc001.3gppnetwork.org", smscRealm,
		diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			got = req

			ans, err := sgd.NewMOForwardShortMessageAnswer(req, c.LocalIdentity(), report)
			if err != nil {
				t.Error(err)
			}

			return ans
		}), settings.ApplicationSGd)

	cfg := testConfig(t)
	p := smsc.config("smsc")
	p.ServiceCentres = []string{"15550000000"}

	// A first SMSC, never connected: the message goes to the next.
	down := settings.Peer{
		ID: "down", Host: "down.epc.mnc001.mcc001.3gppnetwork.org", Address: loopback, Port: unusedPort(t),
		Transport: settings.TransportTCP, Applications: []settings.Application{settings.ApplicationSGd}, Priority: 1,
		ServiceCentres: []string{"15550000000"},
	}
	cfg.Peers = append(cfg.Peers, down, p)

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "smsc")

	c := smscClient{srv}
	if !c.Any() {
		t.Fatal("no SMSC")
	}

	m := sgd.MOForwardShortMessage{
		ServiceCentreAddress: "15550000000", User: tgpp.UserIdentifier{MSISDN: "15551230001"}, SMRPUI: []byte{0x01, 0x07},
	}

	ui, err := c.Forward(t.Context(), m)
	if err != nil || !bytes.Equal(ui, report) {
		t.Fatalf("Forward = %x, %v", ui, err)
	}

	env := tgpp.ParseEnvelope(got)
	if env.DestinationHost != "smsc.epc.mnc001.mcc001.3gppnetwork.org" || env.DestinationRealm != smscRealm {
		t.Fatalf("OFR to %s in %s", env.DestinationHost, env.DestinationRealm)
	}

	m.ServiceCentreAddress = "15559999999"
	if _, err := c.Forward(t.Context(), m); !errors.Is(err, ipsmgw.ErrUnknownServiceCentre) {
		t.Fatalf("Forward to an unknown service centre = %v", err)
	}
}

// The IP-SM-GW registers at the HSS by an Sh-Update of SMSRegistrationInfo, of the MSISDN, else of the IMPU (TS
// 29.328 §6.1.2.1 step 4b).
func TestShUpdate(t *testing.T) {
	var got []sh.ProfileUpdateRequest

	hss := newFakePeerWithHandler(t, "hss.epc.mnc001.mcc001.3gppnetwork.org", imsRealm,
		diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			pur, err := sh.ParseProfileUpdateRequest(req)
			if err != nil {
				t.Error(err)
			}

			got = append(got, pur)

			ans, err := sh.NewProfileUpdateAnswer(req, c.LocalIdentity(), sh.ProfileUpdate{Result: tgpp.Result{Code: diameter.ResultSuccess}})
			if err != nil {
				t.Error(err)
			}

			return ans
		}), settings.ApplicationCx, settings.ApplicationSh)

	cfg := testConfig(t)
	cfg.Peers = seedPeers(hss.config("hss"))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "hss")

	c := shClient{srv}
	impu, _ := sip.ParseURI("sip:alice@" + imsRealm)

	if err := c.UpdateSMSRegistration(t.Context(), ipsmgw.User{MSISDN: "15551230001", IMPU: impu}, "15550000100"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.UpdateSMSRegistration(t.Context(), ipsmgw.User{IMPU: impu}, ""); err != nil {
		t.Fatalf("deregister: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("Sh-Updates = %+v", got)
	}

	for i, want := range []struct {
		user   sh.UserIdentity
		number string
	}{
		{sh.UserIdentity{MSISDN: "15551230001"}, "15550000100"},
		{sh.UserIdentity{PublicIdentity: "sip:alice@" + imsRealm}, ""},
	} {
		data, err := sh.ParseShData(got[i].UserData)
		if err != nil || got[i].User != want.user || data.SMSRegistration == nil ||
			data.SMSRegistration.IPSMGWNumber != want.number ||
			len(got[i].DataReferences) != 1 || got[i].DataReferences[0] != sh.DataSMSRegistrationInfo {
			t.Fatalf("Sh-Update %d = %+v %+v, %v", i, got[i], data, err)
		}
	}
}

// A short message goes to the MME or SMSF the HSS names, a peer of its host, of the realm the HSS gave.
func TestNodesForward(t *testing.T) {
	var got *diameter.Message

	mme := newFakePeerWithHandler(t, "core.epc.mnc001.mcc001.3gppnetwork.org", smscRealm,
		diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			got = req

			ans, err := sgd.NewMTForwardShortMessageAnswer(req, c.LocalIdentity(), []byte{0x00, 0x00})
			if err != nil {
				t.Error(err)
			}

			return ans
		}), settings.ApplicationSGd)

	cfg := testConfig(t)
	cfg.Peers = append(cfg.Peers, mme.config("core"))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "core")

	ui, err := nodesClient{srv}.Forward(t.Context(), mme.host, smscRealm, sgd.MTForwardShortMessage{
		IMSI: "001010000000001", ServiceCentreAddress: "15550000000", SMRPUI: []byte{0x04}, MMENumberForMTSMS: "15550000010",
	})
	if err != nil || !bytes.Equal(ui, []byte{0x00, 0x00}) {
		t.Fatalf("Forward = %x, %v", ui, err)
	}

	if env := tgpp.ParseEnvelope(got); env.DestinationHost != mme.host || env.DestinationRealm != smscRealm {
		t.Fatalf("TFR to %s in %s", env.DestinationHost, env.DestinationRealm)
	}

	if _, err := (nodesClient{srv}).Forward(t.Context(), "unknown.example.org", smscRealm, sgd.MTForwardShortMessage{
		IMSI: "001010000000001", ServiceCentreAddress: "15550000000", SMRPUI: []byte{0x04},
	}); err == nil {
		t.Fatal("Forward to a host of no peer succeeded")
	}
}
