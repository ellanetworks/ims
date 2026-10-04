package pcscf

import (
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

const ueFacing = "ue"

type SCSCF struct {
	Name      sip.URI
	Listeners []netip.AddrPort
}

func pathURI(token string, addr netip.Addr, port uint16, ob bool) sip.URI {
	u := sip.URI{Scheme: "sip", User: token, Host: sip.FormatHost(addr.Unmap()), Port: port}
	u.Params.Set("lr", "")

	if ob {
		u.Params.Set("ob", "")
	}

	return u
}

func contactParam(req *sip.Request, name string) bool {
	contacts, err := req.Header.Contacts()
	if err != nil {
		return false
	}

	return slices.ContainsFunc(contacts, func(c sip.Address) bool { return c.Params.Has(name) || c.URI.Params.Has(name) })
}

func (p *PCSCF) ueRegistration(req *sip.Request) (db.PCSCFRegistration, bool) {
	k, ok := p.ueKey(req)
	if !ok {
		return db.PCSCFRegistration{}, false
	}

	return p.regs.get(k.impi, k.ue)
}

func (p *PCSCF) ueKey(req *sip.Request) (regKey, bool) {
	if p.sas != nil && p.sas.protected(req.Flow.Local.Port()) {
		v, ok := p.sas.lookup(req.Flow)
		if !ok {
			return regKey{}, false
		}

		return regKey{v.impi, req.Flow.Remote.Addr().Unmap()}, true
	}

	return p.regs.sourceKey(req.Flow.Remote)
}

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

	d := p.cfg.Proxy.Dialog(removed)

	if d != nil && d.Released() {
		p.respond(tx, sip.NewResponse(req, 481, ""))
		return
	}

	if p.towardUE(removed) {
		p.toUEFlow(tx, req, out, removed, d)
		return
	}

	p.toCore(tx, req, out, removed, d)
}

func (p *PCSCF) HandleAck(ack *sip.Request) {
	out, removed, err := p.cfg.Proxy.Preprocess(ack)
	if err != nil || len(removed) == 0 {
		p.cfg.Fallback.HandleAck(ack)
		return
	}

	var (
		to     proxy.Target
		reject *sip.Response
	)

	d := p.cfg.Proxy.Dialog(removed)

	if p.towardUE(removed) {
		to, reject = p.ueTarget(ack, out, removed)
	} else {
		to, reject = p.coreTarget(ack, out, removed, d)
	}

	if reject != nil {
		p.log.Debug("dropped an ACK", slog.String("call-id", ack.Header.CallID()), slog.Int("code", reject.StatusCode))
		return
	}

	if err := p.cfg.Proxy.ForwardAck(out, to, d); err != nil {
		p.log.Debug("ACK not forwarded", slog.String("call-id", ack.Header.CallID()), slog.Any("error", err))
	}
}

func (p *PCSCF) towardUE(removed []sip.URI) bool {
	return !removed[0].Params.Has(ueFacing)
}

func (p *PCSCF) fromCore(req *sip.Request) bool {
	return req.Flow.Local.Port() == p.cfg.Port && p.trusted(req.Flow.Remote.Addr())
}

func (p *PCSCF) trusted(a netip.Addr) bool {
	return p.cfg.Trust != nil && p.cfg.Trust.Trusted(a)
}

func (p *PCSCF) ownPort(port uint16) bool {
	return port == p.cfg.Port || p.sas != nil && p.sas.protected(port)
}

func flowToken(removed []sip.URI) string {
	for _, u := range removed {
		if u.User != "" {
			return u.User
		}
	}

	return ""
}

func (p *PCSCF) toUEFlow(tx *transaction.ServerTransaction, req, out *sip.Request, removed []sip.URI, d *proxy.Dialog) {
	to, reject := p.ueTarget(req, out, removed)
	if reject != nil {
		p.respond(tx, reject)
		return
	}

	recordRouteRefresh(out, removed)

	p.forward(tx, req, out, to, proxy.Options{Dialog: d, OnReply: func(rep proxy.Reply) proxy.Verdict {
		if rep.Response != nil && rep.Err == nil {
			fromUEResponse(rep.Response)
			p.respondCharging(req, rep.Response)
		}

		return p.mediaReply(tx, d, rep, false)
	}})
}

func (p *PCSCF) ueTarget(req, out *sip.Request, removed []sip.URI) (proxy.Target, *sip.Response) {
	if !p.fromCore(req) {
		p.log.Info("request toward a UE from outside the core", slog.String("method", req.Method),
			slog.String("source", req.Flow.Remote.String()), slog.String("local", req.Flow.Local.String()))

		return proxy.Target{}, sip.NewResponse(req, 403, "")
	}

	f, ok := p.regs.flow(flowToken(removed))
	if !ok {
		return proxy.Target{}, sip.NewResponse(req, 480, "No Flow")
	}

	to, ok := p.ueFlow(f)
	if !ok {
		return proxy.Target{}, sip.NewResponse(req, 480, "No Flow")
	}

	toUERequest(out)

	return to, nil
}

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

func (p *PCSCF) toCore(tx *transaction.ServerTransaction, req, out *sip.Request, removed []sip.URI, d *proxy.Dialog) {
	to, reject := p.coreTarget(req, out, removed, d)
	if reject != nil {
		p.respond(tx, reject)
		return
	}

	p.forward(tx, req, out, to, proxy.Options{Dialog: d, OnReply: func(rep proxy.Reply) proxy.Verdict {
		if rep.Response != nil {
			toUEResponse(rep.Response)
		}

		return p.mediaReply(tx, d, rep, false)
	}})
}

// TS 24.229 §5.2.6.3.5, §5.2.6.3.9
func (p *PCSCF) coreTarget(req, out *sip.Request, removed []sip.URI, d *proxy.Dialog) (proxy.Target, *sip.Response) {
	if !p.ownFlow(req, flowToken(removed)) {
		p.log.Info("in-dialog request on another UE's flow", slog.String("source", req.Flow.Remote.String()))
		return proxy.Target{}, sip.NewResponse(req, 403, "")
	}

	if res := p.fromUEInDialog(req, out, d); res != nil {
		return proxy.Target{}, res
	}

	fromUE(out)
	p.inDialogCharging(req, d).set(out)
	recordRouteRefresh(out, removed)

	u := out.URI
	if route, err := out.Header.TopRoute(); err == nil {
		u = route.URI
	}

	to, ok := p.target(u, req.Flow.Local.Addr())
	if !ok {
		return proxy.Target{}, sip.NewResponse(req, 404, "No Route")
	}

	return to, nil
}

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

// TS 24.229 §5.2.6.3.5, §5.2.6.3.6, §5.2.6.4.5, §5.2.6.4.6, RFC 3261 §16.6 step 4
func recordRouteRefresh(out *sip.Request, removed []sip.URI) {
	switch out.Method {
	case "INVITE", "UPDATE", "SUBSCRIBE", "NOTIFY":
	default:
		return
	}

	for _, u := range removed {
		out.Header.InsertTop(sip.Field{Name: "Record-Route", Value: "<" + u.String() + ">"})
	}
}

func stripSecAgree(req *sip.Request) {
	req.Header.Del("Security-Client")
	req.Header.Del("Security-Verify")
	removeSecAgree(req, "Require")
	removeSecAgree(req, "Proxy-Require")
}
