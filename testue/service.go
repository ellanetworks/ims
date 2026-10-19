package testue

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// ErrUnknownService is the error of originating a request for a service the UE does not host.
var ErrUnknownService = errors.New("testue: unknown service")

// Service is an IMS communication service the UE hosts on its agent (TS 24.229 §7.2A.8, §7.9.2): MMTel, which the UE
// implements (MMTel), or one of the application's, such as an MCPTT client (TS 24.379 §7.2.1). The UE registers its
// feature tags, takes its methods and option tags, completes the initial requests it originates (Originate) and
// gives it the initial requests for it.
type Service struct {
	// ICSI identifies the service ("urn:urn-7:3gpp-service.ims.icsi.mmtel"): in the g.3gpp.icsi-ref of the Contact
	// the UE registers and of the dialogs of the service, and in the P-Preferred-Service of its requests (TS 24.229
	// §5.1.1.2.1, §5.1.2A.1.1, RFC 6050).
	ICSI string

	// Features are the other media feature tags of the service (RFC 3840 §9): audio and video for MMTel,
	// g.3gpp.mcptt for MCPTT (TS 24.379 §7.2.1).
	Features sip.FeatureSet

	// Allow are the methods of the service, beyond those of the UE: ACK, BYE, CANCEL, NOTIFY, OPTIONS.
	Allow []string

	// Supported and Understood are its option tags (ua.Config).
	Supported, Understood []string

	// OnInvite receives the initial INVITEs for the service, and returns the handler of their session (ua.Config);
	// nil rejects them with a 480.
	OnInvite func(r *ua.Request) ua.Handler

	// OnRequest receives its initial requests other than INVITE and OPTIONS, which the UE answers; nil rejects them
	// with a 405.
	OnRequest func(r *ua.Request)
}

// ownAllow are the methods the UE allows for itself: its registration, its reg event subscription (TS 24.229
// §5.1.1.3), and the dialogs of every service.
var ownAllow = []string{"ACK", "BYE", "CANCEL", "NOTIFY", "OPTIONS"}

// features is the feature set of the service in a Contact: its ICSI, then its other feature tags.
func (s *Service) features() sip.FeatureSet {
	return append(sip.FeatureSet{sip.NewFeature("+g.3gpp.icsi-ref", s.ICSI)}, s.Features...)
}

// featureSet is the feature set of the Contact the UE registers: the feature tags of its services, the ICSIs in one
// g.3gpp.icsi-ref (TS 24.229 §5.1.1.2.1, §7.2A.8.2), a tag two services share once.
func featureSet(services []Service) sip.FeatureSet {
	var out sip.FeatureSet

	for i := range services {
		for _, t := range services[i].features() {
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

// checkServices validates the services of a UE: each with its own ICSI, and feature tags the UE does not set itself.
func checkServices(services []Service) error {
	for i, s := range services {
		if s.ICSI == "" {
			return fmt.Errorf("testue: service %d without an ICSI", i)
		}

		if slices.ContainsFunc(services[:i], func(o Service) bool { return strings.EqualFold(o.ICSI, s.ICSI) }) {
			return fmt.Errorf("testue: service %s twice", s.ICSI)
		}

		for _, t := range s.Features {
			if slices.Contains([]string{"sip.instance", "g.3gpp.icsi-ref"}, t.Tag) {
				return fmt.Errorf("testue: service %s has %s, which the UE sets itself", s.ICSI, t.Tag)
			}
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

// serviceFor is the service an initial request of the network is for: of those its Reject-Contact leaves, the one
// its Accept-Contact prefers, as if each service were a contact of its own (RFC 3841 §7.2.4); the first service on a
// tie, so the one of a request that names no service. nil when there is none.
func (u *UE) serviceFor(req *sip.Request) *Service {
	// Malformed predicates are left out: they prefer nothing.
	accepts, _ := req.Header.AcceptContact()
	rejects, _ := req.Header.RejectContact()

	var (
		best *Service
		qa   float64
	)

	for i := range u.services {
		s := &u.services[i]

		fs := s.features()
		if fs.RejectedBy(rejects) {
			continue
		}

		q, ok := fs.Preference(accepts)
		if !ok {
			continue
		}

		if best == nil || q > qa {
			best, qa = s, q
		}
	}

	return best
}

// service is the service of the UE with this ICSI.
func (u *UE) service(icsi string) (*Service, error) {
	i := slices.IndexFunc(u.services, func(s Service) bool { return strings.EqualFold(s.ICSI, icsi) })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownService, icsi)
	}

	return &u.services[i], nil
}

// incomingInvite gives an initial INVITE to its service.
func (u *UE) incomingInvite(r *ua.Request) ua.Handler {
	s := u.serviceFor(r.Message())
	if s == nil || s.OnInvite == nil {
		u.event(Event{Request: r.Message()})
		_ = r.Reject(480)

		return ua.Handler{}
	}

	return s.OnInvite(r)
}

// requestReceived answers an OPTIONS with the capabilities of the UE (RFC 3261 §11.2), and gives the other initial
// requests to their service.
func (u *UE) requestReceived(r *ua.Request) {
	req := r.Message()

	if req.Method != "OPTIONS" {
		if s := u.serviceFor(req); s != nil && s.OnRequest != nil {
			s.OnRequest(r)
			return
		}
	}

	u.event(Event{Request: req.Clone()})

	rp := ua.Reply{Code: 200}
	if req.Method == "OPTIONS" {
		rp.Header = u.agent.Capabilities()
	} else {
		rp.Code = 405
		rp.Header = sip.Header{{Name: "Allow", Value: strings.Join(u.allow, ", ")}}
	}

	_ = r.Respond(rp)
}

// Contact is the Contact of the dialogs of the service icsi (TS 24.229 §5.1.2A.1.1, §5.1.3.1): the UE's protected
// address, with the feature tags of the service.
func (u *UE) Contact(icsi string) (sip.Address, error) {
	s, err := u.service(icsi)
	if err != nil {
		return sip.Address{}, err
	}

	ps, err := s.features().Params()
	if err != nil {
		return sip.Address{}, fmt.Errorf("testue: service %s: %w", icsi, err)
	}

	uri := sip.URI{Scheme: "sip", User: u.user, Host: sip.FormatHost(u.cfg.Local), Port: u.port(!u.cfg.Plain)}

	return sip.Address{URI: uri, Params: ps}, nil
}

// Originate completes an initial request the service icsi originates, "" for one of no service, before it goes to the
// agent (TS 24.229 §5.1.2A.1.1, §5.1.2A.1.6): the preloaded Route to the P-CSCF and the service route, From and
// P-Preferred-Identity with the default public user identity, P-Preferred-Service with the ICSI, the Contact of the
// service in a request that creates a dialog. It leaves the header fields req has. The agent's Prepare then adds the
// Via and the header fields of the access network and of the security agreement.
func (u *UE) Originate(req *sip.Request, icsi string) error {
	u.mu.Lock()
	closed, impu, route := u.closed, u.state.DefaultIMPU, slices.Clone(u.state.ServiceRoute)
	u.mu.Unlock()

	if closed {
		return ErrClosed
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

	if icsi == "" {
		return nil
	}

	if !req.Header.Has("P-Preferred-Service") {
		req.Header.Add("P-Preferred-Service", icsi)
	}

	if dialogCreating(req.Method) && !req.Header.Has("Contact") {
		c, err := u.Contact(icsi)
		if err != nil {
			return err
		}

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
