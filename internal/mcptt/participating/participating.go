package participating

import (
	"context"
	"log/slog"
	"net/netip"
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

// TS 24.379 clause 7.3.2
type Bindings interface {
	Resolve(req *sip.Request, ids []sip.URI, client string) (registrar.Binding, bool)
	Bindings(id sip.URI) []registrar.Binding
}

// TS 24.379 clause 9.2.2.2.11
type Affiliations interface {
	Affiliated(now time.Time, id sip.URI, clientID string, group sip.URI) bool
}

// TS 24.379 clause 6.3.2.2.5.2
// RFC 3261 section 13.2.2.4
type Router interface {
	Route(req *sip.Request, bs []registrar.Binding) []*sip.Request
}

// RFC 3261 section 10.3
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

// TS 24.229 clause 5.7.3
// TS 24.379 clause 6.3.2.2.5.2
// TS 24.229 clause 5.4.1.7
type IMS struct{ Source settings.Source }

func (r IMS) Route(req *sip.Request, bs []registrar.Binding) []*sip.Request {
	domain := r.Source.Current().ServiceConfiguration().Domain

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

type Controlling interface {
	Group(ctx context.Context, o *Origination)
	GroupCalls(user sip.URI) []sip.URI
}

// TS 24.379 clause 10.1.1.3.1.1
type Origination struct {
	Request  *ua.Request
	Invite   *sip.Request
	Charging identity.Charging

	psi   sip.URI
	ioi   string
	local netip.AddrPort
}

// TS 24.379 clause 6.3.1.1
func Relayed(snap *settings.Snapshot, r *ua.Request) *Origination {
	req := r.Message()

	return &Origination{
		Request: r, Invite: req, Charging: identity.ChargingOf(req.Header), psi: snap.ParticipatingPSI(),
		ioi: snap.ServiceConfiguration().Domain, local: req.Flow.Local,
	}
}

func (o *Origination) Group() (sip.URI, bool) {
	info, _, ok := infoOf(o.Invite)
	if !ok {
		return sip.URI{}, false
	}

	return uriOf(info.Params.RequestURI)
}

// TS 24.379 clause 6.3.2.1.5
// TS 24.379 clause 10.1.1.3.1.1
func (o *Origination) Reply(rp ua.Reply) ua.Reply {
	rp.Header = rp.Header.Clone()
	rp.Header.Del("P-Asserted-Identity")
	rp.Header.Add("P-Asserted-Identity", sip.Address{URI: o.psi}.String())
	Charged(&rp.Header, o.Charging, o.ioi, true)

	if o.local.IsValid() {
		if cs, err := rp.Header.Contacts(); err == nil && len(cs) == 1 {
			rp.Header.Set("Contact", At(cs[0], o.local).String())
		}
	}

	return rp
}

func At(a sip.Address, at netip.AddrPort) sip.Address {
	a.URI = a.URI.Clone()
	a.URI.Host, a.URI.Port = sip.FormatHost(at.Addr()), at.Port()

	return a
}

func Named(a sip.Address, psi sip.URI) sip.Address {
	a.URI = a.URI.Clone()
	a.URI.Host, a.URI.Port = psi.Host, psi.Port

	return a
}

// TS 24.229 clause 5.7.2
// TS 24.229 clause 5.7.5.1
// TS 24.229 clause 5.7.3
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

type Config struct {
	Source       settings.Source
	Bindings     Bindings
	Affiliations Affiliations
	Router       Router
	Controlling  Controlling
	Clock        interface{ Now() time.Time }
	Logger       *slog.Logger
}

type Participating struct{ cfg Config }

func New(cfg Config) *Participating {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Participating{cfg: cfg}
}

// TS 24.379 clause 6.3.1.1
// TS 24.379 clause 10.1.1.3.1.1
func (p *Participating) Originate(ctx context.Context, r *ua.Request, originator []sip.URI) {
	req := r.Message()
	snap := p.cfg.Source.Current()
	host := snap.ParticipatingPSI().Host
	charging, ioi := identity.ChargingOf(req.Header), snap.ServiceConfiguration().Domain
	log := p.cfg.Logger.With(slog.String("call_id", req.Header.CallID()))

	reject := func(code int, reason string, header ...sip.Field) {
		log.DebugContext(ctx, "group call refused", slog.Int("code", code), slog.String("reason", reason))

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

	b, ok := p.cfg.Bindings.Resolve(req, originator, clientID)
	if !ok {
		reject(404, identity.WarnUserUnknown, identity.Warning(host, identity.WarnUserUnknown))
		return
	}

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

	if u := snap.User(b.MCPTTID); u != nil {
		if n, limited := u.N6(); limited && p.calls(b.MCPTTID, group) >= n {
			reject(486, identity.WarnTooManyCalls, identity.Warning(host, identity.WarnTooManyCalls))
			return
		}
	}

	if !okGroup || snap.Group(group) == nil {
		reject(404, identity.WarnNoControlling, identity.Warning(host, identity.WarnNoControlling))
		return
	}

	params.CallingUserID = mcpttinfo.URI(b.MCPTTID.String())

	body, err := mcpttinfo.Encode(info)
	if err != nil {
		reject(500, err.Error())
		return
	}

	p.cfg.Controlling.Group(ctx, &Origination{
		Request: r, Invite: p.toControlling(snap, req, offer, body), Charging: charging, psi: snap.ParticipatingPSI(), ioi: ioi,
	})
}

func (p *Participating) calls(user, group sip.URI) int {
	n := 0

	for _, g := range p.cfg.Controlling.GroupCalls(user) {
		if settings.Key(g) != settings.Key(group) {
			n++
		}
	}

	return n
}

// TS 24.379 clause 6.3.2.1.3
// TS 24.379 clause 10.1.1.3.1.1
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

// RFC 5366
const resourceLists = "application/resource-lists+xml"

type Termination struct {
	Invites     []*sip.Request
	Unconfirmed bool
	Code        int
	Header      sip.Header
}

// TS 24.379 clause 10.1.1.3.2
// TS 24.379 clause 6.3.2.2.5.2
func (p *Participating) Terminate(req *sip.Request) Termination {
	snap := p.cfg.Source.Current()
	host := snap.ParticipatingPSI().Host

	if !Focus(req) {
		return refused(403, identity.Warning(host, identity.WarnFocusNotAssigned))
	}

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

	if info.Params.MKFCGKTPs != nil {
		info.Params.MKFCGKTPs = nil

		var err error
		if raw, err = mcpttinfo.Encode(info); err != nil {
			return refused(500)
		}
	}

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

// RFC 3840 section 9
func Focus(req *sip.Request) bool {
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
