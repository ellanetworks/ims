// Package participating is the participating MCPTT function (TS 24.379 §6.3.2) of the calls of its served users: the
// originating (§10.1.1.3.1.1) and terminating (§10.1.1.3.2) procedures of on-demand prearranged group calls, for the
// users the registrar bound (§7.3).
//
// It reaches the controlling function through Controlling, the MCPTT-3 reference point: in process, the two
// functions co-located and the SIP hop between them elided [PM 11]. What it hands over is the SIP request it would
// send, and what it would answer, so that a SIP binding of MCPTT-3 only has to send them. With the hop elided, the
// media of a served user is the controlling function's, not relayed (§6.3.2.1.1.1 NOTE 3), and the forwarding of
// TS 24.380 §6.4 and the mapping of the MCPTT session identities (§4.5) are the identity.
//
// It keeps no state of its own: the bindings are the registrar's, the affiliations the affiliation state's. Its
// procedures run on the strand of the call they are of.
package participating

import (
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
)

// Bindings are the service authorisations of the registrar (TS 24.379 §7.3.2 step 4).
type Bindings interface {
	Resolve(req *sip.Request, ids []sip.URI, client string) (registrar.Binding, bool)
	Bindings(id sip.URI) []registrar.Binding
}

// Affiliations are the participating function's affiliation state (TS 24.379 §9.2.2.2.11).
type Affiliations interface {
	Affiliated(now time.Time, id sip.URI, clientID string, group sip.URI) bool
}

// Router routes the INVITE the function sends a member (TS 24.379 §6.3.2.2.5.2 steps 4, 11) to the clients of its
// bindings bs, those affiliated to the calling group, by ClientID: one INVITE per branch, of its Request-URI and its
// Route, a copy of req; none if it has no route to any. The first branch to accept joins the member to the call, the
// others ended (RFC 3261 §13.2.2.4).
type Router interface {
	Route(req *sip.Request, bs []registrar.Binding) []*sip.Request
}

// Direct routes to the contact each client registered [PM 14] (RFC 3261 §10.3): the server is its registrar, which
// reaches each of its bindings.
type Direct struct{}

func (Direct) Route(req *sip.Request, bs []registrar.Binding) []*sip.Request {
	var out []*sip.Request

	for _, b := range bs {
		if len(b.Contacts) == 0 {
			continue
		}

		r := req.Clone()
		r.URI = b.Contacts[0].URI.Clone()
		r.Header.Set("To", sip.Address{URI: b.IMPU}.String())
		out = append(out, r)
	}

	return out
}

// IMS routes through the IM CN subsystem, the server an application server: a request to a client is "an initial
// request to a served public user identity" (TS 24.229 §5.7.3), to the IMPU of its binding (TS 24.379 §6.3.2.2.5.2
// step 4: "shall set the Request-URI to the public user identity associated to the MCPTT ID"), routed to the S-CSCF
// the IMPU is registered at, the Contact of its third-party REGISTER (TS 24.229 §5.4.1.7 d; §5.7.3 NOTE 6), which
// serves it as terminating: no "orig" in the Route. Every binding through the IMS has the S-CSCF of its registration,
// so the entry point of the network, which §5.7.3 allows instead, is not needed.
//
// One INVITE goes to each implicit registration set of the bindings, bindings whose sets overlap one set: the S-CSCF
// binds the contacts of a registration to each non-barred IMPU of its set (TS 24.229 §5.4.1.2.2 step 2a), and an
// INVITE to one IMPU targets all the contacts registered for it (§5.4.3.3 step 8 d), forked or searched in sequence
// (step 10 a). Its Request-URI is the IMPU of the binding of the set that expires last, of the latest registration,
// and its Route that binding's S-CSCF.
//
// The Route is loose (RFC 3261 §16.12, §19.1.1): the Contact of the S-CSCF, not a route, has no "lr", and an S-CSCF
// routes loosely as any RFC 3261 proxy. The request, of an IMS communication service of an ICSI, has the ICSI
// asserted (TS 24.229 §5.7.1.9), the server being of the trust domain (§5.7.3), and the P-Charging-Vector of §5.7.3,
// of the type 3 orig-ioi of the organisation's domain.
type IMS struct{ Source settings.Source }

func (r IMS) Route(req *sip.Request, bs []registrar.Binding) []*sip.Request {
	domain := r.Source.Current().ServiceConfiguration().Domain

	// The sets, each of the binding that expires last.
	type set struct {
		b    registrar.Binding
		keys map[string]bool
	}

	sorted := slices.Clone(bs)
	slices.SortStableFunc(sorted, func(a, b registrar.Binding) int { return b.Expires.Compare(a.Expires) })

	var sets []*set

	for _, b := range sorted {
		if !b.SCSCF.IsSIP() {
			continue
		}

		keys := map[string]bool{settings.Key(b.IMPU): true}
		for _, u := range b.IMPUs {
			keys[settings.Key(u)] = true
		}

		var into *set

		for _, st := range sets {
			for k := range keys {
				if st.keys[k] {
					into = st
					break
				}
			}

			if into != nil {
				break
			}
		}

		if into == nil {
			sets = append(sets, &set{b: b, keys: keys})
			continue
		}

		for k := range keys {
			into.keys[k] = true
		}
	}

	out := make([]*sip.Request, 0, len(sets))

	for _, st := range sets {
		b := st.b

		route := b.SCSCF.Clone()
		route.Params.Set("lr", "")

		o := req.Clone()
		o.URI = b.IMPU.Clone()
		o.Header.Set("To", sip.Address{URI: b.IMPU}.String())
		o.Header.Set("Route", sip.Address{URI: route}.String())
		o.Header.Set("P-Asserted-Service", identity.ICSI)
		o.Header.Set("P-Charging-Vector", identity.ChargingVector(domain))
		out = append(out, o)
	}

	return out
}

// Controlling is the controlling function as the participating function reaches it, over MCPTT-3.
type Controlling interface {
	// Group takes the "SIP INVITE request for controlling MCPTT function of an MCPTT group" (TS 24.379 §6.3.1.1)
	// the function sends on receipt of the INVITE of a served user's client, on the strand of the group's call.
	Group(o *Origination)
	// GroupCalls are the groups of the group calls of user, which it takes part in or is invited to: those the
	// function counts against <MaxSimultaneousCallsN6> (§10.1.1.3.1.1 step 5), the controlling function's with the
	// hop elided, since the function keeps no state of its own.
	GroupCalls(user sip.URI) []sip.URI
}

// Origination is a group call of a served user (TS 24.379 §10.1.1.3.1.1): the INVITE of its client, and the one the
// function sends the controlling function on receipt of it.
type Origination struct {
	// Request is the client's INVITE, whose session is the call's leg to the client.
	Request *ua.Request
	// Invite is the INVITE to the controlling function.
	Invite *sip.Request
	// Charging are those of the P-Charging-Vector of Request, through the IMS; of no icid-value in direct mode.
	Charging identity.Charging

	psi sip.URI
	ioi string
}

// Reply is a response of the controlling function to Invite as the function sends it to the client (§6.3.2.1.5,
// §10.1.1.3.1.1): its Warning header fields, P-Answer-State, mcptt-info and the MCPTT session identity of its
// Contact as received (steps 4, 6, 6A; §6.3.2.1.5.2 step 7), the P-Asserted-Identity the participating function's
// (step 5); through the IMS, the P-Charging-Vector of a response of the AS (Charged).
func (o *Origination) Reply(rp ua.Reply) ua.Reply {
	rp.Header = rp.Header.Clone()
	rp.Header.Del("P-Asserted-Identity")
	rp.Header.Add("P-Asserted-Identity", sip.Address{URI: o.psi}.String())
	Charged(&rp.Header, o.Charging, o.ioi, true)

	return rp
}

// Charged completes the header h of a message of the server in a dialog of charging values c, through the IMS,
// with a P-Charging-Vector when it has none: of a response, the icid-value and orig-ioi of the request and the type 3
// term-ioi ioi (TS 24.229 §5.7.2 for a terminating UA, §5.7.5.1); of a request, the icid-value of the initial request
// of the dialog and the type 3 orig-ioi ioi (§5.7.2, §5.7.3). Nothing without an icid-value: in direct mode, no
// P-Charging-Vector came [PM 14].
func Charged(h *sip.Header, c identity.Charging, ioi string, response bool) {
	if c.ICID == "" || h.Has("P-Charging-Vector") {
		return
	}

	v := c.Request(ioi)
	if response {
		v = c.Response(ioi)
	}

	h.Set("P-Charging-Vector", v)
}

// Config configures the function.
type Config struct {
	Source       settings.Source
	Bindings     Bindings
	Affiliations Affiliations
	// Router reaches the clients; without one, the function invites none.
	Router      Router
	Controlling Controlling
	// Clock is the agent's.
	Clock interface{ Now() time.Time }
	// Logger is the log of the function; slog.Default() if nil.
	Logger *slog.Logger
}

// Participating is the participating function.
type Participating struct{ cfg Config }

// New is the participating function.
func New(cfg Config) *Participating {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Participating{cfg: cfg}
}

// Originate is the "SIP INVITE request for originating participating MCPTT function" r of a served user (TS 24.379
// §6.3.1.1), originator the IMS public user identities of its originator (registrar.Resolve). Only on-demand
// prearranged group calls (§10.1.1.3.1.1) are served; another session type gets a 403.
func (p *Participating) Originate(r *ua.Request, originator []sip.URI) {
	req := r.Message()
	snap := p.cfg.Source.Current()
	host := snap.ParticipatingPSI().Host
	charging, ioi := identity.ChargingOf(req.Header), snap.ServiceConfiguration().Domain
	log := p.cfg.Logger.With(slog.String("call_id", req.Header.CallID()))

	// The refusals are logged with their reason, the warning text if any (the INVITE's transaction is logged with
	// its final response).
	reject := func(code int, reason string, header ...sip.Field) {
		log.Debug("group call refused", slog.Int("code", code), slog.String("reason", reason))

		h := sip.Header(header)
		Charged(&h, charging, ioi, true)
		_ = r.Respond(ua.Reply{Code: code, Header: h})
	}

	info, _, ok := infoOf(req)
	if !ok || info.Params.SessionType != mcpttinfo.SessionPrearranged {
		reject(403, "not a prearranged group call")
		return
	}

	params := info.Params
	clientID, _ := textOf(params.ClientID)

	// Steps 2, 2a: the calling user, of the unexpired binding of its client at the asserted identity, of its
	// registration (registrar.Resolve).
	b, ok := p.cfg.Bindings.Resolve(req, originator, clientID)
	if !ok {
		reject(404, identity.WarnUserUnknown, identity.Warning(host, identity.WarnUserUnknown))
		return
	}

	// Step 4: the speech offered (TS 24.379 §6.2.1 step 2), its codec for the controlling function to choose
	// (§10.1.1.4.2 step 2). Step 3: every user may make prearranged group calls.
	offer, ok := r.Offer()
	if !ok || !speechOffered(offer) {
		reject(488, "no speech offered")
		return
	}

	group, okGroup := uriOf(params.RequestURI)

	log = log.With(slog.String("mcptt_id", b.MCPTTID.String()))
	if okGroup {
		log = log.With(slog.String("group", group.String()))
	}

	// Step 5: the user's group calls within <MaxSimultaneousCallsN6>, this one beside them, a call of the same group
	// being the same call, which the user joins (§10.1.1.4.2 step 15). NOTE 3's exception for priority calls comes
	// with them (S3).
	if u := snap.User(b.MCPTTID); u != nil {
		if n, limited := u.N6(); limited && p.calls(b.MCPTTID, group) >= n {
			reject(486, identity.WarnTooManyCalls, identity.Warning(host, identity.WarnTooManyCalls))
			return
		}
	}

	// Steps 6, 7: no implicit affiliation, but for priority and remotely initiated calls (S3). The controlling
	// function of each configured group is this server's (step 1; NOTE 9).
	if !okGroup || snap.Group(group) == nil {
		reject(404, identity.WarnNoControlling, identity.Warning(host, identity.WarnNoControlling))
		return
	}

	// Step 5: the calling user ID.
	params.CallingUserID = mcpttinfo.URI(b.MCPTTID.String())

	body, err := mcpttinfo.Encode(info)
	if err != nil {
		reject(500, err.Error())
		return
	}

	p.cfg.Controlling.Group(&Origination{
		Request: r, Invite: p.toControlling(snap, req, offer, body), Charging: charging, psi: snap.ParticipatingPSI(), ioi: ioi,
	})
}

// calls is the number of group calls of user but that of group.
func (p *Participating) calls(user, group sip.URI) int {
	n := 0

	for _, g := range p.cfg.Controlling.GroupCalls(user) {
		if settings.Key(g) != settings.Key(group) {
			n++
		}
	}

	return n
}

// toControlling is the INVITE to the controlling function of the INVITE req of a client (TS 24.379 §6.3.2.1.3,
// §10.1.1.3.1.1 steps 2 to 8): to the controlling PSI, the Accept-Contact, Reject-Contact, Session-Expires and
// Resource-Priority of req, the participating PSI asserted, the MCPTT feature tag in its Contact and its ICSI
// asserted, the SDP offer and the resource-lists as received, the mcptt-info body given; no Answer-Mode or
// Priv-Answer-Mode.
func (p *Participating) toControlling(snap *settings.Snapshot, req *sip.Request, offer, info []byte) *sip.Request {
	psi := snap.ParticipatingPSI()

	out := sip.NewRequest("INVITE", snap.ControllingPSI())
	out.Header.Set("From", req.Header.Get("From"))
	out.Header.Set("To", sip.Address{URI: snap.ControllingPSI()}.String())
	copyFields(out, req, "Accept-Contact", "Reject-Contact", "Session-Expires", "Resource-Priority")
	out.Header.Add("Supported", "timer")
	out.Header.Add("P-Asserted-Identity", sip.Address{URI: psi}.String())
	out.Header.Add("Contact", identity.WithFeatures(sip.Address{URI: psi}, sip.FeatureSet{sip.NewFeature(identity.FeatureTag)}).String())
	out.Header.Add("P-Asserted-Service", identity.ICSI)

	bodies := []sip.BodyPart{ua.SDP(offer), sip.NewBodyPart(mcpttinfo.ContentType, info)}
	if part, ok, err := req.Content().Find(resourceLists); ok && err == nil {
		bodies = append(bodies, part)
	}

	out.SetBodies(bodies...)

	return out
}

// resourceLists is the MIME type of a resource list (RFC 5366), copied as received (TS 24.379 §6.3.2.1.3 step 7,
// §6.3.2.2.9).
const resourceLists = "application/resource-lists+xml"

// Termination is what the function does with a "SIP INVITE request for terminating participating MCPTT function":
// the INVITEs it sends the clients of the member (Router), or the response it refuses the request with.
type Termination struct {
	// Invites are the INVITEs to the clients; none when refused.
	Invites []*sip.Request
	// Unconfirmed is the 183 (Session Progress) with P-Answer-State Unconfirmed the function answers the request
	// with in automatic commencement (TS 24.379 §6.3.2.2.5.2 step 1): an unconfirmed indication to the controlling
	// function.
	Unconfirmed bool
	// Code and Header are the final response refusing the request.
	Code   int
	Header sip.Header
}

// Terminate is the "SIP INVITE request for terminating participating MCPTT function" req of the controlling
// function (TS 24.379 §10.1.1.3.2), inviting a served user: the INVITEs to the clients of the user that are
// affiliated to the calling group, in automatic commencement (§6.3.2.2.5.2). An affiliation is of a client
// (§9.2.2.2.11), and a user may be bound at several (§7.3.2 step 4a): each affiliated client is invited, the user
// one participant, of the first to accept (Router).
//
// The Answer-Mode Indication of the client's service settings (§7.3.3, §7.3.4) is not published yet (S3): every
// client is taken as auto-answer, by local policy, instead of the 480 of step 3, and its INVITE asks for Answer-Mode
// Auto (§6.3.2.2.5.2 step 8).
func (p *Participating) Terminate(req *sip.Request) Termination {
	snap := p.cfg.Source.Current()
	host := snap.ParticipatingPSI().Host

	// Step 2.
	if !focus(req) {
		return refused(403, identity.Warning(host, identity.WarnFocusNotAssigned))
	}

	// Steps 4, 5: the binding of the called user, at a client affiliated to the calling group.
	info, raw, ok := infoOf(req)
	if !ok {
		return refused(404)
	}

	id, okID := uriOf(info.Params.RequestURI)
	group, okGroup := uriOf(info.Params.CallingGroupID)

	bs := p.affiliated(id, group)
	if !okID || !okGroup || len(bs) == 0 || p.cfg.Router == nil {
		return refused(404)
	}

	// Step 6, §6.3.2.2.9 1b: an MKFC-GKTPs element is not forwarded.
	if info.Params.MKFCGKTPs != nil {
		info.Params.MKFCGKTPs = nil

		var err error
		if raw, err = mcpttinfo.Encode(info); err != nil {
			return refused(500)
		}
	}

	// §6.3.2.2.3; §6.3.2.2.5.2 steps 3 to 11: the Request-URI and To each branch's.
	out := sip.NewRequest("INVITE", bs[0].IMPU)
	out.Header.Set("From", req.Header.Get("From"))
	out.Header.Set("To", sip.Address{URI: bs[0].IMPU}.String())
	copyFields(out, req, "Accept-Contact", "Reject-Contact", "Session-Expires", "Contact", "Resource-Priority")
	out.Header.Add("Supported", "timer")
	out.Header.Add("P-Asserted-Identity", sip.Address{URI: snap.ParticipatingPSI()}.String())
	out.Header.Add("Answer-Mode", sip.AnswerModeAuto)

	bodies := []sip.BodyPart{sip.NewBodyPart(mcpttinfo.ContentType, raw)}
	if offer, ok := sessionSDP(req); ok {
		bodies = append([]sip.BodyPart{ua.SDP(offer)}, bodies...)
	}

	if part, ok, err := req.Content().Find(resourceLists); ok && err == nil {
		bodies = append(bodies, part)
	}

	out.SetBodies(bodies...)

	invites := p.cfg.Router.Route(out, bs)
	if len(invites) == 0 {
		return refused(404)
	}

	return Termination{Invites: invites, Unconfirmed: true}
}

// affiliated are the unexpired bindings of user id at its clients affiliated to group, by client ID.
func (p *Participating) affiliated(id, group sip.URI) []registrar.Binding {
	now := p.cfg.Clock.Now()

	bs := slices.DeleteFunc(slices.Clone(p.cfg.Bindings.Bindings(id)), func(b registrar.Binding) bool {
		return !now.Before(b.Expires) || !p.cfg.Affiliations.Affiliated(now, id, b.ClientID, group)
	})
	slices.SortFunc(bs, func(a, b registrar.Binding) int { return strings.Compare(a.ClientID, b.ClientID) })

	return bs
}

func refused(code int, header ...sip.Field) Termination {
	return Termination{Code: code, Header: sip.Header(header)}
}

// focus reports whether the Contact of req has the isfocus feature tag.
func focus(req *sip.Request) bool {
	cs, err := req.Header.Contacts()
	if err != nil || len(cs) != 1 {
		return false
	}

	fs, _ := cs[0].Features()

	return fs.Has(identity.FocusTag)
}

func copyFields(to, from *sip.Request, names ...string) {
	for _, n := range names {
		for _, v := range from.Header.Values(n) {
			to.Header.Add(n, v)
		}
	}
}

// infoOf is the mcptt-info of req with its parameters, decoded and as received.
func infoOf(req *sip.Request) (mcpttinfo.Info, []byte, bool) {
	part, ok, err := req.Content().Find(mcpttinfo.ContentType)
	if !ok || err != nil {
		return mcpttinfo.Info{}, nil, false
	}

	info, err := mcpttinfo.Decode(part.Body)
	if err != nil || info.Params == nil {
		return mcpttinfo.Info{}, nil, false
	}

	return info, part.Body, true
}

// sessionSDP is the SDP body of req.
func sessionSDP(req *sip.Request) ([]byte, bool) {
	part, ok, err := req.Content().Find(sdp.ContentType)
	if !ok || err != nil {
		return nil, false
	}

	return part.Body, true
}

func uriOf(c *mcpttinfo.Content) (sip.URI, bool) {
	if c == nil {
		return sip.URI{}, false
	}

	v, ok := c.URI()
	if !ok {
		return sip.URI{}, false
	}

	u, err := sip.ParseURI(v)

	return u, err == nil
}

func textOf(c *mcpttinfo.Content) (string, bool) {
	if c == nil {
		return "", false
	}

	return c.Text()
}

// speechOffered reports whether an SDP offer has a stream of MCPTT speech: an RTP audio stream not refused.
func speechOffered(b []byte) bool {
	s, err := sdp.Parse(b)
	if err != nil {
		return false
	}

	for _, m := range s.Media {
		if d, err := m.Desc(); err == nil && d.Type == sdp.Audio && m.Port() != 0 && strings.HasPrefix(d.Proto, "RTP/") {
			return true
		}
	}

	return false
}
