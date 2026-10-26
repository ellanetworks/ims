package ipsmgw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/nas"
	"github.com/ellanetworks/core/nas/sms"
	"github.com/ellanetworks/ims/sip"
)

// fakeRouter is the HSS of the IP-SM-GW over S6c.
type fakeRouter struct {
	mu       sync.Mutex
	requests []s6c.RoutingRequest
	routing  s6c.Routing
	err      error
	reports  []s6c.DeliveryReport
	alert    s6c.ServingNodes
}

func (r *fakeRouter) SendRoutingInfoForSM(_ context.Context, req s6c.RoutingRequest) (s6c.Routing, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.requests = append(r.requests, req)

	return r.routing, r.err
}

func (r *fakeRouter) ReportSMDeliveryStatus(_ context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reports = append(r.reports, rep)

	return s6c.ReportResult{ServingNodes: r.alert}, nil
}

type fakeAnswer struct {
	ui  []byte
	err error
}

// fakeNodes are the MMEs and SMSFs of the IP-SM-GW over SGd, by host.
type fakeNodes struct {
	mu        sync.Mutex
	answers   map[string]fakeAnswer
	forwarded []sgd.MTForwardShortMessage
	hosts     []string
}

func (n *fakeNodes) Forward(_ context.Context, host, _ string, m sgd.MTForwardShortMessage) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.forwarded = append(n.forwarded, m)
	n.hosts = append(n.hosts, host)

	a, ok := n.answers[host]
	if !ok {
		return nil, fmt.Errorf("not connected")
	}

	return a.ui, a.err
}

var ims = diameter.Identity{OriginHost: "ims." + home, OriginRealm: home}

const smsc = "smsc.epc.mnc001.mcc001.3gppnetwork.org"

func mmeRouting() s6c.Routing {
	return s6c.Routing{
		IMSI: "001010000000001",
		ServingNodes: s6c.ServingNodes{Serving: &s6c.ServingNode{MME: &s6c.NodeAddress{
			Name: "core.epc.mnc001.mcc001.3gppnetwork.org", Realm: "epc.mnc001.mcc001.3gppnetwork.org", Number: "15550000010",
		}}},
		MWDStatus: s6c.MWDStatusMNRF,
	}
}

func envelope() tgpp.Envelope {
	return tgpp.Envelope{
		SessionID: "smsc;1", Origin: diameter.Identity{OriginHost: smsc, OriginRealm: "epc.mnc001.mcc001.3gppnetwork.org"},
		DestinationHost: ims.OriginHost, DestinationRealm: home,
	}
}

// routingInfo sends the IP-SM-GW the Send Routing Info for SM of the SMS-GMSC for the MSISDN of serviceInfo, and
// returns its answer.
func (w *world) routingInfo() *diameter.Message {
	w.t.Helper()

	req, err := s6c.NewSendRoutingInfoForSMRequest(envelope(), s6c.RoutingRequest{
		MSISDN: "15551230001", ServiceCentreAddress: "15550000000", MTI: s6c.SMRPMTIDeliver, SMEA: []byte{0x0b, 0x91},
	})
	if err != nil {
		w.t.Fatal(err)
	}

	return w.a.RoutingInfo(w.t.Context(), ims, req)
}

// forward sends the IP-SM-GW the MT Forward Short Message of the SMS-GMSC to corr, and returns a channel of its
// answer, which waits for the delivery.
func (w *world) forward(corr string, timer time.Duration) <-chan *diameter.Message {
	w.t.Helper()

	req, err := sgd.NewMTForwardShortMessageRequest(envelope(), sgd.MTForwardShortMessage{
		IMSI: corr, ServiceCentreAddress: "15550000000", SMRPUI: []byte{0x04, 0x02}, DeliveryTimer: timer,
	})
	if err != nil {
		w.t.Fatal(err)
	}

	ch := make(chan *diameter.Message, 1)

	go func() { ch <- w.a.ForwardShortMessage(context.Background(), ims, req) }()

	return ch
}

// available registers alice, of the MSISDN of serviceInfo, able to receive short messages over IP.
func (w *world) available() {
	w.t.Helper()

	impu := "sip:alice@" + home

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(w.t, update{msisdn: "15551230001", number: "15550000100"})
}

// delivered takes the MESSAGE of a short message delivered over IMS, answers it code, and returns it and its RP-DATA.
func (w *world) delivered(code int) (*sip.Request, *sms.RPData) {
	w.t.Helper()

	req, _ := w.scscf.RecvRequest()
	if req.Method != "MESSAGE" {
		w.t.Fatalf("got %s, want a MESSAGE", req.Method)
	}

	w.scscf.Send(sip.UDP, w.as, sip.NewResponse(req, code, ""))

	rp, err := sms.ParseRP(req.Content().Body, nas.DirectionDownlink)
	data, ok := rp.(*sms.RPData)

	if err != nil || !ok {
		w.t.Fatalf("RPDU %x: %v", req.Content().Body, err)
	}

	return req, data
}

// report sends the delivery report rp of the UE, of an In-Reply-To of callID if not "".
func (w *world) report(rp sms.RPMessage, callID string) {
	w.t.Helper()

	body, err := rp.MarshalBinary()
	if err != nil {
		w.t.Fatal(err)
	}

	var extra []string
	if callID != "" {
		extra = append(extra, "In-Reply-To: "+callID)
	}

	if res := w.message(ContentType, body, extra...); res.StatusCode != 202 {
		w.t.Fatalf("delivery report = %d", res.StatusCode)
	}
}

func answerOf(t *testing.T, ch <-chan *diameter.Message) *diameter.Message {
	t.Helper()

	select {
	case ans := <-ch:
		return ans
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
		return nil
	}
}

// The IP-SM-GW asks the HSS, and answers with itself, of an MT Correlation ID of the HPLMN (TS 23.204 §6.4 step 3c;
// TS 23.040 §3.2.7a), the MWD Status of the HSS transferred (TS 29.338 §5.2.1.4).
func TestRoutingInfo(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.router.routing = mmeRouting()

	routing, err := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	if err != nil {
		t.Fatal(err)
	}

	node := routing.Serving
	if node == nil || node.IPSMGW == nil || node.IPSMGW.Name != ims.OriginHost || node.IPSMGW.Number != "15550000100" ||
		node.MME != nil || routing.SMSF3GPP != nil || routing.MWDStatus != s6c.MWDStatusMNRF {
		t.Fatalf("routing = %+v", routing)
	}

	if len(routing.IMSI) != 15 || !strings.HasPrefix(routing.IMSI, "001010") || routing.IMSI == mmeRouting().IMSI {
		t.Fatalf("MT Correlation ID = %s", routing.IMSI)
	}

	if r := w.router.requests[0]; r.MSISDN != "15551230001" || !r.GPRSIndicator || !r.SMSFSupport || r.ServiceCentreAddress != "15550000000" {
		t.Fatalf("SRR to the HSS = %+v", r)
	}
}

// An error of the HSS is returned as such, unless the IP-SM-GW has a public user identity of the MSISDN (TS 24.341
// §5.3.3.3).
func TestRoutingInfoError(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.router.err = &s6c.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), MWDStatus: s6c.MWDStatusMNRF}

	_, err := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	if !tgpp.IsExperimental(err, tgpp.ResultErrorAbsentUser) {
		t.Fatalf("answer = %v, want the HSS's absent user", err)
	}

	w.available()

	routing, err := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	if err != nil || routing.Serving == nil || routing.Serving.IPSMGW == nil {
		t.Fatalf("answer = %+v, %v, want the IP-SM-GW", routing, err)
	}
}

// A short message goes over IMS to a user able to receive it, and the SMS-GMSC gets the SMS-DELIVER-REPORT of the
// UE's RP-ACK (TS 24.341 §5.3.3.4.2; TS 23.204 §6.5).
func TestDeliveredOverIMS(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.router.routing = mmeRouting()
	w.available()

	routing, _ := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	ans := w.forward(routing.IMSI, 30*time.Second)

	req, data := w.delivered(202)

	checks := map[string]string{
		"Accept-Contact":      "*;+g.3gpp.smsip;explicit;require",
		"Request-Disposition": "no-fork",
		"P-Asserted-Identity": "<sip:ipsmgw." + home + ">",
		"Content-Type":        ContentType,
	}

	for name, want := range checks {
		if got := req.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	if req.URI.String() != "sip:alice@"+home || data.Originator == nil || data.Originator.Digits != "15550000000" ||
		!bytes.Equal(data.UserData, []byte{0x04, 0x02}) {
		t.Fatalf("MESSAGE to %s of %+v", req.URI, data)
	}

	w.report(&sms.RPAck{Direction: nas.DirectionUplink, Reference: data.Reference, UserData: []byte{0x00, 0x00}},
		req.Header.CallID())

	res, err := sgd.ParseMTForwardShortMessageAnswer(answerOf(t, ans))
	if err != nil || !bytes.Equal(res.SMRPUI, []byte{0x00, 0x00}) {
		t.Fatalf("TFA = %+v, %v", res, err)
	}

	if len(w.nodes.forwarded) != 0 || len(w.router.reports) != 0 {
		t.Fatalf("forwarded %+v, reported %+v", w.nodes.forwarded, w.router.reports)
	}
}

// A delivery report without In-Reply-To is of the short message of its reference to the sender.
func TestDeliveryReportByReference(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.router.routing = mmeRouting()
	w.available()

	routing, _ := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	ans := w.forward(routing.IMSI, 30*time.Second)

	_, data := w.delivered(202)
	w.report(&sms.RPAck{Direction: nas.DirectionUplink, Reference: data.Reference}, "")

	if _, err := sgd.ParseMTForwardShortMessageAnswer(answerOf(t, ans)); err != nil {
		t.Fatalf("TFA = %v", err)
	}
}

// A full memory stops the delivery, and is reported to the HSS (TS 23.204 §6.5a step 4; TS 23.040 §11.1).
func TestMemoryCapacityExceeded(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.router.routing = mmeRouting()
	w.available()

	routing, _ := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	ans := w.forward(routing.IMSI, 30*time.Second)

	req, data := w.delivered(202)
	w.report(&sms.RPError{
		Direction: nas.DirectionUplink, Reference: data.Reference, Cause: sms.RPCauseMemoryCapacityExceeded,
		UserData: []byte{0x00, 0xd3},
	}, req.Header.CallID())

	_, err := sgd.ParseMTForwardShortMessageAnswer(answerOf(t, ans))

	var re *sgd.ResultError
	if !errors.As(err, &re) || re.DeliveryFailureCause == nil || *re.DeliveryFailureCause != sgd.CauseMemoryCapacityExceeded ||
		!bytes.Equal(re.DiagnosticInfo, []byte{0x00, 0xd3}) {
		t.Fatalf("TFA = %v", err)
	}

	if len(w.nodes.forwarded) != 0 {
		t.Fatalf("forwarded over NAS: %+v", w.nodes.forwarded)
	}

	if len(w.router.reports) != 1 || w.router.reports[0].IPSMGW == nil ||
		w.router.reports[0].IPSMGW.Cause != s6c.DeliveryCauseMemoryCapacityExceeded {
		t.Fatalf("reports = %+v", w.router.reports)
	}
}

// A failure over IMS falls back to the MME, of the IMSI (TS 23.204 §6.5a; TS 29.311 Table 6.1.4.4.1.1).
func TestFallbackToNAS(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.router.routing = mmeRouting()
	w.nodes.answers["core.epc.mnc001.mcc001.3gppnetwork.org"] = fakeAnswer{ui: []byte{0x00, 0x01}}
	w.available()

	routing, _ := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	ans := w.forward(routing.IMSI, 30*time.Second)

	w.delivered(480)

	res, err := sgd.ParseMTForwardShortMessageAnswer(answerOf(t, ans))
	if err != nil || !bytes.Equal(res.SMRPUI, []byte{0x00, 0x01}) {
		t.Fatalf("TFA = %+v, %v", res, err)
	}

	if m := w.nodes.forwarded[0]; m.IMSI != "001010000000001" || m.MMENumberForMTSMS != "15550000010" {
		t.Fatalf("TFR to the MME = %+v", m)
	}
}

// Every domain absent: the HSS gets the outcomes, and the SMS-GMSC the absent user of the last attempt; no delivery
// report within the timer is an absent user over IMS (TS 23.040 §11.1).
func TestAbsentEverywhere(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.router.routing = mmeRouting()
	w.nodes.answers["core.epc.mnc001.mcc001.3gppnetwork.org"] = fakeAnswer{err: &sgd.ResultError{
		Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser),
	}}
	w.available()

	routing, _ := s6c.ParseSendRoutingInfoForSMAnswer(w.routingInfo())
	ans := w.forward(routing.IMSI, 3*time.Second)

	w.delivered(202)

	_, err := sgd.ParseMTForwardShortMessageAnswer(answerOf(t, ans))
	if !tgpp.IsExperimental(err, tgpp.ResultErrorAbsentUser) {
		t.Fatalf("TFA = %v", err)
	}

	rep := w.router.reports[0]
	if rep.MSISDN != "15551230001" || rep.IPSMGW == nil || rep.IPSMGW.Cause != s6c.DeliveryCauseAbsentUser ||
		rep.IPSMGW.AbsentDiagnostic == nil || *rep.IPSMGW.AbsentDiagnostic != tgpp.AbsentUserNoResponseIPSMGW ||
		rep.MME == nil || rep.MME.Cause != s6c.DeliveryCauseAbsentUser {
		t.Fatalf("report = %+v", rep)
	}
}

// An MT Correlation ID the IP-SM-GW did not give is of an unknown user.
func TestUnknownCorrelationID(t *testing.T) {
	w := newWorld(t, newFakeStore())

	_, err := sgd.ParseMTForwardShortMessageAnswer(answerOf(t, w.forward("001010123456789", time.Second)))
	if !tgpp.IsExperimental(err, tgpp.ResultErrorUserUnknown) {
		t.Fatalf("TFA = %v", err)
	}
}

func TestPLMNOf(t *testing.T) {
	if got := plmnOf(home); got != "001001" {
		t.Fatalf("plmnOf = %s", got)
	}
}
