package pcscf

import (
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

// ueFacing marks the P-CSCF's Record-Route entry that faces the UE.
const ueFacing = "ue"

// SCSCF is how the P-CSCF reaches the S-CSCF of a Service-Route without DNS.
type SCSCF struct {
	Name      sip.URI
	Listeners []netip.AddrPort
}

// pathURI is the P-CSCF's Path entry: an IP URI whose user part is the
// registration's flow token (TS 24.229 §5.2.2.1, §5.2.2.2 1A).
func pathURI(token string, addr netip.Addr, port uint16, ob bool) sip.URI {
	u := sip.URI{Scheme: "sip", User: token, Host: sip.FormatHost(addr.Unmap()), Port: port}
	u.Params.Set("lr", "")

	if ob {
		u.Params.Set("ob", "")
	}

	return u
}

func isRegEvent(req *sip.Request) bool {
	event, _, err := sip.ParseTokenParams(req.Header.Get("Event"))

	return err == nil && strings.EqualFold(event, "reg")
}

func contactParam(req *sip.Request, name string) bool {
	contacts, err := req.Header.Contacts()
	if err != nil {
		return false
	}

	return slices.ContainsFunc(contacts, func(c sip.Address) bool { return c.Params.Has(name) || c.URI.Params.Has(name) })
}

// ueRegistration returns the registration of the UE that sent a request:
// that of its security associations, else that of its address.
func (p *PCSCF) ueRegistration(req *sip.Request) (db.PCSCFRegistration, bool) {
	ue := req.Flow.Remote.Addr().Unmap()

	if p.sas != nil && p.sas.protected(req.Flow.Local.Port()) {
		v, ok := p.sas.lookup(req.Flow)
		if !ok {
			return db.PCSCFRegistration{}, false
		}

		return p.regs.get(v.impi, ue)
	}

	return p.regs.fromSource(req.Flow.Remote)
}

// ueSubscribe forwards a UE's initial SUBSCRIBE to its reg event along its
// Service-Route (TS 24.229 §5.2.6.3).
func (p *PCSCF) ueSubscribe(tx *transaction.ServerTransaction, req *sip.Request) {
	reg, ok := p.ueRegistration(req)
	if !ok {
		p.log.Info("reg event SUBSCRIBE from an unregistered UE", slog.String("source", req.Flow.Remote.String()))
		p.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	if res := p.cfg.Proxy.Check(req); res != nil {
		p.respond(tx, res)
		return
	}

	out, _, err := p.cfg.Proxy.Preprocess(req)
	if err != nil {
		p.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	if len(reg.ServiceRoute) == 0 {
		p.respond(tx, sip.NewResponse(req, 403, "No Service-Route"))
		return
	}

	routes := out.Header.Elements("Route")
	if !slices.Equal(routes, reg.ServiceRoute) {
		out.Header.Del("Route")
		out.Header.Add("Route", strings.Join(reg.ServiceRoute, ", "))
	}

	preferred, _ := req.Header.Addresses("P-Preferred-Identity")
	asserted := defaultIdentity(reg.AssociatedURIs)

	for _, a := range preferred {
		if uri, ok := matchURI(reg.AssociatedURIs, a.URI.String()); ok {
			asserted = uri
			break
		}
	}

	fromUE(out)
	stripSecAgree(out)
	out.Header.Del("P-Preferred-Identity")
	out.Header.Add("P-Asserted-Identity", "<"+asserted+">")

	first, err := sip.ParseAddress(reg.ServiceRoute[0])
	if err != nil {
		p.respond(tx, sip.NewResponse(req, 500, ""))
		return
	}

	to, ok := p.target(first.URI, req.Flow.Local.Addr())
	if !ok {
		p.log.Warn("no route to the Service-Route", slog.String("route", reg.ServiceRoute[0]))
		p.respond(tx, sip.NewResponse(req, 503, ""))

		return
	}

	rr := &proxy.RecordRoute{User: reg.FlowToken, Double: true, UpstreamParams: sip.Params{{Name: ueFacing}}}
	if contactParam(req, "ob") {
		rr.Params.Set("ob", "")
	}

	p.forward(tx, req, out, to, proxy.Options{RecordRoute: rr, OnReply: func(rep proxy.Reply) proxy.Verdict {
		if rep.Response != nil {
			toUE(rep.Response)
		}

		return proxy.Relay
	}})
}

// target is where a request to a core URI goes: its IP address, or the
// S-CSCF's listener for its name.
func (p *PCSCF) target(u sip.URI, local netip.Addr) (proxy.Target, bool) {
	tr, dest, err := sip.Destination(u)
	if err != nil {
		if !strings.EqualFold(u.Host, p.cfg.SCSCF.Name.Host) {
			return proxy.Target{}, false
		}

		for _, l := range p.cfg.SCSCF.Listeners {
			if l.Addr().Unmap() == local.Unmap() || !dest.IsValid() && l.Addr().Unmap().Is4() == local.Unmap().Is4() {
				dest = l
			}
		}

		if !dest.IsValid() {
			return proxy.Target{}, false
		}

		tr = sip.UDP
		if v, ok := u.Params.Get("transport"); ok {
			tr = sip.Transport(strings.ToUpper(v))
		}
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: netip.AddrPortFrom(local, p.cfg.Port), Remote: dest}}, true
}

func (p *PCSCF) forward(tx *transaction.ServerTransaction, req, out *sip.Request, to proxy.Target, opts proxy.Options) {
	err := p.cfg.Proxy.Forward(tx, out, to, opts)
	if err == nil || errors.Is(err, proxy.ErrAnswered) {
		return
	}

	code := 500

	if serr, ok := errors.AsType[*sip.StatusError](err); ok {
		code = serr.StatusCode
	}

	p.respond(tx, sip.NewResponse(req, code, ""))
}

// inDialog handles a request with a To tag: a NOTIFY of the P-CSCF's own
// subscription, or a request forwarded within a dialog the P-CSCF
// record-routed.
func (p *PCSCF) inDialog(tx *transaction.ServerTransaction, req *sip.Request) {
	if req.Method == "NOTIFY" {
		if s := p.subs.match(req); s != nil {
			p.respond(tx, p.subs.notified(s, req))
			return
		}
	}

	if res := p.cfg.Proxy.Check(req); res != nil {
		p.respond(tx, res)
		return
	}

	out, removed, err := p.cfg.Proxy.Preprocess(req)
	if err != nil {
		p.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	if len(removed) == 0 {
		if req.Method == "NOTIFY" {
			p.respond(tx, sip.NewResponse(req, 481, ""))
			return
		}

		p.cfg.Fallback.HandleRequest(tx, req)

		return
	}

	if p.towardUE(removed) {
		p.toUEFlow(tx, req, out, removed)
		return
	}

	p.toCore(tx, req, out, removed)
}

// towardUE tells the direction of an in-dialog request from the first Route
// entry it carried: the UE's requests start with the P-CSCF's UE-facing
// entry, which its Record-Route marks.
func (p *PCSCF) towardUE(removed []sip.URI) bool {
	return !removed[0].Params.Has(ueFacing)
}

// flowToken returns the flow token the P-CSCF's Route entries carry.
func flowToken(removed []sip.URI) string {
	for _, u := range removed {
		if u.User != "" {
			return u.User
		}
	}

	return ""
}

func (p *PCSCF) toUEFlow(tx *transaction.ServerTransaction, req, out *sip.Request, removed []sip.URI) {
	f, ok := p.regs.flow(flowToken(removed))
	if !ok {
		p.respond(tx, sip.NewResponse(req, 480, "No Flow"))
		return
	}

	to, ok := p.ueFlow(f)
	if !ok {
		p.respond(tx, sip.NewResponse(req, 480, "No Flow"))
		return
	}

	out.Header.Del("P-Charging-Vector")
	out.Header.Del("P-Charging-Function-Addresses")

	p.forward(tx, req, out, to, proxy.Options{OnReply: func(rep proxy.Reply) proxy.Verdict {
		if rep.Response != nil {
			trust.StripResponse(rep.Response)
		}

		return proxy.Relay
	}})
}

// ueFlow is where a request to a UE goes: over its security associations,
// from the protected client port, with the protected server port in the Via
// so that the UE's responses arrive on it (TS 24.229 §5.2.2.2 1A, TS 33.203
// §7.1). A UE without security associations gets it from the P-CSCF port at
// the address it registered from; one that registered over security
// associations it no longer has gets nothing.
func (p *PCSCF) ueFlow(f flow) (proxy.Target, bool) {
	tr := f.transport
	if tr == "" {
		tr = sip.UDP
	}

	if p.sas != nil {
		if out, ok := p.sas.requestFlow(f.impi, f.ue.Addr(), tr); ok {
			return proxy.Target{Flow: out, SentBy: netip.AddrPortFrom(out.Local.Addr(), p.sas.cfg.ServerPort)}, true
		}
	}

	if f.protected || !f.ue.IsValid() || f.ue.Port() == 0 {
		return proxy.Target{}, false
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: netip.AddrPortFrom(f.local, p.cfg.Port), Remote: f.ue}}, true
}

// toCore forwards a UE's in-dialog request. The flow token of its Route must
// be that of the UE's own flow: a UE cannot relay through another's dialog.
func (p *PCSCF) toCore(tx *transaction.ServerTransaction, req, out *sip.Request, removed []sip.URI) {
	if !p.ownFlow(req, flowToken(removed)) {
		p.log.Info("in-dialog request on another UE's flow", slog.String("source", req.Flow.Remote.String()))
		p.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	fromUE(out)
	stripSecAgree(out)

	u := out.URI
	if route, err := out.Header.TopRoute(); err == nil {
		u = route.URI
	}

	to, ok := p.target(u, req.Flow.Local.Addr())
	if !ok {
		p.respond(tx, sip.NewResponse(req, 404, "No Route"))
		return
	}

	p.forward(tx, req, out, to, proxy.Options{OnReply: func(rep proxy.Reply) proxy.Verdict {
		if rep.Response != nil {
			toUE(rep.Response)
		}

		return proxy.Relay
	}})
}

// ownFlow tells whether a flow token is that of the UE that sent a request:
// same address, and same private identity over security associations.
func (p *PCSCF) ownFlow(req *sip.Request, token string) bool {
	f, ok := p.regs.flow(token)
	if !ok || f.ue.Addr() != req.Flow.Remote.Addr().Unmap() {
		return false
	}

	if p.sas != nil && p.sas.protected(req.Flow.Local.Port()) {
		v, ok := p.sas.lookup(req.Flow)
		return ok && v.impi == f.impi
	}

	return !f.protected && f.ue.Port() == req.Flow.Remote.Port()
}

// stripSecAgree removes the security agreement header fields of a UE's
// request (RFC 3329 §2.3.1: a proxy "MUST remove the 'sec-agree' value from
// both the Require and Proxy-Require header fields").
func stripSecAgree(req *sip.Request) {
	req.Header.Del("Security-Client")
	req.Header.Del("Security-Verify")
	removeSecAgree(req, "Require")
	removeSecAgree(req, "Proxy-Require")
}
