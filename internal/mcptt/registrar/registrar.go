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
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/sip/xmltree"
	"go.opentelemetry.io/otel/trace"
)

type Mode int

const (
	ThroughIMS Mode = iota
	Direct
)

// TS 24.379 clause 9.2.2.2.15
// TS 23.280 clause 5.2.5
type Affiliations interface {
	ImplicitConfigured(ctx context.Context, now time.Time, user *settings.User, client string)
	Reconfigure(ctx context.Context, now time.Time, before, user *settings.User, client string)
	LogOff(ctx context.Context, now time.Time, user sip.URI, client string)
}

type Config struct {
	Source         settings.Source
	Mode           Mode
	Store          Store
	StrandOf       func(ik string) *ua.Strand
	Trusted        func(netip.Addr) bool
	Agent          *ua.Agent
	HomeDomain     string
	Tokens         TokenCheck
	Affiliations   Affiliations
	Clock          transaction.Clock
	DefaultExpires time.Duration
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
}

// TS 24.379 clause 7.3.2
type Binding struct {
	MCPTTID  sip.URI
	ClientID string
	IMPU     sip.URI
	IMPUs    []sip.URI
	Expires  time.Time
	Since    time.Time

	Contacts []sip.Address

	SCSCF    sip.URI
	Token    string
	Charging Charging
}

// TS 24.229 clause 5.7.1.1
type Charging struct {
	ICID, OrigIOI     string
	FunctionAddresses string
}

type Registrar struct {
	cfg      Config
	life     context.Context
	stop     context.CancelFunc
	detached tracing.Detached

	mu        sync.Mutex
	bindings  map[bindingKey]*binding
	impus     map[string]map[bindingKey]*binding
	users     map[string]map[bindingKey]*binding
	clients   map[string]*binding
	watches   map[string]*watch
	sequences map[seqKey]*sequence
	closed    bool
	dirty     map[string]bool
	saving    sync.Mutex
}

type bindingKey struct{ impu, client string }

type binding struct {
	Binding

	key      bindingKey
	user     string
	direct   bool
	contacts []contact
	timer    *ua.Timer
	reg      ueReg
	callID   string
	cseq     uint32
	as       sip.URI
	restored bool
}

// RFC 3261 section 10.3
type contact struct {
	addr    sip.Address
	expires time.Time
	callID  string
	cseq    uint32
}

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

	var (
		life context.Context
		stop context.CancelFunc = func() {}
	)

	if cfg.Agent != nil {
		life = cfg.Agent.Layer().Lifetime()
	} else {
		life, stop = context.WithCancel(context.Background())
	}

	return &Registrar{
		cfg: cfg, life: life, stop: stop, detached: tracing.NewDetached(cfg.TracerProvider, tracerName, "mcptt"), bindings: map[bindingKey]*binding{}, impus: map[string]map[bindingKey]*binding{},
		users: map[string]map[bindingKey]*binding{}, clients: map[string]*binding{}, watches: map[string]*watch{},
		sequences: map[seqKey]*sequence{}, dirty: map[string]bool{},
	}
}

// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.7.1.3B
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

// TS 24.229 clause 7.9A.8
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

func (g *Registrar) Bound(id sip.URI, client string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.clients[client]

	return b != nil && b.user == settings.Key(id)
}

func (g *Registrar) Bindings(id sip.URI) []Binding {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.of(settings.Key(id))
}

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

const tracerName = "github.com/ellanetworks/ims/internal/mcptt/registrar"

const (
	expiryName      = "registration expiry"
	resubscribeName = "reg event resubscribe"
)

func (g *Registrar) detach(cause trace.SpanContext, st *ua.Strand, d time.Duration, name string, f func(context.Context) error) *ua.Timer {
	return st.AfterFunc(d, func() {
		ctx, span := g.detached.Start(g.life, name, cause)
		tracing.End(span, f(ctx))
	})
}

func (g *Registrar) Handle(ctx context.Context, r *ua.Request) {
	snap := g.cfg.Source.Current()
	now := g.cfg.Clock.Now()

	if g.cfg.Mode == Direct {
		g.direct(ctx, r, snap, now)
	} else {
		g.thirdParty(ctx, r, snap, now)
	}
}

// TS 24.379 clause 7.3.2
type authorisation struct {
	user   *settings.User
	client string
}

type refusal struct {
	code    int
	warning string
}

var failed = &refusal{code: 403, warning: identity.WarnServiceAuthorisationFailed}

// TS 24.379 clause 6.6.3.4.1
const xmldsig = "http://www.w3.org/2000/09/xmldsig#"

// TS 24.379 clause 7.3.1A
// TS 24.379 clause 7.3.2
func (g *Registrar) authorise(snap *settings.Snapshot, reg *sip.Request, impus []sip.URI) (*authorisation, *refusal) {
	part, ok, err := reg.Content().Find(mcpttinfo.ContentType)

	switch {
	case err != nil:
		return nil, &refusal{code: 400}
	case !ok:
		return nil, nil
	}

	info, _ := mcpttinfo.Decode(part.Body)

	p := info.Params
	if p == nil {
		return nil, failed
	}

	if signed(info.Other) || signed(p.Other) {
		return nil, &refusal{code: 403, warning: identity.WarnIntegrityCheckFailed}
	}

	if encrypted(p.AccessToken) || encrypted(p.ClientID) {
		return nil, &refusal{code: 403, warning: identity.WarnUnableToDecrypt}
	}

	token, ok1 := p.AccessToken.Text()
	client, ok2 := p.ClientID.Text()

	client = strings.TrimSpace(client)
	if !ok1 || !ok2 || client == "" {
		return nil, failed
	}

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

// TS 24.379 clause 7.3.2
// TS 33.180 clause 5.1.3.2.1
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

const (
	causeDeregistered = "deregistered"
	causeExpired      = "expired"
	causeRebound      = "client bound to another user"
	causeUnauthorised = "no longer authorised"
)

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

// TS 24.379 clause 9.2.2.2.15
func (g *Registrar) apply(ctx context.Context, st *ua.Strand, now time.Time, ch *change) {
	g.persist(ctx)

	cause := trace.SpanContextFromContext(ctx)

	g.mu.Lock()

	for _, b := range ch.armed {
		if g.bindings[b.key] != b || g.closed {
			continue
		}

		if b.timer != nil {
			b.timer.Stop()
		}

		b.timer = g.detach(cause, st, b.Expires.Sub(now), expiryName, func(ctx context.Context) error {
			g.expire(ctx, st, b)
			return nil
		})
	}

	for _, s := range ch.sequences {
		if g.sequences[s.key] == s && !g.closed {
			s.timer = st.AfterFunc(s.expires.Sub(now), func() { g.forget(s) })
		}
	}

	start, stop := g.watching(ch)

	g.mu.Unlock()

	for _, o := range ch.off {
		g.cfg.Logger.InfoContext(ctx, "logged off", slog.String("mcptt_id", o.user.String()), slog.String("client_id", o.client),
			slog.String("cause", o.cause))
		g.cfg.Affiliations.LogOff(ctx, now, o.user, o.client)
	}

	if a := ch.implicit; a != nil {
		g.cfg.Affiliations.ImplicitConfigured(ctx, now, a.user, a.client)
	}

	for _, s := range stop {
		unsubscribe(ctx, s)
	}

	if start != nil {
		_ = g.subscribe(ctx, st, start)
	}
}

// TS 23.280 clause 5.2.5
// TS 24.379 clause 9.2.2.2.15
func (g *Registrar) Revoke(ctx context.Context, before, after *settings.Snapshot) {
	g.mu.Lock()

	iks := make([]string, 0, len(g.impus))
	for ik := range g.impus {
		iks = append(iks, ik)
	}

	g.mu.Unlock()

	for _, ik := range iks {
		st := g.cfg.StrandOf(ik)
		st.Post(func() {
			now := g.cfg.Clock.Now()

			var (
				ch   change
				kept []reconfiguration
			)

			g.mu.Lock()

			for _, b := range g.impus[ik] {
				switch u, was := after.User(b.MCPTTID), before.User(b.MCPTTID); {
				case u == nil || !slices.ContainsFunc(b.IMPUs, u.HasIMPU):
					g.unbind(b, &ch, causeUnauthorised)
				case now.Before(b.Expires) && was != nil:
					kept = append(kept, reconfiguration{before: was, user: u, client: b.ClientID})
				}
			}

			g.mu.Unlock()

			g.apply(ctx, st, now, &ch)

			for _, r := range kept {
				g.cfg.Affiliations.Reconfigure(ctx, now, r.before, r.user, r.client)
			}
		})
	}
}

type reconfiguration struct {
	before, user *settings.User
	client       string
}

func (g *Registrar) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.closed = true
	g.stop()

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

// RFC 3261 section 10.3
// TS 24.229 clause 5.7.1.1
func (g *Registrar) expire(ctx context.Context, st *ua.Strand, b *binding) {
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

	g.apply(ctx, st, now, &ch)
}

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

// TS 24.379 clause 7.3.2
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

// TS 24.379 clause 4.4.1
func (g *Registrar) reject(r *ua.Request, cfg *settings.Snapshot, f *refusal, header ...sip.Field) {
	if f.warning != "" {
		header = append(header, identity.Warning(cfg.ServiceConfiguration().Domain, f.warning))
	}

	level := slog.LevelDebug
	if f.warning != "" {
		level = slog.LevelInfo
	}

	ctx := r.Context()
	if g.cfg.Logger.Enabled(ctx, level) {
		attrs := []slog.Attr{slog.Int("code", f.code)}
		if to, err := r.Message().Header.To(); err == nil {
			attrs = append(attrs, slog.String("impu", to.URI.String()))
		}

		if f.warning != "" {
			attrs = append(attrs, slog.String("warning", f.warning))
		}

		g.cfg.Logger.LogAttrs(ctx, level, "REGISTER refused", attrs...)
	}

	_ = r.Respond(ua.Reply{Code: f.code, Header: header})
}

func (g *Registrar) authorised(ctx context.Context, a *authorisation, impu sip.URI) {
	g.cfg.Logger.InfoContext(ctx, "service authorised", slog.String("mcptt_id", a.user.ID().String()), slog.String("client_id", a.client),
		slog.String("impu", impu.String()))
}
