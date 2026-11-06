package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

type plainRole struct{}

func (plainRole) HandleRequest(context.Context, *transaction.ServerTransaction, *sip.Request) {}

func (plainRole) HandleCancel(context.Context, *transaction.ServerTransaction, *sip.Request) {}

func (plainRole) HandleAck(context.Context, *sip.Request) {}

func (plainRole) HandleTransactionError(context.Context, *transaction.ServerTransaction, error) {}

type linkingRole struct {
	plainRole

	links []trace.SpanContext
}

func (r linkingRole) SpanLinks(*sip.Request) []trace.SpanContext { return r.links }

func TestDispatcherSpanLinksAreTheRolesOfTheListener(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}})
	other := netip.MustParseAddr("127.0.0.2")

	d := newDispatcher(slog.New(slog.DiscardHandler))
	d.bind(netip.AddrPortFrom(loopback, 5060), rolePCSCF)
	d.bind(netip.AddrPortFrom(loopback, 5070), roleICSCF)
	d.bind(netip.AddrPortFrom(other, 5060), roleIBCF)
	d.set(&role{name: rolePCSCF, handler: linkingRole{links: []trace.SpanContext{sc}}})
	d.set(&role{name: roleICSCF, handler: plainRole{}})
	d.set(&role{name: roleIBCF, handler: plainRole{}})

	on := func(local netip.AddrPort) *sip.Request {
		req := sip.NewRequest("BYE", sip.URI{Scheme: "sip", Host: "example.com"})
		req.Flow = sip.Flow{Transport: sip.UDP, Local: local}

		return req
	}

	if got := d.spanLinks(on(netip.AddrPortFrom(loopback, 5060))); len(got) != 1 || !got[0].Equal(sc) {
		t.Fatalf("links of the P-CSCF's listener %v", got)
	}

	for _, local := range []netip.AddrPort{netip.AddrPortFrom(loopback, 5070), netip.AddrPortFrom(loopback, 5080), netip.AddrPortFrom(other, 5060)} {
		if got := d.spanLinks(on(local)); got != nil {
			t.Fatalf("links of %s %v", local, got)
		}
	}
}

func TestDispatcherSelectsTheRoleByAddressAndPort(t *testing.T) {
	pcscf := netip.AddrPortFrom(loopback, 5060)
	ibcf := netip.MustParseAddrPort("127.0.0.2:5060")
	refused := errors.New("refused")

	d := newDispatcher(slog.New(slog.DiscardHandler))
	d.bind(pcscf, rolePCSCF)
	d.bind(ibcf, roleIBCF)

	if d.accept(ibcf, netip.MustParseAddrPort("192.0.2.1:1")) {
		t.Fatal("a listener without a role accepted a connection")
	}

	d.set(&role{name: rolePCSCF, handler: plainRole{}})
	d.set(&role{
		name: roleIBCF, handler: plainRole{}, connections: 64, filter: func(sip.Message) error { return refused },
		accept: func(_, remote netip.AddrPort) bool { return remote.Addr() == netip.MustParseAddr("192.0.2.1") },
	})

	req := func(local netip.AddrPort) *sip.Request {
		r := sip.NewRequest("OPTIONS", sip.URI{Scheme: "sip", Host: "example.com"})
		r.Flow = sip.Flow{Transport: sip.UDP, Local: local}

		return r
	}

	switch {
	case d.filter(req(pcscf)) != nil:
		t.Error("the IBCF's filter applied to the P-CSCF's listener")
	case !errors.Is(d.filter(req(ibcf)), refused):
		t.Error("the IBCF's filter not applied to its listener")
	case !d.accept(pcscf, netip.MustParseAddrPort("198.51.100.1:1")):
		t.Error("the P-CSCF's listener refused a connection")
	case d.accept(ibcf, netip.MustParseAddrPort("198.51.100.1:1")):
		t.Error("the IBCF's listener accepted a connection from an unknown source")
	case !d.accept(ibcf, netip.MustParseAddrPort("192.0.2.1:1")):
		t.Error("the IBCF's listener refused a connection from its source")
	case d.connections(pcscf) != 0 || d.connections(ibcf) != 64:
		t.Errorf("connection limits %d, %d", d.connections(pcscf), d.connections(ibcf))
	}
}
