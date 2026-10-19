package testue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// ErrUnknownService is the error of naming a service the UE does not host; ErrServiceDisabled of originating a
// request for one disabled.
var (
	ErrUnknownService  = errors.New("testue: unknown service")
	ErrServiceDisabled = errors.New("testue: service disabled")
)

// Service is a service the UE hosts on its agent: an IMS communication service with its ICSI (TS 24.229 §7.2A.8,
// §7.9.2), such as MMTel, which the UE implements (MMTel) or an MCPTT client of the application's (TS 24.379 §7.2.1),
// or one without, as SMS over IP (TS 24.341, SMS). The UE registers its feature tags while it is enabled, takes its
// methods and option tags, completes the initial requests it originates (Originate) and gives it the initial
// requests for it.
type Service struct {
	// Name identifies the service in the UE: "mmtel", "sms", "mcptt".
	Name string

	// ICSI identifies an IMS communication service ("urn:urn-7:3gpp-service.ims.icsi.mmtel"): in the
	// g.3gpp.icsi-ref of the Contact the UE registers and of the dialogs of the service, and in the
	// P-Preferred-Service of its requests (TS 24.229 §5.1.1.2.1, §5.1.2A.1.1, RFC 6050). Empty for a service without.
	ICSI string

	// Features are the other media feature tags of the service, in the Contact the UE registers and in those of the
	// dialogs of the service (RFC 3840 §9): audio and video for MMTel, g.3gpp.mcptt for MCPTT (TS 24.379 §7.2.1).
	Features sip.FeatureSet

	// RegisterFeatures are feature tags of the Contact the UE registers only: g.3gpp.smsip (TS 24.341 §5.3.1.2), the
	// capability of a service without dialogs.
	RegisterFeatures sip.FeatureSet

	// AcceptContact are the caller preferences of the initial requests of the service (RFC 3841), which Originate
	// adds to those without an Accept-Contact: "*;+g.3gpp.mcptt;require;explicit" (TS 24.379 §10.1.1.2.1.1).
	AcceptContact []sip.CallerPreference

	// Allow are the methods of the service, beyond those of the UE: ACK, BYE, CANCEL, NOTIFY, OPTIONS.
	Allow []string

	// Supported are option tags the service supports, which Originate advertises in its initial requests;
	// Understood those it accepts in Require without advertising them. The UE refuses with a 420 an initial request
	// for the service that requires an option tag only another service has.
	Supported, Understood []string

	// Match tells whether the service is the one for an initial request by its contents, when its Accept-Contact
	// prefers no service over the others (TS 24.229 §5.1.2A.2): the media of its SDP, its Content-Type. Nil matches
	// nothing.
	Match func(req *sip.Request) bool

	// RegisterBodies are the bodies of the service in each REGISTER of the UE while the service is enabled: an
	// application/vnd.3gpp.mcptt-info+xml for MCPTT service authorisation (TS 24.379 §7.2.1).
	RegisterBodies func() []sip.BodyPart

	// Disabled is the initial state of the service: not registered, taking no requests, until UE.Enable.
	Disabled bool

	// OnInvite receives the initial INVITEs for the service, and returns the handler of their session (ua.Config);
	// without it, or INVITE in Allow, the service takes none.
	OnInvite func(r *ua.Request) ua.Handler

	// OnRequest receives its initial requests of the other methods of Allow but OPTIONS, which the UE answers;
	// without it, the service takes none.
	OnRequest func(r *ua.Request)
}

// ownAllow are the methods the UE allows for itself: its registration, its reg event subscription (TS 24.229
// §5.1.1.3), and the dialogs of every service.
var ownAllow = []string{"ACK", "BYE", "CANCEL", "NOTIFY", "OPTIONS"}

// features is the feature set of the service in the Contact of its dialogs: its ICSI, then its other feature tags.
func (s *Service) features() sip.FeatureSet {
	if s.ICSI == "" {
		return slices.Clone(s.Features)
	}

	return append(sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", s.ICSI)}, s.Features...)
}

// registered is the feature set of the service in the Contact the UE registers, against which the S-CSCF matched the
// requests for it (RFC 3841 §7.2.4).
func (s *Service) registered() sip.FeatureSet {
	return append(s.features(), s.RegisterFeatures...)
}

// featureSet is the feature set of the Contact the UE registers: the feature tags of its services, the ICSIs in one
// g.3gpp.icsi-ref (TS 24.229 §5.1.1.2.1, §7.2A.8.2), a tag two services share once.
func featureSet(services []*Service) sip.FeatureSet {
	var out sip.FeatureSet

	for _, s := range services {
		for _, t := range s.registered() {
			j := slices.IndexFunc(out, func(o sip.FeatureTerm) bool { return o.Tag == t.Tag })
			if j < 0 {
				out = append(out, sip.FeatureTerm{Tag: t.Tag, Values: slices.Clone(t.Values)})
				continue
			}

			for _, v := range t.Values {
				if !slices.Contains(out[j].Values, v) {
					out[j].Values = append(out[j].Values, v)
				}
			}
		}
	}

	return out
}

// checkServices validates the services of a UE: each with its own name and ICSI, feature tags the UE does not set
// itself, and something to register.
func checkServices(services []Service) error {
	for i, s := range services {
		if s.Name == "" {
			return fmt.Errorf("testue: service %d without a name", i)
		}

		if slices.ContainsFunc(services[:i], func(o Service) bool {
			return o.Name == s.Name || s.ICSI != "" && strings.EqualFold(o.ICSI, s.ICSI)
		}) {
			return fmt.Errorf("testue: service %s twice", s.Name)
		}

		if len(s.registered()) == 0 {
			return fmt.Errorf("testue: service %s without a feature tag to register", s.Name)
		}

		for _, t := range append(slices.Clone(s.Features), s.RegisterFeatures...) {
			if slices.Contains([]string{"sip.instance", "g.3gpp.icsi-ref"}, t.Tag) {
				return fmt.Errorf("testue: service %s has %s, which the UE sets itself", s.Name, t.Tag)
			}
		}

		if _, err := s.registered().Params(); err != nil {
			return fmt.Errorf("testue: service %s: %w", s.Name, err)
		}
	}

	return nil
}

// union is the methods or option tags of the UE and its services, each once.
func union(own []string, services []Service, of func(s *Service) []string) []string {
	out := slices.Clone(own)

	for i := range services {
		for _, v := range of(&services[i]) {
			if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, v) }) {
				out = append(out, v)
			}
		}
	}

	return out
}

// enabled are the services of the UE enabled now, in order.
func (u *UE) enabled() []*Service {
	u.mu.Lock()
	defer u.mu.Unlock()

	var out []*Service

	for i := range u.services {
		if s := &u.services[i]; !u.disabled[s.Name] {
			out = append(out, s)
		}
	}

	return out
}

// Enable enables the service name, which re-registers a registered UE with its feature tags (TS 24.229 §5.1.1.4.1:
// the ICSIs it uses changed).
func (u *UE) Enable(ctx context.Context, name string) error {
	return u.setEnabled(ctx, name, true)
}

// Disable disables the service name, which re-registers a registered UE without its feature tags: an MCPTT client
// logging off (TS 24.379 §7.2.1 NOTE 1). The dialogs of the service go on.
func (u *UE) Disable(ctx context.Context, name string) error {
	return u.setEnabled(ctx, name, false)
}

func (u *UE) setEnabled(ctx context.Context, name string, on bool) error {
	if _, err := u.service(name); err != nil {
		return err
	}

	u.mu.Lock()
	changed := u.disabled[name] == on
	u.disabled[name] = !on
	registered := u.state.Registered
	u.mu.Unlock()

	if !changed || !registered {
		return nil
	}

	return u.Reregister(ctx)
}

// serviceFor is the service an initial request of the network is for (RFC 3841 §7.2): of the enabled services that
// take its method, those its Reject-Contact leaves, the one its Accept-Contact prefers, as if each service were a
// contact of its own; of those it prefers alike, the first whose Match takes the request, else the first (TS 24.229
// §5.1.2A.2). Without one, code is the response: 400 for predicates that do not parse, which would otherwise prefer
// nothing they meant to; 405 when no service takes the method; 480 when the preferences leave none (RFC 3841 §7.2.4).
func (u *UE) serviceFor(req *sip.Request) (*Service, int) {
	accepts, err := req.Header.AcceptContact()
	if err != nil {
		return nil, 400
	}

	rejects, err := req.Header.RejectContact()
	if err != nil {
		return nil, 400
	}

	var (
		best  []*Service
		qa    float64
		taken bool
	)

	for _, s := range u.enabled() {
		if !s.takes(req.Method) {
			continue
		}

		taken = true

		fs := s.registered()
		if fs.RejectedBy(rejects) {
			continue
		}

		q, ok := fs.Preference(accepts)

		switch {
		case !ok:
		case len(best) == 0 || q > qa:
			best, qa = []*Service{s}, q
		case q == qa:
			best = append(best, s)
		}
	}

	switch {
	case len(best) > 0:
		if i := slices.IndexFunc(best, func(s *Service) bool { return s.Match != nil && s.Match(req) }); i >= 0 {
			return best[i], 0
		}

		return best[0], 0
	case taken:
		return nil, 480
	}

	return nil, 405
}

// takes reports whether the service takes initial requests of the method.
func (s *Service) takes(method string) bool {
	if !slices.Contains(s.Allow, method) {
		return false
	}

	if method == "INVITE" {
		return s.OnInvite != nil
	}

	return s.OnRequest != nil
}

// unsupported are the option tags the request requires that the service has not, though another service has
// (RFC 3261 §8.2.2.3): the agent accepted them for the other.
func (u *UE) unsupported(s *Service, req *sip.Request) []string {
	var out []string

	required, _ := req.Header.OptionTags("Require")

	for _, tag := range required {
		own := slices.ContainsFunc(append(slices.Clone(s.Supported), s.Understood...), func(t string) bool {
			return strings.EqualFold(t, tag)
		})

		if !own && slices.ContainsFunc(u.serviceTags, func(t string) bool { return strings.EqualFold(t, tag) }) {
			out = append(out, tag)
		}
	}

	return out
}

// service is the service of the UE named name.
func (u *UE) service(name string) (*Service, error) {
	i := slices.IndexFunc(u.services, func(s Service) bool { return s.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownService, name)
	}

	return &u.services[i], nil
}

// dispatch finds the service of an initial request, refusing the request without one, or with an option tag the
// service lacks.
func (u *UE) dispatch(r *ua.Request) *Service {
	s, code := u.serviceFor(r.Message())
	if s == nil {
		u.refuse(r, code)
		return nil
	}

	if tags := u.unsupported(s, r.Message()); len(tags) > 0 {
		u.event(Event{Request: r.Message().Clone()})
		_ = r.Respond(ua.Reply{Code: 420, Header: sip.Header{{Name: "Unsupported", Value: strings.Join(tags, ", ")}}})

		return nil
	}

	return s
}

// incomingInvite gives an initial INVITE to its service.
func (u *UE) incomingInvite(r *ua.Request) ua.Handler {
	s := u.dispatch(r)
	if s == nil {
		return ua.Handler{}
	}

	return s.OnInvite(r)
}

// requestReceived answers an OPTIONS with the capabilities of the UE (RFC 3261 §11.2), and gives the other initial
// requests to their service.
func (u *UE) requestReceived(r *ua.Request) {
	req := r.Message()

	if req.Method == "OPTIONS" {
		u.event(Event{Request: req.Clone()})
		_ = r.Respond(ua.Reply{Code: 200, Header: u.agent.Capabilities()})

		return
	}

	if s := u.dispatch(r); s != nil {
		s.OnRequest(r)
	}
}

// refuse answers an initial request no service takes: a 405 with the methods the UE allows but that one, which it
// does not outside a dialog (RFC 3261 §21.4.6); the 480 of the preferences that leave no service; the 400 of
// predicates that do not parse.
func (u *UE) refuse(r *ua.Request, code int) {
	req := r.Message()
	u.event(Event{Request: req.Clone()})

	rp := ua.Reply{Code: code}
	if code == 405 {
		allow := slices.DeleteFunc(slices.Clone(u.allow), func(m string) bool { return m == req.Method })
		rp.Header = sip.Header{{Name: "Allow", Value: strings.Join(allow, ", ")}}
	}

	_ = r.Respond(rp)
}

// Contact is the Contact of the dialogs of the service name (TS 24.229 §5.1.2A.1.1, §5.1.3.1): the UE's protected
// address, with the feature tags of the service.
func (u *UE) Contact(name string) (sip.Address, error) {
	s, err := u.service(name)
	if err != nil {
		return sip.Address{}, err
	}

	// checkServices made sure they encode.
	ps, _ := s.features().Params()
	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	return sip.Address{URI: uri, Params: ps}, nil
}

// Originate completes an initial request the service name originates, before it goes to the agent (TS 24.229
// §5.1.2A.1.1, §5.1.2A.1.6): the preloaded Route to the P-CSCF and the service route, From and P-Preferred-Identity
// with the default public user identity, P-Preferred-Service with the ICSI, the Accept-Contact and Supported of the
// service, its Contact in a request that creates a dialog. It leaves the header fields req has. The agent's Prepare
// then adds the Via and the header fields of the access network and of the security agreement.
func (u *UE) Originate(req *sip.Request, name string) error {
	s, err := u.service(name)
	if err != nil {
		return err
	}

	u.mu.Lock()
	closed, disabled := u.closed, u.disabled[name]
	impu, route := u.state.DefaultIMPU, slices.Clone(u.state.ServiceRoute)
	u.mu.Unlock()

	switch {
	case closed:
		return ErrClosed
	case disabled:
		return fmt.Errorf("%w: %s", ErrServiceDisabled, name)
	}

	if impu == "" {
		impu = u.id.impu
	}

	if !req.Header.Has("Route") {
		preloaded, err := u.outboundRoute()
		if err != nil {
			return err
		}

		req.Header.Add("Route", "<"+preloaded.String()+">")

		for _, r := range route {
			req.Header.Add("Route", r)
		}
	}

	if !req.Header.Has("From") {
		req.Header.Add("From", "<"+impu+">")
	}

	if !req.Header.Has("P-Preferred-Identity") {
		req.Header.Add("P-Preferred-Identity", "<"+impu+">")
	}

	if s.ICSI != "" && !req.Header.Has("P-Preferred-Service") {
		req.Header.Add("P-Preferred-Service", s.ICSI)
	}

	if !req.Header.Has("Accept-Contact") {
		for _, p := range s.AcceptContact {
			v, err := p.Value()
			if err != nil {
				return fmt.Errorf("testue: service %s: Accept-Contact: %w", name, err)
			}

			req.Header.Add("Accept-Contact", v)
		}
	}

	for _, tag := range s.Supported {
		if !req.Header.HasOptionTag("Supported", tag) {
			req.Header.Add("Supported", tag)
		}
	}

	if dialogCreating(req.Method) && !req.Header.Has("Contact") {
		c, _ := u.Contact(name)
		req.Header.Add("Contact", c.String())
	}

	return nil
}

// dialogCreating reports whether a request creates a dialog (RFC 3261 §12, RFC 6665 §4.1.2, RFC 3515).
func dialogCreating(method string) bool {
	return method == "INVITE" || method == "SUBSCRIBE" || method == "REFER"
}

// Agent is the agent of the UE, on which its services run: their sessions, subscriptions and requests.
func (u *UE) Agent() *ua.Agent { return u.agent }

// HoldSAs keeps the security associations a request or a session setup of a service uses until release, when the UE
// re-registers in the meantime (TS 33.203 §7.4). Calling release more than once has no effect.
func (u *UE) HoldSAs() (release func()) {
	if u.cfg.Plain {
		return func() {}
	}

	u.mu.Lock()
	u.pending++
	u.mu.Unlock()

	var once bool

	return func() {
		u.mu.Lock()
		defer u.mu.Unlock()

		if once {
			return
		}

		once = true
		u.pending--
		u.dropOldLocked()
	}
}
