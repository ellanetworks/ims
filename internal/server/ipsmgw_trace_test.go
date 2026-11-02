package server

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/nas"
	"github.com/ellanetworks/core/nas/sms"
	"github.com/ellanetworks/ims/internal/ipsmgw"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestShortMessageSubmitIsInItsTrace(t *testing.T) {
	spans := tracetest.NewSpanRecorder()

	smsc := newFakePeerWithHandler(t, "smsc.epc.mnc001.mcc001.3gppnetwork.org", smscRealm,
		diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
			time.Sleep(50 * time.Millisecond)

			ans, err := sgd.NewMOForwardShortMessageAnswer(req, c.LocalIdentity(), []byte{0x01, 0x00})
			if err != nil {
				t.Error(err)
			}

			return ans
		}), settings.ApplicationSGd)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	p := smsc.config("smsc")
	p.ServiceCentres = []string{"15550000000"}
	cfg.Peers = append(cfg.Peers, p)
	cfg.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	srv := startIMS(t, cfg)
	waitOpen(t, srv, "smsc")

	scscf := newPeer(t, srv, netip.AddrPortFrom(loopback, uint16(unusedPort(t))))

	rpdu, err := (&sms.RPData{
		Direction: nas.DirectionUplink, Reference: 5, Destination: sms.E164Address("15550000000"),
		UserData: []byte{0x01, 0x07, 0x0b, 0x91, 0x51, 0x55, 0x21, 0x03, 0x00, 0xf2, 0x00, 0x00, 0x02, 0xe8, 0x34},
	}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	msg := fmt.Sprintf("MESSAGE tel:+15550000000 SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP %s;branch=z9hG4bK-mo1\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:alice@%s>;tag=a\r\n"+
		"To: <tel:+15550000000>\r\n"+
		"Call-ID: mo-trace\r\n"+
		"CSeq: 1 MESSAGE\r\n"+
		"P-Asserted-Identity: <sip:alice@%s>, <tel:+15551230001>\r\n"+
		"Content-Type: %s\r\n"+
		"Content-Length: %d\r\n\r\n", scscf.Addr(), imsRealm, imsRealm, ipsmgw.ContentType, len(rpdu))

	req, err := sip.Parse(append([]byte(msg), rpdu...))
	if err != nil {
		t.Fatal(err)
	}

	scscf.Send(sip.UDP, netip.AddrPortFrom(loopback, ipsmgw.Port), req)

	if res, _ := scscf.RecvResponse(); res.StatusCode != 202 {
		t.Fatalf("MESSAGE answered %d, want 202", res.StatusCode)
	}

	answer, _ := scscf.RecvRequest()
	scscf.Send(sip.UDP, netip.AddrPortFrom(loopback, ipsmgw.Port), sip.NewResponse(answer, 200, "OK"))

	if rp, err := sms.ParseRP(answer.Content().Body, nas.DirectionDownlink); err != nil {
		t.Fatalf("submit report %x: %v", answer.Content().Body, err)
	} else if _, ok := rp.(*sms.RPAck); !ok {
		t.Fatalf("submit report %+v, want an RP-ACK", rp)
	}

	var message, ofr sdktrace.ReadOnlySpan

	eventually(t, "the MESSAGE and OFR spans", func() bool {
		for _, s := range spansNamed(spans.Ended(), "MESSAGE", trace.SpanKindServer) {
			for _, a := range s.Attributes() {
				if a.Key == "ella.ims.role" && a.Value.AsString() == "ipsmgw" {
					message = s
				}
			}
		}

		for _, s := range spans.Ended() {
			if strings.HasPrefix(s.Name(), "SGd/") && s.SpanKind() == trace.SpanKindClient {
				ofr = s
			}
		}

		return message != nil && ofr != nil
	})

	if ofr.SpanContext().TraceID() != message.SpanContext().TraceID() || ofr.Parent().SpanID() != message.SpanContext().SpanID() {
		t.Fatalf("the OFR span %s has parent %v, want the MESSAGE's %v", ofr.Name(), ofr.Parent(), message.SpanContext())
	}
}
