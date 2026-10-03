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

// call is the P-CSCF's record of an INVITE dialog of one of its UEs, kept as
// the tracker's dialog value.
type call struct {
	// ue is the party of the dialog the P-CSCF serves: the caller on the
	// originating side, the callee on the terminating side.
	ue proxy.Side

	// charging is the P-Charging-Vector of the initial INVITE, whose icid
	// the P-CSCF puts in the dialog's later requests (TS 24.229 §5.2.6.3.5
	// step 7, §5.2.6.3.9 step 3).
	charging chargingVector
}

func callOf(d *proxy.Dialog) *call {
	if d == nil {
		return nil
	}

	c, _ := d.Value().(*call)

	return c
}

// terminatingRoute reports whether the top Route of a request is the Path
// entry of a UE's registration: the P-CSCF's own URI with a flow token, not
// one of its UE-facing Record-Route entries (TS 24.229 §5.2.6.2).
func (p *PCSCF) terminatingRoute(req *sip.Request) (sip.URI, bool) {
	routes, err := req.Header.Routes()
	if err != nil || len(routes) == 0 || !p.cfg.Proxy.IsLocal(routes[0].URI) {
		return sip.URI{}, false
	}

	top := routes[0].URI

	return top, top.User != "" && p.towardUE([]sip.URI{top})
}

// unregisteredOrigin reports whether an initial request comes from a UE the
// P-CSCF has no registration for. It is discarded unanswered (TS 24.229
// §5.2.6.3.2A), before the transaction layer could send a 100 (Trying).
func (p *PCSCF) unregisteredOrigin(req *sip.Request) bool {
	if req.Method == "REGISTER" || req.Method == "ACK" || req.Method == "CANCEL" || toTag(req) != "" {
		return false
	}

	// Filter sees the messages of every role.
	if !p.ownPort(req.Flow.Local.Port()) || req.Flow.Local.Port() == p.cfg.Port && p.trusted(req.Flow.Remote.Addr()) {
		return false
	}

	_, ok := p.ueRegistration(req)

	return !ok
}

// originating routes an initial request from the UE along its Service-Route
// (TS 24.229 §5.2.6.3.3, §5.2.6.3.7, §5.2.6.3.11).
func (p *PCSCF) originating(tx *transaction.ServerTransaction, req *sip.Request) {
	reg, ok := p.ueRegistration(req)
	if !ok {
		// Filter drops these; the registration ended in between.
		p.log.Info("initial request from an unregistered UE", slog.String("method", req.Method),
			slog.String("source", req.Flow.Remote.String()))
		p.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	// Requests that belong in a dialog are refused outside one, as Kamailio
	// does, rather than routed as initial requests.
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

	// A request for the P-CSCF itself, such as a keep-alive OPTIONS.
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

	// §5.2.6.3.3 step 2: the routes must be the Service-Route, compared per
	// URI; otherwise the P-CSCF replaces them (option b).
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

	opts := proxy.Options{OnReply: func(rep proxy.Reply) proxy.Verdict {
		if rep.Response != nil {
			toUEResponse(rep.Response)
		}

		return proxy.Relay
	}}

	// §5.2.6.3.3 step 4: the UE-facing entry carries the port where the
	// P-CSCF awaits the UE's subsequent requests, the protected server
	// port, and the flow token, which the core-facing one carries too.
	switch out.Method {
	case "INVITE":
		opts.Dialog = p.cfg.Proxy.NewDialog(proxy.DialogConfig{Value: &call{ue: proxy.Caller, charging: cv}})
		fallthrough
	case "SUBSCRIBE", "REFER":
		opts.RecordRoute = &proxy.RecordRoute{User: reg.FlowToken, Double: true, UpstreamParams: sip.Params{{Name: ueFacing}}}
		if contactParam(req, "ob") {
			opts.RecordRoute.Params.Set("ob", "")
		}
	}

	p.forward(tx, req, out, to, opts)
}

// terminating routes an initial request from the core to the UE whose Path
// the top Route is (TS 24.229 §5.2.6.4.3, §5.2.6.4.7).
func (p *PCSCF) terminating(tx *transaction.ServerTransaction, req *sip.Request, top sip.URI) {
	if !p.fromCore(req) {
		p.log.Info("request toward a UE from outside the core", slog.String("method", req.Method),
			slog.String("source", req.Flow.Remote.String()), slog.String("local", req.Flow.Local.String()))
		p.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	// §5.2.6.4.3 step 1, §5.2.6.4.7: the PCRF reported the UE's signalling
	// path lost.
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

	// §5.2.6.4.3 step 11: the P-Called-Party-ID becomes the asserted identity
	// of the UE's responses.
	var called string

	if ids, err := req.Header.Addresses("P-Called-Party-ID"); err == nil && len(ids) > 0 {
		called = "<" + ids[0].URI.String() + ">"
	}

	cv, _ := parseChargingVector(req.Header.Get("P-Charging-Vector"))

	dialogForming := false

	opts := proxy.Options{OnReply: func(rep proxy.Reply) proxy.Verdict {
		res := rep.Response
		if res == nil || rep.Err != nil {
			return proxy.Relay
		}

		fromUEResponse(res)

		// §5.2.6.4.4 step 1 for the 1xx and 2xx of a dialog, §5.2.6.4.8
		// step 2 for any response to a standalone request.
		if called != "" && res.StatusCode > 100 && (res.StatusCode < 300 || !dialogForming) {
			res.Header.Add("P-Asserted-Identity", called)
		}

		cv.respond(res, p.cfg.HomeDomain)

		return proxy.Relay
	}}

	switch out.Method {
	case "INVITE":
		opts.Dialog = p.cfg.Proxy.NewDialog(proxy.DialogConfig{Value: &call{ue: proxy.Callee, charging: cv}})
		fallthrough
	case "SUBSCRIBE", "REFER":
		dialogForming = true
		opts.RecordRoute = &proxy.RecordRoute{User: top.User, Double: true, DownstreamParams: sip.Params{{Name: ueFacing}}}

		// §5.2.6.4.4: the flow token and "ob" of the Route go in the
		// Record-Route.
		if top.Params.Has("ob") {
			opts.RecordRoute.Params.Set("ob", "")
		}
	}

	p.forward(tx, req, out, to, opts)
}

// sameRoutes compares a route set with another, URI by URI.
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

// assertedIdentities are the identities the P-CSCF asserts for a request from
// the UE (TS 24.229 §5.2.6.3.1): those of the P-Preferred-Identity that are
// registered, at most one SIP and one tel URI, else the default identity.
// The From header field takes no part (NOTE 3), nor do display names (NOTE
// 4).
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
		if isTel && haveTel || !isTel && haveSIP {
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

// matchIdentity finds a public identity among the registered ones. A SIP URI
// with user=phone is compared as the tel URI it stands for (§5.2.6.3.1).
// The identity as the UE gave it wins over its other form.
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

// fromUEInDialog checks an in-dialog request from the UE against the dialog
// the tracker knows (TS 24.229 §5.2.6.3.5 and §5.2.6.3.9 steps 1 and 2): the
// UE must be its party, and its routes those of the dialog, which replace
// them otherwise. Without a known dialog (Decision 2), only the flow check of
// coreTarget applies.
func (p *PCSCF) fromUEInDialog(req, out *sip.Request, d *proxy.Dialog) *sip.Response {
	c := callOf(d)
	if c == nil {
		return nil
	}

	if side, ok := d.Party(req); !ok || side != c.ue {
		p.log.Info("request from a UE not on the dialog", slog.String("request", req.StartLine()), slog.String("dialog", d.ID()))
		return sip.NewResponse(req, 403, "Not on this dialog")
	}

	// The route set of the far side is known once the INVITE is answered.
	switch d.State() {
	case proxy.Answered, proxy.Confirmed, proxy.Ending:
	default:
		return nil
	}

	far := proxy.Callee
	if c.ue == proxy.Callee {
		far = proxy.Caller
	}

	want := d.Routes(far)

	if routes, err := out.Header.Routes(); err != nil || !sameRoutes(routes, want) {
		p.log.Debug("in-dialog routes replaced by the dialog's", slog.String("dialog", d.ID()),
			slog.Any("route", out.Header.Values("Route")))
		setRoutes(out, want)
	}

	return nil
}

// chargingVector is a P-Charging-Vector (RFC 7315 §4.6).
type chargingVector struct {
	icid      string
	generated string
	origIOI   string
}

func (p *PCSCF) newChargingVector(local netip.Addr) chargingVector {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	return chargingVector{
		icid:      strings.ToUpper(hex.EncodeToString(b)),
		generated: local.Unmap().String(),
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
		}
	}

	return cv, cv.icid != ""
}

func (cv chargingVector) String() string {
	s := "icid-value=" + cv.icid

	if cv.generated != "" {
		s += ";icid-generated-at=" + cv.generated
	}

	if cv.origIOI != "" {
		s += ";orig-ioi=" + cv.origIOI
	}

	return s
}

// set puts the vector in a request toward the core, with the P-CSCF's type 1
// orig-ioi and no term-ioi (§5.2.6.3.3 step 7).
func (cv chargingVector) set(req *sip.Request) {
	req.Header.Del("P-Charging-Vector")

	if cv.icid != "" {
		req.Header.Add("P-Charging-Vector", cv.String())
	}
}

// respond puts the vector of the request in a response from the UE, with the
// P-CSCF's type 1 term-ioi (§5.2.6.4.4 step 6).
func (cv chargingVector) respond(res *sip.Response, homeDomain string) {
	res.Header.Del("P-Charging-Vector")

	if cv.icid == "" {
		return
	}

	v := cv.String()
	if homeDomain != "" {
		v += ";term-ioi=" + homeDomain
	}

	res.Header.Add("P-Charging-Vector", v)
}

// inDialogCharging is the P-Charging-Vector of an in-dialog request from the
// UE: the INVITE's icid on a known call, a new one otherwise.
func (p *PCSCF) inDialogCharging(req *sip.Request, d *proxy.Dialog) chargingVector {
	if c := callOf(d); c != nil && c.charging.icid != "" {
		cv := c.charging
		cv.origIOI = p.cfg.HomeDomain

		return cv
	}

	return p.newChargingVector(req.Flow.Local.Addr())
}

// toUERequest removes from a request toward the UE what the trust domain
// keeps to itself (TS 24.229 §4.4, §5.2.1). The P-Asserted-Identity stays
// unless Privacy asks for "id" (RFC 3325 §7, Decision 12); so does the
// caller's P-Early-Media "supported" (Decision 7).
func toUERequest(req *sip.Request) {
	asserted := keptIdentity(req.Header)
	early := earlyMediaSupported(req.Header)

	trust.StripRequest(req)
	dropFromAll(req.Header.Del)

	for _, v := range asserted {
		req.Header.Add("P-Asserted-Identity", v)
	}

	if early {
		req.Header.Add("P-Early-Media", sip.EarlyMediaSupported)
	}
}

// toUEResponse does the same for a response toward the UE. No early media is
// authorised (Decision 7), so no P-Early-Media reaches it.
func toUEResponse(res *sip.Response) {
	asserted := keptIdentity(res.Header)

	toUE(res)

	for _, v := range asserted {
		res.Header.Add("P-Asserted-Identity", v)
	}
}

func keptIdentity(h sip.Header) []string {
	priv, err := h.Privacy()
	if err != nil || priv.Has(sip.PrivacyID) {
		return nil
	}

	return h.Values("P-Asserted-Identity")
}

// earlyMediaSupported reports whether a message says its sender supports
// P-Early-Media. That is all the P-CSCF passes on from a UE, which may not
// authorise early media itself (TS 24.229 §5.2.6.4.1, RFC 5009 §5).
func earlyMediaSupported(h sip.Header) bool {
	em, ok, err := h.EarlyMedia()
	return ok && err == nil && em.Has(sip.EarlyMediaSupported)
}

// dropFromAll removes the header fields the P-CSCF removes from every message,
// whichever its direction (TS 24.229 §5.2.1).
func dropFromAll(del func(string) int) {
	for _, name := range []string{"P-Charging-Vector", "P-Charging-Function-Addresses", "P-Media-Authorization"} {
		del(name)
	}
}

// removeLocationSource removes the "loc-src" parameter of the UE's Geolocation
// header fields (TS 24.229 §5.2.1 step 8).
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
