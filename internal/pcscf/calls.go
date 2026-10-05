package pcscf

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type call struct {
	ue proxy.Side

	icid string

	policy *callPolicy
}

func callOf(d *proxy.Dialog) *call {
	if d == nil {
		return nil
	}

	c, _ := d.Value().(*call)

	return c
}

// TS 24.229 §5.2.6.2
func (p *PCSCF) terminatingRoute(req *sip.Request) (sip.URI, bool) {
	routes, err := req.Header.Routes()
	if err != nil || len(routes) == 0 || !p.cfg.Proxy.IsLocal(routes[0].URI) {
		return sip.URI{}, false
	}

	top := routes[0].URI

	return top, top.User != "" && p.towardUE([]sip.URI{top})
}

// TS 24.229 §5.2.6.3.2A
func (p *PCSCF) unregisteredOrigin(req *sip.Request) bool {
	if req.Method == "REGISTER" || req.Method == "ACK" || req.Method == "CANCEL" || toTag(req) != "" {
		return false
	}

	if !p.ownPort(req.Flow.Local.Port()) || req.Flow.Local.Port() == p.cfg.Port && p.trusted(req.Flow.Remote.Addr()) {
		return false
	}

	_, ok := p.ueRegistration(req)

	return !ok
}

// TS 24.229 §5.2.6.3.3, §5.2.6.3.7, §5.2.6.3.11
func (p *PCSCF) originating(tx *transaction.ServerTransaction, req *sip.Request) {
	reg, ok := p.ueRegistration(req)
	if !ok {
		p.log.Debug("initial request from an unregistered UE", slog.String("method", req.Method),
			slog.String("source", req.Flow.Remote.String()))
		p.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	switch req.Method {
	case "UPDATE":
		p.respond(tx, sip.NewResponse(req, 403, "Target refresh outside dialog not allowed"))
		return
	case "BYE", "PRACK":
		p.respond(tx, sip.NewResponse(req, 403, "Originating subsequent requests outside dialog not allowed"))
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

	if !out.Header.Has("Route") && p.cfg.Proxy.IsLocal(out.URI) {
		p.cfg.Fallback.HandleRequest(tx, req)
		return
	}

	if len(reg.ServiceRoute) == 0 {
		p.respond(tx, sip.NewResponse(req, 403, "No Service-Route"))
		return
	}

	serviceRoute, err := sip.ParseAddressList(strings.Join(reg.ServiceRoute, ", "))
	if err != nil {
		p.log.Warn("unusable Service-Route", slog.String("impi", reg.IMPI), slog.Any("error", err))
		p.respond(tx, sip.NewResponse(req, 500, ""))

		return
	}

	if routes, err := out.Header.Routes(); err != nil || !sameRoutes(routes, serviceRoute) {
		if len(routes) > 0 {
			p.log.Debug("preloaded routes replaced by the Service-Route", slog.String("impi", reg.IMPI),
				slog.Any("route", out.Header.Values("Route")))
		}

		setRoutes(out, serviceRoute)
	}

	preferred, _ := req.Header.Addresses("P-Preferred-Identity")

	asserted := assertedIdentities(preferred, reg.AssociatedURIs)
	if len(asserted) == 0 {
		p.log.Warn("registration without public identities", slog.String("impi", reg.IMPI))
		p.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	fromUE(out)

	for _, a := range asserted {
		out.Header.Add("P-Asserted-Identity", "<"+a+">")
	}

	cv := p.newChargingVector(req.Flow.Local.Addr())
	cv.set(out)

	to, ok := p.target(serviceRoute[0].URI, req.Flow.Local.Addr())
	if !ok {
		p.log.Warn("no route to the Service-Route", slog.String("route", reg.ServiceRoute[0]))
		p.respond(tx, sip.NewResponse(req, 503, ""))

		return
	}

	var dialog *proxy.Dialog

	opts := proxy.Options{OnReply: func(rep proxy.Reply) proxy.Verdict {
		if rep.Response != nil {
			toUEResponse(rep.Response)
		}

		return p.mediaReply(tx, dialog, rep, true)
	}}

	switch out.Method {
	case "INVITE":
		c := &call{ue: proxy.Caller, icid: cv.icid, policy: p.newCallPolicy(regKey{reg.IMPI, reg.UEAddress.Addr().Unmap()}, asserted, "")}

		opts.NoAnswer = p.cfg.NoAnswer
		dialog = p.cfg.Proxy.NewDialog(proxy.DialogConfig{
			Value: c, Target: p.dialogTarget(proxy.Caller, reg.FlowToken, req.Flow.Local.Addr()), OnEvent: p.callEvent(c),
		})
		c.policy.attach(c, dialog)
		opts.Dialog = dialog

		fallthrough
	case "SUBSCRIBE", "REFER":
		opts.RecordRoute = &proxy.RecordRoute{User: reg.FlowToken, Double: true, UpstreamParams: sip.Params{{Name: ueFacing}}}
		if contactParam(req, "ob") {
			opts.RecordRoute.Params.Set("ob", "")
		}
	}

	p.forward(tx, req, out, to, opts)
}

// TS 24.229 §5.2.6.4.3, §5.2.6.4.7
func (p *PCSCF) terminating(tx *transaction.ServerTransaction, req *sip.Request, top sip.URI) {
	if !p.fromCore(req) {
		p.log.Warn("request toward a UE from outside the core", slog.String("method", req.Method),
			slog.String("source", req.Flow.Remote.String()), slog.String("local", req.Flow.Local.String()))
		p.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	if p.regs.signallingLost(top.User) {
		p.respond(tx, sip.NewResponse(req, 500, ""))
		return
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

	to, reject := p.ueTarget(req, out, removed)
	if reject != nil {
		p.respond(tx, reject)
		return
	}

	var called string

	if ids, err := req.Header.Addresses("P-Called-Party-ID"); err == nil && len(ids) > 0 {
		called = "<" + ids[0].URI.String() + ">"
	}

	cv, _ := parseChargingVector(req.Header.Get("P-Charging-Vector"))

	dialogForming := false

	var dialog *proxy.Dialog

	opts := proxy.Options{OnReply: func(rep proxy.Reply) proxy.Verdict {
		res := rep.Response
		if res == nil || rep.Err != nil {
			return p.mediaReply(tx, dialog, rep, true)
		}

		fromUEResponse(res)

		if called != "" && res.StatusCode > 100 && (res.StatusCode < 300 || !dialogForming) {
			res.Header.Add("P-Asserted-Identity", called)
		}

		p.respondCharging(req, res)

		return p.mediaReply(tx, dialog, rep, true)
	}}

	switch out.Method {
	case "INVITE":
		c := &call{ue: proxy.Callee, icid: cv.icid}

		if f, ok := p.regs.flow(top.User); ok {
			c.policy = p.newCallPolicy(regKey{f.impi, f.ue.Addr().Unmap()}, p.servedIdentities(f, called), req.Header.Get("P-Asserted-Service"))
		}

		dialog = p.cfg.Proxy.NewDialog(proxy.DialogConfig{
			Value: c, Target: p.dialogTarget(proxy.Callee, top.User, req.Flow.Local.Addr()), OnEvent: p.callEvent(c),
		})
		c.policy.attach(c, dialog)
		opts.Dialog = dialog

		fallthrough
	case "SUBSCRIBE", "REFER":
		dialogForming = true
		opts.RecordRoute = &proxy.RecordRoute{User: top.User, Double: true, DownstreamParams: sip.Params{{Name: ueFacing}}}

		if top.Params.Has("ob") {
			opts.RecordRoute.Params.Set("ob", "")
		}
	}

	p.forward(tx, req, out, to, opts)
}

func (p *PCSCF) callEvent(c *call) func(proxy.DialogEvent) {
	return func(e proxy.DialogEvent) {
		if e.Kind == proxy.EventEnded {
			p.callEnded(c)
		}
	}
}

// TS 29.214 §5.4
func (p *PCSCF) servedIdentities(f flow, called string) []string {
	if called != "" {
		return []string{strings.Trim(called, "<>")}
	}

	if r, ok := p.regs.get(f.impi, f.ue.Addr().Unmap()); ok {
		if d := defaultIdentity(r.AssociatedURIs); d != "" {
			return []string{d}
		}
	}

	return nil
}

// TS 24.229 §5.2.8.1, TS 33.203 §7.4.2a
func (p *PCSCF) dialogTarget(ue proxy.Side, token string, local netip.Addr) func(proxy.Side, *sip.Request) (proxy.Target, error) {
	return func(toward proxy.Side, req *sip.Request) (proxy.Target, error) {
		if toward == ue {
			f, ok := p.regs.flow(token)
			if !ok {
				return proxy.Target{}, errNoFlow
			}

			to, ok := p.ueFlow(f)
			if !ok {
				return proxy.Target{}, errNoFlow
			}

			return to, nil
		}

		u := req.URI
		if route, err := req.Header.TopRoute(); err == nil {
			u = route.URI
		}

		to, ok := p.target(u, local)
		if !ok {
			return proxy.Target{}, errNoRoute
		}

		return to, nil
	}
}

func sameRoutes(a, b []sip.Address) bool {
	return slices.EqualFunc(a, b, func(x, y sip.Address) bool { return x.URI.Equivalent(y.URI) })
}

func setRoutes(req *sip.Request, routes []sip.Address) {
	req.Header.Del("Route")

	if len(routes) == 0 {
		return
	}

	values := make([]string, len(routes))
	for i, r := range routes {
		values[i] = "<" + r.URI.String() + ">"
	}

	req.Header.Add("Route", strings.Join(values, ", "))
}

// TS 24.229 §5.2.6.3.1, RFC 3325 §9.1
func assertedIdentities(preferred []sip.Address, associated []string) []string {
	var (
		out              []string
		haveSIP, haveTel bool
	)

	for _, a := range preferred {
		id, ok := matchIdentity(associated, a.URI)
		if !ok {
			continue
		}

		isTel := strings.HasPrefix(strings.ToLower(id), "tel:")
		if !isTel && haveSIP && !haveTel {
			if t := telForm(a.URI); t.IsTel() {
				id, isTel = matchURI(associated, t.String())
			}
		}

		if id == "" || isTel && haveTel || !isTel && haveSIP {
			continue
		}

		haveTel, haveSIP = haveTel || isTel, haveSIP || !isTel

		out = append(out, id)
	}

	if len(out) == 0 {
		if d := defaultIdentity(associated); d != "" {
			out = append(out, d)
		}
	}

	return out
}

// TS 24.229 §5.2.6.3.1
func matchIdentity(associated []string, u sip.URI) (string, bool) {
	if id, ok := matchURI(associated, u.String()); ok {
		return id, true
	}

	want := telForm(u)

	for _, a := range associated {
		v, err := sip.ParseURI(a)
		if err == nil && telForm(v).Equivalent(want) {
			return a, true
		}
	}

	return "", false
}

func telForm(u sip.URI) sip.URI {
	if !u.IsSIP() {
		return u
	}

	if v, ok := u.Params.Get("user"); !ok || !strings.EqualFold(v, "phone") {
		return u
	}

	t, err := sip.ParseURI("tel:" + u.User)
	if err != nil {
		return u
	}

	return t
}

// TS 24.229 §5.2.6.3.5, §5.2.6.3.9 steps 1 and 2
func (p *PCSCF) fromUEInDialog(req, out *sip.Request, d *proxy.Dialog) *sip.Response {
	c := callOf(d)
	if c == nil {
		return nil
	}

	side, ok := d.Party(req)

	switch {
	case ok && side == c.ue:
	case !ok && c.ue == proxy.Caller && fromTag(req) == d.CallerTag():
		return nil
	default:
		p.log.Debug("request from a UE not on the dialog", slog.String("request", req.StartLine()), slog.String("dialog", d.ID()))
		return sip.NewResponse(req, 403, "Not on this dialog")
	}

	want, ok := d.RouteSet(req)
	if !ok {
		return nil
	}

	if routes, err := out.Header.Routes(); err != nil || !sameRoutes(routes, want) {
		p.log.Debug("in-dialog routes replaced by the dialog's", slog.String("dialog", d.ID()),
			slog.Any("route", out.Header.Values("Route")))
		setRoutes(out, want)
	}

	return nil
}

func fromTag(req *sip.Request) string {
	from, err := req.Header.From()
	if err != nil {
		return ""
	}

	return from.Tag()
}

type chargingVector struct {
	icid      string
	generated string
	origIOI   string
	termIOI   string
	access    string
}

func (p *PCSCF) newChargingVector(local netip.Addr) chargingVector {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	return chargingVector{
		icid:      strings.ToUpper(hex.EncodeToString(b)),
		generated: sip.FormatHost(local.Unmap()),
		origIOI:   p.cfg.HomeDomain,
	}
}

func parseChargingVector(s string) (chargingVector, bool) {
	var cv chargingVector

	for part := range strings.SplitSeq(s, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		value = strings.TrimSpace(value)

		switch strings.ToLower(strings.TrimSpace(name)) {
		case "icid-value":
			cv.icid = value
		case "icid-generated-at":
			cv.generated = value
		case "orig-ioi":
			cv.origIOI = value
		case "term-ioi":
			cv.termIOI = value
		}
	}

	return cv, cv.icid != ""
}

func (cv chargingVector) String() string {
	s := "icid-value=" + cv.icid

	for _, p := range []struct{ name, value string }{
		{"icid-generated-at", cv.generated}, {"orig-ioi", cv.origIOI}, {"term-ioi", cv.termIOI},
	} {
		if p.value != "" {
			s += ";" + p.name + "=" + p.value
		}
	}

	if cv.access != "" {
		s += ";" + cv.access
	}

	return s
}

func (cv chargingVector) setResponse(res *sip.Response) {
	res.Header.Del("P-Charging-Vector")
	res.Header.Add("P-Charging-Vector", cv.String())
}

func (cv chargingVector) set(req *sip.Request) {
	req.Header.Del("P-Charging-Vector")

	if cv.icid != "" {
		req.Header.Add("P-Charging-Vector", cv.String())
	}
}

// TS 24.229 §5.2.6.4.4 step 6, §5.2.6.4.6, §5.2.6.4.8, §5.2.6.4.10
func (p *PCSCF) respondCharging(req *sip.Request, res *sip.Response) {
	res.Header.Del("P-Charging-Vector")

	cv, ok := parseChargingVector(req.Header.Get("P-Charging-Vector"))
	if !ok {
		return
	}

	res.Header.Add("P-Charging-Vector", chargingVector{icid: cv.icid, origIOI: cv.origIOI, termIOI: p.cfg.HomeDomain}.String())
}

// TS 24.229 §5.2.6.3.5 step 7, §5.2.6.3.9 step 3
func (p *PCSCF) inDialogCharging(req *sip.Request, d *proxy.Dialog) chargingVector {
	if c := callOf(d); c != nil && c.icid != "" {
		return chargingVector{icid: c.icid, origIOI: p.cfg.HomeDomain, access: c.policy.takeCharging()}
	}

	return p.newChargingVector(req.Flow.Local.Addr())
}

// TS 24.229 §4.4, §5.2.1
func toUERequest(req *sip.Request) {
	kept := keptTowardUE(req.Header)

	trust.StripRequest(req)
	dropFromAll(req.Header.Del)

	req.Header = append(req.Header, kept...)
}

func toUEResponse(res *sip.Response) {
	kept := keptTowardUE(res.Header)

	toUE(res)

	res.Header = append(res.Header, kept...)
}

// RFC 3325, RFC 5009, TS 24.229 §5.2.6.4.2, §4.4.13, TS 24.237 §4.4.4,
// RFC 7044 §10.1.2
func keptTowardUE(h sip.Header) []sip.Field {
	priv, err := h.Privacy()
	private := err != nil

	var kept []sip.Field

	for _, f := range h {
		switch name := sip.LongName(f.Name); {
		case strings.EqualFold(name, "P-Asserted-Identity"):
			if !private && !priv.Has(sip.PrivacyID) {
				kept = append(kept, f)
			}
		case strings.EqualFold(name, "P-Early-Media"), strings.EqualFold(name, "Feature-Caps"):
			kept = append(kept, f)
		case strings.EqualFold(name, "History-Info"):
			if private || priv.Has(sip.PrivacyHistory) {
				continue
			}

			if v := publicHistory(f.Value); v != "" {
				kept = append(kept, sip.Field{Name: f.Name, Value: v})
			}
		}
	}

	return kept
}

// RFC 7044 §10.1.2
func publicHistory(v string) string {
	var out []string

	for _, e := range sip.SplitList(v) {
		if !strings.Contains(strings.ToLower(e), "privacy=history") {
			out = append(out, e)
		}
	}

	return strings.Join(out, ", ")
}

// TS 24.229 §5.2.6.4.1, RFC 5009
func earlyMediaSupported(h sip.Header) bool {
	em, ok, err := h.EarlyMedia()
	return ok && err == nil && em.Has(sip.EarlyMediaSupported)
}

// TS 24.229 §5.2.1
func dropFromAll(del func(string) int) {
	for _, name := range []string{"P-Charging-Vector", "P-Charging-Function-Addresses", "P-Media-Authorization"} {
		del(name)
	}
}

// TS 24.229 §5.2.1 step 8
func removeLocationSource(req *sip.Request) {
	values := req.Header.Values("Geolocation")
	if !slices.ContainsFunc(values, func(v string) bool { return strings.Contains(strings.ToLower(v), "loc-src") }) {
		return
	}

	req.Header.Del("Geolocation")

	for _, v := range values {
		as, err := sip.ParseAddressList(v)
		if err != nil {
			req.Header.Add("Geolocation", v)
			continue
		}

		for _, a := range as {
			a.Params.Del("loc-src")
			req.Header.Add("Geolocation", a.String())
		}
	}
}
