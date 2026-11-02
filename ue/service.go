package ue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

var (
	ErrUnknownService  = errors.New("ue: unknown service")
	ErrServiceDisabled = errors.New("ue: service disabled")
)

type Service struct {
	Name string

	ICSI string

	Features sip.FeatureSet

	RegisterFeatures sip.FeatureSet

	AcceptContact []sip.CallerPreference

	Allow []string

	Supported, Understood []string

	Match func(req *sip.Request) bool

	RegisterBodies func() []sip.BodyPart

	Disabled bool

	OnInvite func(r *ua.Request) ua.Handler

	OnRequest func(r *ua.Request)
}

var ownAllow = []string{"ACK", "BYE", "CANCEL", "NOTIFY", "OPTIONS"}

func (s *Service) features() sip.FeatureSet {
	if s.ICSI == "" {
		return slices.Clone(s.Features)
	}

	return append(sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", s.ICSI)}, s.Features...)
}

// RFC 3841 section 7.2.4
func (s *Service) registered() sip.FeatureSet {
	return append(s.features(), s.RegisterFeatures...)
}

// TS 24.229 clause 5.1.1.2.1
// TS 24.229 clause 7.2A.8.2
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

func checkServices(services []Service) error {
	for i, s := range services {
		if s.Name == "" {
			return fmt.Errorf("ue: service %d without a name", i)
		}

		if slices.ContainsFunc(services[:i], func(o Service) bool {
			return o.Name == s.Name || s.ICSI != "" && strings.EqualFold(o.ICSI, s.ICSI)
		}) {
			return fmt.Errorf("ue: service %s twice", s.Name)
		}

		if len(s.registered()) == 0 {
			return fmt.Errorf("ue: service %s without a feature tag to register", s.Name)
		}

		for _, t := range append(slices.Clone(s.Features), s.RegisterFeatures...) {
			if slices.Contains([]string{"sip.instance", "g.3gpp.icsi-ref"}, t.Tag) {
				return fmt.Errorf("ue: service %s has %s, which the UE sets itself", s.Name, t.Tag)
			}
		}

		if _, err := s.registered().Params(); err != nil {
			return fmt.Errorf("ue: service %s: %w", s.Name, err)
		}
	}

	return nil
}

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

// TS 24.229 clause 5.1.1.4.1
func (u *UE) Enable(ctx context.Context, name string) error {
	return u.setEnabled(ctx, name, true)
}

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

// RFC 3841 section 7.2
// RFC 3841 section 7.2.4
// TS 24.229 clause 5.1.2A.2
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

func (s *Service) takes(method string) bool {
	if !slices.Contains(s.Allow, method) {
		return false
	}

	if method == "INVITE" {
		return s.OnInvite != nil
	}

	return s.OnRequest != nil
}

// RFC 3261 section 8.2.2.3
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

func (u *UE) service(name string) (*Service, error) {
	i := slices.IndexFunc(u.services, func(s Service) bool { return s.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownService, name)
	}

	return &u.services[i], nil
}

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

func (u *UE) incomingInvite(_ context.Context, r *ua.Request) ua.Handler {
	s := u.dispatch(r)
	if s == nil {
		return ua.Handler{}
	}

	return s.OnInvite(r)
}

// RFC 3261 section 11.2
func (u *UE) requestReceived(_ context.Context, r *ua.Request) {
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

// RFC 3261 section 21.4.6
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

// TS 24.229 clause 5.1.2A.1.1
// TS 24.229 clause 5.1.3.1
func (u *UE) Contact(name string) (sip.Address, error) {
	s, err := u.service(name)
	if err != nil {
		return sip.Address{}, err
	}

	ps, _ := s.features().Params()
	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	return sip.Address{URI: uri, Params: ps}, nil
}

// TS 24.229 clause 5.1.2A.1.1
// TS 24.229 clause 5.1.2A.1.6
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
		impu = u.id.IMPU
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
				return fmt.Errorf("ue: service %s: Accept-Contact: %w", name, err)
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

// RFC 3261 section 12
// RFC 6665 section 4.1.2
// RFC 3515
func dialogCreating(method string) bool {
	return method == "INVITE" || method == "SUBSCRIBE" || method == "REFER"
}

func (u *UE) Agent() *ua.Agent { return u.agent }

// TS 33.203 clause 7.4
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
