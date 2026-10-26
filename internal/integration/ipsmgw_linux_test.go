//go:build linux && (amd64 || arm64)

package integration

import (
	"bytes"
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/sh"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/nas"
	"github.com/ellanetworks/core/nas/sms"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/testue"
)

const (
	coreHost  = "core.epc.mnc001.mcc001.3gppnetwork.org"
	coreRealm = "epc.mnc001.mcc001.3gppnetwork.org"

	serviceCentre = "15550000000"
	ipsmgwNumber  = "15550000100"
)

// fakeCore is the HSS of the IP-SM-GW over Sh and S6c, and its SMSC over SGd, as one Diameter peer: it records the
// Sh-Updates and the short messages submitted, answers the routing information of a user with an MME it cannot
// reach, and sends the IP-SM-GW the requests of the HSS and the SMS-GMSC.
type fakeCore struct {
	node      *diameter.Node
	updates   chan sh.ProfileUpdateRequest
	submitted chan sgd.MOForwardShortMessage
	reports   chan s6c.DeliveryReport
}

func (s *scene) fakeCore(imsi string) *fakeCore {
	s.t.Helper()

	f := &fakeCore{
		updates: make(chan sh.ProfileUpdateRequest, 8), submitted: make(chan sgd.MOForwardShortMessage, 8),
		reports: make(chan s6c.DeliveryReport, 8),
	}

	mux := diameter.NewMux()
	mux.Handle(sh.ApplicationID, sh.CommandProfileUpdate, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			pur, err := sh.ParseProfileUpdateRequest(req)
			if err != nil {
				return sh.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
			}

			f.updates <- pur

			ans, _ := sh.NewProfileUpdateAnswer(req, c.LocalIdentity(), sh.ProfileUpdate{Result: tgpp.Result{Code: diameter.ResultSuccess}})

			return ans
		}))
	mux.Handle(s6c.ApplicationID, s6c.CommandSendRoutingInfoForSM, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			ans, err := s6c.NewSendRoutingInfoForSMAnswer(req, c.LocalIdentity(), s6c.Routing{
				IMSI: imsi,
				ServingNodes: s6c.ServingNodes{Serving: &s6c.ServingNode{MME: &s6c.NodeAddress{
					Name: "mme.unreachable.example.org", Realm: "unreachable.example.org", Number: "15550000010",
				}}},
			}, true)
			if err != nil {
				return s6c.NewErrorAnswer(req, c.LocalIdentity(), err)
			}

			return ans
		}))
	mux.Handle(s6c.ApplicationID, s6c.CommandReportSMDeliveryStatus, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			if rep, err := s6c.ParseReportSMDeliveryStatusRequest(req); err == nil {
				f.reports <- rep
			}

			ans, _ := s6c.NewReportSMDeliveryStatusAnswer(req, c.LocalIdentity(), s6c.ReportResult{}, true)

			return ans
		}))
	mux.Handle(sgd.ApplicationID, sgd.CommandMOForwardShortMessage, diameter.HandlerFunc(
		func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			m, err := sgd.ParseMOForwardShortMessageRequest(req)
			if err != nil {
				return tgpp.NewErrorAnswer(req, c.LocalIdentity(), err)
			}

			f.submitted <- m

			ans, _ := sgd.NewMOForwardShortMessageAnswer(req, c.LocalIdentity(), []byte{0x01, 0x00})

			return ans
		}))

	node, addr := diametertest.Listen(s.t, diametertest.Config{
		Identity: diameter.Identity{
			OriginHost: coreHost, OriginRealm: coreRealm, HostIPAddresses: []netip.Addr{s.hss.Addr().Addr()},
			ProductName: "fake-core",
		},
		Peer: diameter.Peer{ID: "ims", Host: imsHost, Applications: []diameter.Application{
			{ID: sh.ApplicationID, VendorID: tgpp.VendorID},
			{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
			{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
		}},
		Handler: mux,
	})
	f.node = node

	id := s.addPeer(api.DiameterPeerParams{
		Host: coreHost, Address: addr.Addr().String(), Port: int(addr.Port()), Applications: []string{"sh", "s6c", "sgd"},
		ServiceCentres: []string{serviceCentre},
	})
	s.put("/api/v1/diameter/routes/sh", api.DiameterRouteParams{Realm: coreRealm})
	s.put("/api/v1/diameter/routes/s6c", api.DiameterRouteParams{Realm: coreRealm})
	s.put("/api/v1/ip-sm-gw", api.IPSMGWParams{Number: ipsmgwNumber})
	s.diameterOpen(id)

	return f
}

// send sends the IMS a request of the HSS or the SMS-GMSC, built of an envelope to the IMS.
func (f *fakeCore) send(t *testing.T, build func(tgpp.Envelope) (*diameter.Message, error)) *diameter.Message {
	t.Helper()

	req, err := build(tgpp.Envelope{
		SessionID: f.node.NewSessionID(), Origin: f.node.Identity(), DestinationHost: imsHost, DestinationRealm: domain,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ans, err := f.node.Send(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	return ans
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()

	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("no %s", what)
	}

	var zero T

	return zero
}

// smsIFCs are the iFCs of the IP-SM-GW of subscriber i (TS 23.204 §6.1, §6.3): its REGISTERs, of the MSISDN in the
// service information, and its MESSAGEs of short messages.
func (s *scene) smsIFCs(i int) {
	ipsmgw := "sip:ipsmgw." + domain
	msisdn := strings.TrimPrefix(subscriberAt(i).tel, "tel:+")

	reg := ifc(0, ipsmgw, cx.DefaultHandlingSessionContinued, methodIs("REGISTER"))
	reg.ApplicationServer.ServiceInfo = &msisdn
	reg.ApplicationServer.IncludeRegisterRequest = true
	reg.ApplicationServer.IncludeRegisterResponse = true

	s.setIFCs(i, reg, ifc(1, ipsmgw, cx.DefaultHandlingSessionContinued, methodIs("MESSAGE"),
		caseIs(cx.SessionCaseOriginatingRegistered), headerIs("Content-Type", `application/vnd\.3gpp\.sms`)))
}

// smsOf waits for a MESSAGE of a short message the UE received, and returns it and its RP message.
func smsOf(t *testing.T, u *testue.UE) (*sip.Request, sms.RPMessage) {
	t.Helper()

	for {
		e := recv(t, u.Events(), "MESSAGE to the UE")
		if e.Request == nil || e.Request.Method != "MESSAGE" {
			continue
		}

		rp, err := sms.ParseRP(e.Request.Content().Body, nas.DirectionDownlink)
		if err != nil {
			t.Fatalf("RP message %x: %v", e.Request.Content().Body, err)
		}

		return e.Request, rp
	}
}

// A UE registered for SMS over IP: the IP-SM-GW registers at the HSS for it (TS 24.341 §5.3.3.2); its short message
// goes through the S-CSCF to the IP-SM-GW by the iFC, then to the SMSC, and its submit report comes back through the
// S-CSCF (TS 23.204 §6.3); a short message of the SMS-GMSC reaches it over IMS, of the routing information the HSS
// forwarded, and its delivery report goes back to the SMS-GMSC (TS 23.204 §6.4, §6.5).
func TestSMSOverIP(t *testing.T) {
	s := newScene(t)
	sub := subscriberAt(0)
	msisdn := strings.TrimPrefix(sub.tel, "tel:+")

	core := s.fakeCore(sub.imsi)
	s.smsIFCs(0)

	u := s.newUE(false, testue.Config{Transport: sip.UDP})
	s.register(u)

	if pur := recv(t, core.updates, "Sh-Update"); pur.User.MSISDN != msisdn {
		t.Fatalf("Sh-Update of %+v, want the MSISDN %s", pur.User, msisdn)
	} else if data, err := sh.ParseShData(pur.UserData); err != nil || data.SMSRegistration.IPSMGWNumber != ipsmgwNumber {
		t.Fatalf("Sh-Update of %+v, %v", data, err)
	}

	t.Run("mobile originated", func(t *testing.T) {
		tpdu := []byte{0x01, 0x07, 0x0b, 0x91, 0x51, 0x55, 0x00, 0x00, 0x00, 0xf2, 0x00, 0x00, 0x02, 0xe8, 0x34}

		data, err := (&sms.RPData{
			Direction: nas.DirectionUplink, Reference: 9, Destination: sms.E164Address(serviceCentre), UserData: tpdu,
		}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}

		res, err := u.SMS().Send(s.ctx(), "tel:+"+serviceCentre, data)
		if err != nil || res.StatusCode != 202 {
			t.Fatalf("MESSAGE = %v, %v, want 202", res, err)
		}

		m := recv(t, core.submitted, "short message at the SMSC")
		if m.ServiceCentreAddress != serviceCentre || m.User.MSISDN != msisdn || !bytes.Equal(m.SMRPUI, tpdu) {
			t.Fatalf("OFR = %+v", m)
		}

		report, rp := smsOf(t, u)
		if ack, ok := rp.(*sms.RPAck); !ok || ack.Reference != 9 || report.Header.Get("In-Reply-To") != res.Header.CallID() {
			t.Fatalf("submit report %+v, In-Reply-To %q", rp, report.Header.Get("In-Reply-To"))
		}
	})

	t.Run("mobile terminated", func(t *testing.T) {
		ans := core.send(t, func(env tgpp.Envelope) (*diameter.Message, error) {
			return s6c.NewSendRoutingInfoForSMRequest(env, s6c.RoutingRequest{
				MSISDN: msisdn, ServiceCentreAddress: serviceCentre, MTI: s6c.SMRPMTIDeliver, SMEA: []byte{0x0b, 0x91},
			})
		})

		routing, err := s6c.ParseSendRoutingInfoForSMAnswer(ans)
		if err != nil || routing.Serving == nil || routing.Serving.IPSMGW == nil || routing.Serving.IPSMGW.Number != ipsmgwNumber {
			t.Fatalf("SRA = %+v, %v", routing, err)
		}

		deliver := []byte{
			0x04, 0x0b, 0x91, 0x51, 0x55, 0x00, 0x00, 0x00, 0xf2, 0x00, 0x00, 0x62, 0x90, 0x72, 0x11, 0x95,
			0x00, 0x00, 0x02, 0xe8, 0x34,
		}

		answered := make(chan *diameter.Message, 1)

		go func() {
			answered <- core.send(t, func(env tgpp.Envelope) (*diameter.Message, error) {
				return sgd.NewMTForwardShortMessageRequest(env, sgd.MTForwardShortMessage{
					IMSI: routing.IMSI, ServiceCentreAddress: serviceCentre, SMRPUI: deliver, DeliveryTimer: 20 * time.Second,
				})
			})
		}()

		req, rp := smsOf(t, u)

		data, ok := rp.(*sms.RPData)
		if !ok || !bytes.Equal(data.UserData, deliver) || data.Originator == nil || data.Originator.Digits != serviceCentre {
			t.Fatalf("short message %+v", rp)
		}

		if v := req.Header.Get("P-Asserted-Identity"); v != "<sip:ipsmgw."+domain+">" {
			t.Errorf("P-Asserted-Identity %q, want the IP-SM-GW", v)
		}

		ack, err := (&sms.RPAck{Direction: nas.DirectionUplink, Reference: data.Reference, UserData: []byte{0x00, 0x00}}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}

		if res, err := u.SMS().Send(s.ctx(), "tel:+"+serviceCentre, ack,
			sip.Field{Name: "In-Reply-To", Value: req.Header.CallID()}); err != nil || res.StatusCode != 202 {
			t.Fatalf("delivery report = %v, %v, want 202", res, err)
		}

		tfa, err := sgd.ParseMTForwardShortMessageAnswer(recv(t, answered, "TFA"))
		if err != nil || !bytes.Equal(tfa.SMRPUI, []byte{0x00, 0x00}) {
			t.Fatalf("TFA = %+v, %v", tfa, err)
		}
	})
}
