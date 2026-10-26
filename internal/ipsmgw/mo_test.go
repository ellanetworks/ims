package ipsmgw

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/nas"
	"github.com/ellanetworks/core/nas/sms"
	"github.com/ellanetworks/ims/sip"
)

// submitTPDU is an SMS-SUBMIT.
var submitTPDU = []byte{0x01, 0x07, 0x0b, 0x91, 0x51, 0x55, 0x21, 0x03, 0x00, 0xf2, 0x00, 0x00, 0x02, 0xe8, 0x34}

// message sends a MESSAGE of the sender alice, from the S-CSCF, of body and the extra header fields, and returns
// the response.
func (w *world) message(contentType string, body []byte, extra ...string) *sip.Response {
	w.t.Helper()

	w.cseq++

	msg := fmt.Sprintf("MESSAGE tel:+15550000000 SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP %s;branch=z9hG4bK-msg%d\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:alice@%s>;tag=a\r\n"+
		"To: <tel:+15550000000>\r\n"+
		"Call-ID: mo-%d\r\n"+
		"CSeq: 1 MESSAGE\r\n"+
		"P-Asserted-Identity: <sip:alice@%s>, <tel:+15551230001>\r\n"+
		"P-Charging-Vector: icid-value=mo;orig-ioi=visited.example.org\r\n",
		w.scscf.Addr(), w.cseq, home, w.cseq, home)

	for _, h := range extra {
		msg += h + "\r\n"
	}

	msg += fmt.Sprintf("Content-Type: %s\r\nContent-Length: %d\r\n\r\n", contentType, len(body)) + string(body)

	w.scscf.Send(sip.UDP, w.as, mustParse(w.t, msg))

	res, _ := w.scscf.RecvResponse()

	return res
}

// answered takes the MESSAGE of the IP-SM-GW that answers a short message, answers it 200, and returns it and its
// RPDU.
func (w *world) answered() (*sip.Request, sms.RPMessage) {
	w.t.Helper()

	req, _ := w.scscf.RecvRequest()
	if req.Method != "MESSAGE" {
		w.t.Fatalf("got %s, want a MESSAGE", req.Method)
	}

	w.scscf.Send(sip.UDP, w.as, sip.NewResponse(req, 200, "OK"))

	rp, err := sms.ParseRP(req.Content().Body, nas.DirectionDownlink)
	if err != nil {
		w.t.Fatalf("RPDU %x: %v", req.Content().Body, err)
	}

	return req, rp
}

func rpData(t *testing.T, sc *sms.Address) []byte {
	t.Helper()

	b, err := (&sms.RPData{Direction: nas.DirectionUplink, Reference: 5, Destination: sc, UserData: submitTPDU}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// A short message goes to the SMSC of its service centre address, and the SMSC's submit report back to the sender,
// in a MESSAGE of the IP-SM-GW (TS 24.341 §5.3.3.4.1, §5.3.3.4.3).
func TestShortMessageSubmitted(t *testing.T) {
	w := newWorld(t, newFakeStore())
	w.smscs.ui = []byte{0x01, 0x00, 0x62, 0x90}

	if res := w.message(ContentType, rpData(t, sms.E164Address("15550000000"))); res.StatusCode != 202 ||
		res.Header.Get("P-Charging-Vector") != "icid-value=mo;orig-ioi=visited.example.org;term-ioi="+home {
		t.Fatalf("MESSAGE = %d %v", res.StatusCode, res.Header)
	}

	req, rp := w.answered()

	ack, ok := rp.(*sms.RPAck)
	if !ok || ack.Reference != 5 || !bytes.Equal(ack.UserData, w.smscs.ui) {
		t.Fatalf("RPDU = %+v", rp)
	}

	checks := map[string]string{
		"Accept-Contact":      "*;+g.3gpp.smsip;explicit;require",
		"Request-Disposition": "fork",
		"In-Reply-To":         "mo-1",
		"P-Asserted-Identity": "<sip:ipsmgw." + home + ">",
		"Content-Type":        ContentType,
	}

	for name, want := range checks {
		if got := req.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	if req.URI.String() != "tel:+15551230001" || !strings.HasSuffix(req.Header.Get("Route"), ";lr>") {
		t.Errorf("Request-URI %s, Route %s", req.URI, req.Header.Get("Route"))
	}

	want := sgd.MOForwardShortMessage{
		ServiceCentreAddress: "15550000000", User: tgpp.UserIdentifier{MSISDN: "15551230001"}, SMRPUI: submitTPDU,
	}

	if len(w.smscs.forwarded) != 1 || w.smscs.forwarded[0].ServiceCentreAddress != want.ServiceCentreAddress ||
		w.smscs.forwarded[0].User != want.User || !bytes.Equal(w.smscs.forwarded[0].SMRPUI, want.SMRPUI) {
		t.Fatalf("forwarded = %+v", w.smscs.forwarded)
	}
}

// A refusal of the SMSC, or no SMSC, is an RP-ERROR of the cause of TS 23.040 §11.3.
func TestShortMessageRefused(t *testing.T) {
	congestion := sgd.CauseSCCongestion

	tests := map[string]struct {
		sc   *sms.Address
		err  error
		want sms.RPCause
		diag []byte
	}{
		"unknown service centre": {sms.E164Address("15559999999"), nil, sms.RPCauseUnassignedNumber, nil},
		"national service centre": {
			&sms.Address{TypeOfNumber: sms.TypeOfNumberNational, NumberingPlan: sms.NumberingPlanISDN, Digits: "5550000000"},
			nil, sms.RPCauseUnassignedNumber, nil,
		},
		"SC congestion": {sms.E164Address("15550000000"), &sgd.ResultError{
			Result: tgpp.Experimental(tgpp.ResultErrorSMDeliveryFailure), DeliveryFailureCause: &congestion,
			DiagnosticInfo: []byte{0x01, 0xc0},
		}, sms.RPCauseCongestion, []byte{0x01, 0xc0}},
		"facility not supported": {sms.E164Address("15550000000"), &sgd.ResultError{
			Result: tgpp.Experimental(tgpp.ResultErrorFacilityNotSupported),
		}, sms.RPCauseRequestedFacilityNotImplemented, nil},
		"no answer": {sms.E164Address("15550000000"), fmt.Errorf("timeout"), sms.RPCauseNetworkOutOfOrder, nil},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, newFakeStore())
			w.smscs.err = tt.err

			w.message(ContentType, rpData(t, tt.sc))

			_, rp := w.answered()

			e, ok := rp.(*sms.RPError)
			if !ok || e.Reference != 5 || e.Cause != tt.want || !bytes.Equal(e.UserData, tt.diag) {
				t.Fatalf("RPDU = %+v, want RP-ERROR %s", rp, tt.want)
			}
		})
	}
}

// A notification of available memory gets an RP-ERROR #69, of no ReadyForSM over Diameter (TS 23.040 §11.2); an
// RP-DATA that does not decode, an RP-ERROR #96, and one of an unknown type #97 (TS 24.011 §9.3).
func TestOtherRPDUs(t *testing.T) {
	smma, _ := (&sms.RPSMMA{Reference: 7}).MarshalBinary()

	tests := map[string]struct {
		rpdu []byte
		want sms.RPMessage
	}{
		"RP-SMMA": {smma, &sms.RPError{
			Direction: nas.DirectionDownlink, Reference: 7,
			Cause: sms.RPCauseRequestedFacilityNotImplemented,
		}},
		"truncated RP-DATA": {[]byte{0x00, 0x09, 0x00}, &sms.RPError{Direction: nas.DirectionDownlink, Reference: 9, Cause: sms.RPCauseInvalidMandatoryInformation}},
		"unknown type":      {[]byte{0x07, 0x03}, &sms.RPError{Direction: nas.DirectionDownlink, Reference: 3, Cause: sms.RPCauseMessageTypeNonExistent}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, newFakeStore())

			if res := w.message(ContentType, tt.rpdu); res.StatusCode != 202 {
				t.Fatalf("MESSAGE = %d", res.StatusCode)
			}

			_, rp := w.answered()

			got, _ := rp.MarshalBinary()
			want, _ := tt.want.MarshalBinary()

			if !bytes.Equal(got, want) {
				t.Fatalf("RPDU = %x, want %x", got, want)
			}
		})
	}
}

// A MESSAGE gets the 403 of no IP-SM-GW without an SMSC, a 488 of an In-Reply-To of no short message the IP-SM-GW
// delivered, and a 415 of another body.
func TestMessageRefused(t *testing.T) {
	w := newWorld(t, newFakeStore())

	if res := w.message("text/plain", []byte("x")); res.StatusCode != 415 {
		t.Fatalf("text MESSAGE = %d", res.StatusCode)
	}

	if res := w.message(ContentType, []byte{0x02, 0x01}, "In-Reply-To: unknown"); res.StatusCode != 488 {
		t.Fatalf("MESSAGE of an In-Reply-To = %d", res.StatusCode)
	}

	w.smscs.none = true

	if res := w.message(ContentType, rpData(t, sms.E164Address("15550000000"))); res.StatusCode != 403 ||
		res.Reason != "SMS over IP Not Supported" {
		t.Fatalf("MESSAGE without an SMSC = %d %s", res.StatusCode, res.Reason)
	}

	w.scscf.RecvNone(0)
}

// An RP-DATA without a TPDU is of invalid mandatory information (TS 24.011 §9).
func TestShortMessageWithoutTPDU(t *testing.T) {
	w := newWorld(t, newFakeStore())

	b, err := (&sms.RPData{Direction: nas.DirectionUplink, Reference: 5, Destination: sms.E164Address("15550000000")}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	w.message(ContentType, b)

	if _, rp := w.answered(); rp.(*sms.RPError).Cause != sms.RPCauseInvalidMandatoryInformation || len(w.smscs.forwarded) != 0 {
		t.Fatalf("RPDU = %+v", rp)
	}
}
