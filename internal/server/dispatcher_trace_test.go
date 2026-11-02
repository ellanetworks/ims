package server

import (
	"context"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

// plainRole is a role that tells nothing of what its requests link to.
type plainRole struct{}

func (plainRole) HandleRequest(context.Context, *transaction.ServerTransaction, *sip.Request) {}

func (plainRole) HandleCancel(context.Context, *transaction.ServerTransaction, *sip.Request) {}

func (plainRole) HandleAck(context.Context, *sip.Request) {}

func (plainRole) HandleTransactionError(context.Context, *transaction.ServerTransaction, error) {}

// linkingRole is a role that tells what the requests on its port link to.
type linkingRole struct {
	plainRole

	links []trace.SpanContext
}

func (r linkingRole) SpanLinks(*sip.Request) []trace.SpanContext { return r.links }

func TestDispatcherSpanLinksAreTheRolesOfThePort(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}})

	d := newDispatcher(slog.New(slog.DiscardHandler))
	d.set(5060, rolePCSCF, linkingRole{links: []trace.SpanContext{sc}})
	d.set(5070, roleICSCF, plainRole{})

	on := func(port uint16) *sip.Request {
		req := sip.NewRequest("BYE", sip.URI{Scheme: "sip", Host: "example.com"})
		req.Flow = sip.Flow{Transport: sip.UDP, Local: netip.AddrPortFrom(loopback, port)}

		return req
	}

	if got := d.spanLinks(on(5060)); len(got) != 1 || !got[0].Equal(sc) {
		t.Fatalf("links of the P-CSCF's port %v", got)
	}

	for _, port := range []uint16{5070, 5080} {
		if got := d.spanLinks(on(port)); got != nil {
			t.Fatalf("links of port %d %v", port, got)
		}
	}
}
