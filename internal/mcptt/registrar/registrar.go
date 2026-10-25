// Package registrar is the registration and service authorisation of the MCPTT server (TS 24.379 §7.3): the
// REGISTER of a client, to the server as its registrar in direct mode [PM 14] (RFC 3261 §10.3), or in the message/sip
// body of a third-party REGISTER of the S-CSCF (TS 24.229 §5.4.1.7, §5.7.1.1), authorised by one path (§7.3.2), that
// binds the MCPTT ID and MCPTT client ID to the IMS public user identity.
//
// The bindings are by IMPU and client ID: an IMPU may be of several users (TS 23.280 §8.3.1), and a client is of one
// user at one IMPU at a time, a later service authorisation of it re-binding it (TS 33.180 §5.1.3.2.1). They live in
// memory, safe for concurrent use; the requests of an IMPU come on one strand, in order, and the expiry of its
// bindings, and the NOTIFYs of its reg event subscription, run on it. A change of the settings takes effect at once
// (Revoke): a user no longer configured, or no longer at its IMPU, is logged off.
//
// Through the IMS, a Store keeps the bindings across restarts: the S-CSCF sends the third-party REGISTERs of the
// REGISTERs of the UEs only (TS 24.229 §5.4.1.7), and nothing makes it send them again to a server restarted. Restore
// puts them back, and Resubscribe subscribes to the reg event package of their IMPUs again, whose first full document
// removes those the UEs deregistered meanwhile.
//
// The state changes atomically, under the lock; what a change does to the other functions, the log-offs and the
// implicit affiliations, and to the subscriptions, follows its response, outside the lock (TS 24.229 §5.4.1.7: the
// S-CSCF waits for no more than the response).
package registrar

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/sip/xmltree"
)

// Mode is how clients reach the server.
type Mode int

const (
	// ThroughIMS: the server is an application server, told of registrations by third-party REGISTERs (TS 24.229
	// §5.7.1.1).
	ThroughIMS Mode = iota
	// Direct: the server is the registrar of its clients [PM 14], of the domain of the service configuration.
	Direct
)

// Affiliations are what service authorisation does to the affiliations of a client: the implicit affiliation to its
// user's configured groups once authorised (TS 24.379 §9.2.2.2.15), and again once they change while it stays
// authorised (Reconfigure), their removal once logged off (TS 23.280 §5.2.5).
type Affiliations interface {
	ImplicitConfigured(now time.Time, user *settings.User, client string)
	Reconfigure(now time.Time, user *settings.User, client string)
	LogOff(now time.Time, user sip.URI, client string)
}

type Config struct {
	Source settings.Source
	Mode   Mode
	// Store, if any, keeps the bindings through the IMS; StrandOf is the strand of the IMPU of a key, of its
	// REGISTERs, for those Restore puts back.
	Store    Store
	StrandOf func(ik string) *ua.Strand
	// Trusted reports whether an address is of the trust domain (TS 24.229 §4.4.1), whose reg event NOTIFYs only are
	// taken; none is if nil.
	Trusted func(netip.Addr) bool
	// Agent subscribes to the reg event package of the IMPUs through the IMS (TS 24.229 §5.7.1.1); none without, a
	// deregistration then removing all the bindings of its IMPU. Its Config.Contact is the Contact of the
	// subscriptions.
	Agent *ua.Agent
	// HomeDomain is the IMS's (TS 24.229 §5.1.1.2.1), the host of the SIP URI of a telephone number, "user=phone",
	// that the server subscribes with to the reg event package of an IMPU of a tel URI (subscribed); none if "".
	HomeDomain string
	// Tokens checks the access tokens; Unvalidated if nil.
	Tokens       TokenCheck
	Affiliations Affiliations
	// Clock is the agent's.
	Clock transaction.Clock
	// DefaultExpires is the expiration of a direct registration that asks for none (RFC 3261 §10.3 step 7); an hour
	// if zero.
	DefaultExpires time.Duration
	// Logger is the log of the registrar; slog.Default() if nil. Access tokens and bodies are never logged.
	Logger *slog.Logger
}

// Binding is the binding of a service authorisation (TS 24.379 §7.3.2 step 4).
type Binding struct {
	MCPTTID  sip.URI
	ClientID string
	// IMPU is the public user identity the client registered at: the To of its REGISTER in direct mode, of the
	// third-party REGISTER through the IMS (TS 24.229 §5.4.1.7 c), where the requests to the client go (TS 24.379
	// §6.3.2.2.5.2 step 4).
	IMPU sip.URI
	// IMPUs are the public user identities the client is served at, IMPU first: through the IMS, the implicit
	// registration set of IMPU (registrar.Resolve).
	IMPUs   []sip.URI
	Expires time.Time
	// Since is when the client was authorised for the user (TS 24.379 §7.3.2), which the refreshes of its binding keep.
	Since time.Time

	// Contacts are where the client is reached, in direct mode (RFC 3261 §10.3).
	Contacts []sip.Address

	// SCSCF is the S-CSCF of the user, the Contact of the third-party REGISTER (TS 24.229 §5.4.1.7 d): where the
	// requests the server originates to the client go.
	SCSCF sip.URI
	// Token is the "+g.3gpp.registration-token" of the third-party REGISTER (TS 24.229 §5.7.1.1): the registration of
	// the UE among those of the IMPU.
	Token string
	// Charging are the charging values of the third-party REGISTER (TS 24.229 §5.7.1.1).
	Charging Charging
}

// Charging are the charging values an application server stores of a third-party REGISTER (TS 24.229 §5.7.1.1): the
// icid-value and orig-ioi of P-Charging-Vector, P-Charging-Function-Addresses.
type Charging struct {
	ICID, OrigIOI     string
	FunctionAddresses string
}

// Registrar is the registrar and service authorisation of the server.
type Registrar struct {
	cfg Config

	mu       sync.Mutex
	bindings map[bindingKey]*binding
	// impus and users are the bindings by IMPU key and by user key, clients by client ID: a client is bound at one
	// IMPU at a time (bind). They change with bindings only (put, remove).
	impus   map[string]map[bindingKey]*binding
	users   map[string]map[bindingKey]*binding
	clients map[string]*binding
	// watches are the reg event subscriptions, by IMPU key.
	watches map[string]*watch
	// sequences are the last REGISTERs of the UEs applied through the IMS, of each IMPU and Call-ID (thirdParty).
	sequences map[seqKey]*sequence
	// closed is set by Close: no timer is armed after it.
	closed bool
	// dirty are the clients whose bindings changed since the last write to the store.
	dirty map[string]bool
	// saving serializes the writes to the store, each of the state after the one before it.
	saving sync.Mutex
}

type bindingKey struct{ impu, client string }

type binding struct {
	Binding

	key  bindingKey
	user string
	// direct: of a direct registration, its expiry that of its last contact.
	direct   bool
	contacts []contact
	timer    *ua.Timer
	// reg is the registration of the UE through the IMS, and callID and cseq those of its REGISTER; as is the
	// Request-URI of its third-party REGISTER, the AS the iFC names (TS 24.229 §5.4.1.7), which subscribes.
	reg    ueReg
	callID string
	cseq   uint32
	as     sip.URI
	// restored: put back by Restore, not refreshed nor reconciled since by a reg event document (staleIn).
	restored bool
}

// contact is a contact of a direct registration, with what RFC 3261 §10.3 step 7 orders the updates by.
type contact struct {
	addr    sip.Address
	expires time.Time
	callID  string
	cseq    uint32
}

// New is a registrar.
func New(cfg Config) *Registrar {
	if cfg.Tokens == nil {
		cfg.Tokens = Unvalidated{}
	}

	if cfg.DefaultExpires == 0 {
		cfg.DefaultExpires = time.Hour
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Registrar{
		cfg: cfg, bindings: map[bindingKey]*binding{}, impus: map[string]map[bindingKey]*binding{},
		users: map[string]map[bindingKey]*binding{}, clients: map[string]*binding{}, watches: map[string]*watch{},
		sequences: map[seqKey]*sequence{}, dirty: map[string]bool{},
	}
}

// Resolve is the served user of a request req, of P-Asserted-Identity ids, which may carry a SIP and a tel URI (RFC
// 3325 §9.1), and <mcptt-client-id> client (TS 24.379 §10.1.1.3.1.1 steps 2, 2a; §9.2.2.2.3 step 2; §6.3.6 NOTE): the
// unexpired binding of client served at one of ids. In direct mode [PM 14], ids are the From. req may be nil.
//
// Through the IMS, the asserted identity may be any public user identity of the implicit registration set of the IMPU
// the client registered at, which the S-CSCF chooses the To of the third-party REGISTER among (TS 24.229 §5.4.1.7 c):
// the P-CSCF asserts the default public user identity, the first of the P-Associated-URI, or the one the UE
// preferred (§5.2.6.3.1, §5.4.1.2.2F b). The set is that of the third-party REGISTER (thirdParty), and the IMPUs the
// reg event package reports registered (§5.4.2.1.2 step 3: a <registration> for each public user identity the user
// owns).
//
// A request whose Feature-Caps has a registration token is of the registration of that token (§5.7.1.3B, §7.9A.8),
// the initial requests for a dialog and the standalone transactions alike: the client ID of an mcptt-info is the
// UE's to claim, and the UEs of an IMPU may be of different users (TS 23.280 §8.3.1). Without a token on either side,
// the asserted identity and the client ID are all there is.
func (g *Registrar) Resolve(req *sip.Request, ids []sip.URI, client string) (Binding, bool) {
	token := TokenOf(req)
	now := g.cfg.Clock.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.clients[client]
	if b == nil || !now.Before(b.Expires) || token != "" && b.Token != "" && token != b.Token || !b.serves(ids, g.watches[b.key.impu]) {
		return Binding{}, false
	}

	return b.public(), true
}

// serves reports whether b is served at one of ids: of its IMPUs, or of those the reg event subscription w of its
// IMPU reports registered, if any.
func (b *binding) serves(ids []sip.URI, w *watch) bool {
	for _, id := range ids {
		k := settings.Key(id)
		if k == "" {
			continue
		}

		if slices.ContainsFunc(b.IMPUs, func(u sip.URI) bool { return settings.Key(u) == k }) || w != nil && w.aliases[k] {
			return true
		}
	}

	return false
}

// TokenOf is the registration token of the Feature-Caps of req (TS 24.229 §7.9A.8), "" if none or req is nil.
func TokenOf(req *sip.Request) string {
	if req == nil {
		return ""
	}

	sets, _ := req.Header.FeatureCaps()
	for _, fs := range sets {
		if ts := fs.Tokens(registrationToken); len(ts) > 0 {
			return ts[0]
		}
	}

	return ""
}

// Bound reports whether client is bound to the MCPTT ID id, at any IMPU.
func (g *Registrar) Bound(id sip.URI, client string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.clients[client]

	return b != nil && b.user == settings.Key(id)
}

// Bindings are the bindings of the MCPTT ID id, in no order.
func (g *Registrar) Bindings(id sip.URI) []Binding {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.of(settings.Key(id))
}

// Clients are the unexpired bindings, of each client its one, in no order.
func (g *Registrar) Clients() []Binding {
	now := g.cfg.Clock.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]Binding, 0, len(g.clients))

	for _, b := range g.clients {
		if now.Before(b.Expires) {
			out = append(out, b.public())
		}
	}

	return out
}

func (g *Registrar) of(user string) []Binding {
	var out []Binding

	for _, b := range g.users[user] {
		out = append(out, b.public())
	}

	return out
}

func (b *binding) public() Binding {
	out := b.Binding
	out.Contacts = nil
	out.IMPUs = slices.Clone(b.IMPUs)

	for _, c := range b.contacts {
		out.Contacts = append(out.Contacts, c.addr.Clone())
	}

	return out
}

// Handle answers a REGISTER, of OnRequest, on the strand of its IMPU.
func (g *Registrar) Handle(r *ua.Request) {
	snap := g.cfg.Source.Current()
	now := g.cfg.Clock.Now()

	if g.cfg.Mode == Direct {
		g.direct(r, snap, now)
	} else {
		g.thirdParty(r, snap, now)
	}
}

// authorisation is a service authorisation the REGISTER of a client asks for (TS 24.379 §7.3.2).
type authorisation struct {
	user   *settings.User
	client string
}

// refusal is a final response refusing a REGISTER: its code, and its warning text.
type refusal struct {
	code    int
	warning string
}

var failed = &refusal{code: 403, warning: identity.WarnServiceAuthorisationFailed}

// xmldsig is the namespace of the <Signature> of integrity protected XML (TS 24.379 §6.6.3.4.1).
const xmldsig = "http://www.w3.org/2000/09/xmldsig#"

// authorise is the service authorisation of the REGISTER reg of a client at the public user identities impus (TS 24.379 §7.3.1A, §7.3.2 steps 2
// to 3): nil without an mcptt-info body, a registration without service authorisation (§7.2.1AA; TS 33.180
// §5.1.3.2.1: the access token in any REGISTER is optional). An access token and client ID that are not both in the
// clear, or a body integrity protected, are refused: protection is off [PM 9], and without the keys of TS 33.180 the
// server can decrypt nothing (§7.3.1A step 2 c, step 3 a) nor verify a signature (§6.6.3.4.2 steps 4, 5). Encrypted
// content gets the warning text of a decryption that failed (§6.6.2.4.2 step 3), as does content of which only one
// element is encrypted, whose confidentiality protection §7.3.1A step 3 a fails, as §7.3.3 step 3 answers it.
//
// The maximum simultaneous authorisations of §7.3.2 steps 2a and 2b, of the <anyExt> of <OnNetwork> of the user
// profile and of the service configuration (TS 24.484), are not configured yet: no limit applies.
func (g *Registrar) authorise(snap *settings.Snapshot, reg *sip.Request, impus []sip.URI) (*authorisation, *refusal) {
	part, ok, err := reg.Content().Find(mcpttinfo.ContentType)

	switch {
	case err != nil:
		return nil, &refusal{code: 400}
	case !ok:
		return nil, nil
	}

	// A malformed element of another is no matter (F.1.3).
	info, _ := mcpttinfo.Decode(part.Body)

	p := info.Params
	if p == nil {
		return nil, failed
	}

	if signed(info.Other) || signed(p.Other) {
		return nil, &refusal{code: 403, warning: identity.WarnIntegrityCheckFailed}
	}

	// §7.3.1A.
	if encrypted(p.AccessToken) || encrypted(p.ClientID) {
		return nil, &refusal{code: 403, warning: identity.WarnUnableToDecrypt}
	}

	token, ok1 := p.AccessToken.Text()
	client, ok2 := p.ClientID.Text()

	client = strings.TrimSpace(client)
	if !ok1 || !ok2 || client == "" {
		return nil, failed
	}

	// Step 3.
	u, err := g.cfg.Tokens.Authorise(snap, strings.TrimSpace(token), impus)
	if err != nil {
		return nil, failed
	}

	return &authorisation{user: u, client: client}, nil
}

func encrypted(c *mcpttinfo.Content) bool { return c != nil && c.Type == mcpttinfo.Encrypted }

func signed(es []*xmltree.Element) bool {
	return slices.ContainsFunc(es, func(e *xmltree.Element) bool {
		return e.Name.Space == xmldsig && e.Name.Local == "Signature"
	})
}

// change is what a REGISTER, an expiry or a NOTIFY did to the bindings: the clients logged off, the bindings and
// sequences whose timers to arm, the client authorised to affiliate implicitly, and the IMPUs whose bindings changed,
// with the reg event subscription to make of the IMPU of a third-party REGISTER.
type change struct {
	off       []logOff
	armed     []*binding
	sequences []*sequence
	implicit  *authorisation
	impus     map[string]bool
	watch     *watch
}

func (ch *change) touch(ik string) {
	if ch.impus == nil {
		ch.impus = map[string]bool{}
	}

	ch.impus[ik] = true
}

type logOff struct {
	user   sip.URI
	client string
	cause  string
}

// bind binds the client of a to impu, as b, replacing the bindings of the client at every IMPU (TS 24.379 §7.3.2
// step 4), which re-bind (TS 33.180 §5.1.3.2.1: the IMPU changed, or another user logged onto the UE): a client is of
// one user at a time, another user's affiliations at it logged off. The user's bindings at other clients stay
// (§7.3.2 step 4a). Called with g.mu held.
func (g *Registrar) bind(b *binding, ch *change) {
	if o := g.clients[b.key.client]; o != nil {
		cause := ""
		if o.user != b.user {
			cause = causeRebound
		} else if !o.Since.IsZero() {
			b.Since = o.Since
		}

		g.unbind(o, ch, cause)
	}

	g.put(b)
	ch.armed = append(ch.armed, b)
	ch.touch(b.key.impu)
}

// The causes of a log-off, as logged.
const (
	causeDeregistered = "deregistered"
	causeExpired      = "expired"
	causeRebound      = "client bound to another user"
	causeUnauthorised = "no longer authorised"
)

// unbind removes b, logging its client off for cause, unless "". Called with g.mu held.
func (g *Registrar) unbind(b *binding, ch *change, cause string) {
	if g.bindings[b.key] != b {
		return
	}

	g.remove(b)
	ch.touch(b.key.impu)

	if b.timer != nil {
		b.timer.Stop()
	}

	if cause != "" {
		ch.off = append(ch.off, logOff{user: b.MCPTTID, client: b.ClientID, cause: cause})
	}
}

// put adds b, of a client without a binding, and remove removes b, to bindings and the indexes. Called with g.mu
// held.
func (g *Registrar) put(b *binding) {
	g.bindings[b.key] = b
	add(g.impus, b.key.impu, b)
	add(g.users, b.user, b)
	g.clients[b.key.client] = b
	g.dirty[b.key.client] = true
}

func (g *Registrar) remove(b *binding) {
	delete(g.bindings, b.key)
	drop(g.impus, b.key.impu, b)
	drop(g.users, b.user, b)
	delete(g.clients, b.key.client)
	g.dirty[b.key.client] = true
}

func add(index map[string]map[bindingKey]*binding, k string, b *binding) {
	if index[k] == nil {
		index[k] = map[bindingKey]*binding{}
	}

	index[k][b.key] = b
}

func drop(index map[string]map[bindingKey]*binding, k string, b *binding) {
	delete(index[k], b.key)

	if len(index[k]) == 0 {
		delete(index, k)
	}
}

// apply follows a change, on the strand of its IMPU, once it is answered: it saves the bindings changed, arms the
// timers of the bindings, logs the clients off and then affiliates the one authorised (TS 24.379 §9.2.2.2.15), and
// subscribes to or unsubscribes from the reg event package of the IMPUs changed.
func (g *Registrar) apply(st *ua.Strand, now time.Time, ch *change) {
	g.persist()

	g.mu.Lock()

	for _, b := range ch.armed {
		if g.bindings[b.key] != b || g.closed {
			continue
		}

		if b.timer != nil {
			b.timer.Stop()
		}

		b.timer = st.AfterFunc(b.Expires.Sub(now), func() { g.expire(st, b) })
	}

	for _, s := range ch.sequences {
		if g.sequences[s.key] == s && !g.closed {
			s.timer = st.AfterFunc(s.expires.Sub(now), func() { g.forget(s) })
		}
	}

	start, stop := g.watching(ch)

	g.mu.Unlock()

	for _, o := range ch.off {
		g.cfg.Logger.Info("logged off", slog.String("mcptt_id", o.user.String()), slog.String("client_id", o.client),
			slog.String("cause", o.cause))
		g.cfg.Affiliations.LogOff(now, o.user, o.client)
	}

	if a := ch.implicit; a != nil {
		g.cfg.Affiliations.ImplicitConfigured(now, a.user, a.client)
	}

	for _, s := range stop {
		unsubscribe(s)
	}

	if start != nil {
		g.subscribe(st, start)
	}
}

// Revoke removes the bindings whose users the current settings no longer authorise at their IMPUs, deleted or of
// the IMPU removed, logging their clients off (TS 23.280 §5.2.5), on the strands of their IMPUs; the clients of the
// others have the implicit affiliations to their users' configured groups brought to the settings
// (Affiliations.Reconfigure; TS 24.379 §9.2.2.2.15).
func (g *Registrar) Revoke() {
	g.mu.Lock()

	iks := make([]string, 0, len(g.impus))
	for ik := range g.impus {
		iks = append(iks, ik)
	}

	g.mu.Unlock()

	for _, ik := range iks {
		st := g.cfg.StrandOf(ik)
		st.Post(func() {
			snap := g.cfg.Source.Current()
			now := g.cfg.Clock.Now()

			var (
				ch   change
				kept []authorisation
			)

			g.mu.Lock()

			for _, b := range g.impus[ik] {
				if u := snap.User(b.MCPTTID); u == nil || !slices.ContainsFunc(b.IMPUs, u.HasIMPU) {
					g.unbind(b, &ch, causeUnauthorised)
				} else {
					kept = append(kept, authorisation{user: u, client: b.ClientID})
				}
			}

			g.mu.Unlock()

			g.apply(st, now, &ch)

			for _, a := range kept {
				g.cfg.Affiliations.Reconfigure(now, a.user, a.client)
			}
		})
	}
}

// Close stops the expiry timers of the bindings and sequences, which are the registrar's, not the agent's, and arms
// none after it: the agent closed, no binding expires and no client is logged off.
func (g *Registrar) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.closed = true

	for _, b := range g.bindings {
		if b.timer != nil {
			b.timer.Stop()
		}
	}

	for _, s := range g.sequences {
		if s.timer != nil {
			s.timer.Stop()
		}
	}
}

// expire removes the contacts of b whose registration expired, and b with its last (RFC 3261 §10.3; TS 24.229
// §5.7.1.1): its client is logged off.
func (g *Registrar) expire(st *ua.Strand, b *binding) {
	now := g.cfg.Clock.Now()

	var ch change

	g.mu.Lock()

	if g.bindings[b.key] == b && !g.closed {
		b.prune(now)

		if now.Before(b.Expires) {
			ch.armed = append(ch.armed, b)
		} else {
			g.unbind(b, &ch, causeExpired)
		}
	}

	g.mu.Unlock()

	g.apply(st, now, &ch)
}

// prune removes the contacts expired, and sets the expiry of b to that of its last contact, in direct mode.
func (b *binding) prune(now time.Time) {
	if !b.direct {
		return
	}

	b.contacts = slices.DeleteFunc(b.contacts, func(c contact) bool { return !now.Before(c.expires) })
	b.Expires = time.Time{}

	for _, c := range b.contacts {
		if c.expires.After(b.Expires) {
			b.Expires = c.expires
		}
	}
}

// devices is the mcptt-info of the 200 to the REGISTER that authorised user: of <multiple-devices-ind> "true" if
// more than one binding exists for it (TS 24.379 §7.3.2 step 6), none otherwise. Called with g.mu held.
func (g *Registrar) devices(user string) []sip.BodyPart {
	if len(g.of(user)) < 2 {
		return nil
	}

	b, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{
		Ext: []*xmltree.Element{mcpttinfo.ExtContent("multiple-devices-ind", mcpttinfo.Bool(true))},
	}})
	if err != nil {
		return nil
	}

	return []sip.BodyPart{sip.NewBodyPart(mcpttinfo.ContentType, b)}
}

// reject answers a REGISTER with a refusal, with its Warning (TS 24.379 §4.4.1) and the header fields of header. A
// refused service authorisation is logged at info, as Ella IMS logs a failed authentication; the others at debug.
func (g *Registrar) reject(r *ua.Request, cfg *settings.Snapshot, f *refusal, header ...sip.Field) {
	if f.warning != "" {
		header = append(header, identity.Warning(cfg.ServiceConfiguration().Domain, f.warning))
	}

	level := slog.LevelDebug
	if f.warning != "" {
		level = slog.LevelInfo
	}

	if g.cfg.Logger.Enabled(context.Background(), level) {
		attrs := []slog.Attr{slog.Int("code", f.code)}
		if to, err := r.Message().Header.To(); err == nil {
			attrs = append(attrs, slog.String("impu", to.URI.String()))
		}

		if f.warning != "" {
			attrs = append(attrs, slog.String("warning", f.warning))
		}

		g.cfg.Logger.LogAttrs(context.Background(), level, "REGISTER refused", attrs...)
	}

	_ = r.Respond(ua.Reply{Code: f.code, Header: header})
}

// authorised logs the service authorisation of the client of a at impu (TS 24.379 §7.3.2).
func (g *Registrar) authorised(a *authorisation, impu sip.URI) {
	g.cfg.Logger.Info("service authorised", slog.String("mcptt_id", a.user.ID().String()), slog.String("client_id", a.client),
		slog.String("impu", impu.String()))
}
