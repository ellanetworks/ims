package settings

import (
	"cmp"
	"slices"
)

type IPSMGW struct {
	Number string
}

// TS 29.338 clause 6.3.1.1
type SMSRoute struct {
	ID            string
	ServiceCentre string
	Peers         []string
}

func (g IPSMGW) Validate() error {
	if g.Number != "" && (!isDigits(g.Number) || len(g.Number) > maxE164Digits) {
		return invalidf("ip_sm_gw.number must be an E.164 number of up to %d digits", maxE164Digits)
	}

	return nil
}

func (r SMSRoute) Validate() error {
	switch {
	case r.ServiceCentre != "" && (!isDigits(r.ServiceCentre) || len(r.ServiceCentre) > maxE164Digits):
		return invalidf("service_centre must be an E.164 number of up to %d digits, or empty for the default route",
			maxE164Digits)
	case len(r.Peers) == 0:
		return invalidf("peers must list at least one Diameter peer")
	}

	for i, id := range r.Peers {
		if slices.Contains(r.Peers[:i], id) {
			return invalidf("peers lists %s twice", id)
		}
	}

	return nil
}

func (r SMSRoute) describe() string {
	if r.ServiceCentre == "" {
		return "the default SMS route"
	}

	return "the SMS route of service centre " + r.ServiceCentre
}

func (s Settings) SMSRoute(id string) (SMSRoute, bool) {
	i, ok := s.smsRoute(id)
	if !ok {
		return SMSRoute{}, false
	}

	return s.SMSRoutes[i], true
}

func (s Settings) smsRoute(id string) (int, bool) {
	i := slices.IndexFunc(s.SMSRoutes, func(r SMSRoute) bool { return r.ID == id })
	return i, i >= 0
}

// TS 29.338 clause 6.3.1.1
func (s Settings) SMSCs(serviceCentre string) []Peer {
	var route *SMSRoute

	for i, r := range s.SMSRoutes {
		if r.ServiceCentre == serviceCentre {
			route = &s.SMSRoutes[i]
			break
		}

		if r.ServiceCentre == "" {
			route = &s.SMSRoutes[i]
		}
	}

	if route == nil {
		return nil
	}

	var out []Peer

	for _, id := range route.Peers {
		if i, ok := s.peer(id); ok {
			out = append(out, s.Peers[i])
		}
	}

	slices.SortStableFunc(out, func(a, b Peer) int { return cmp.Compare(a.Priority, b.Priority) })

	return out
}

func (s Settings) peerInUse(id string) error {
	for _, r := range s.SMSRoutes {
		if slices.Contains(r.Peers, id) {
			i, _ := s.peer(id)
			return conflictf("Diameter peer %s is used by %s", s.Peers[i].name(), r.describe())
		}
	}

	return nil
}

func (s Settings) validateSMSRoutes() error {
	for i, r := range s.SMSRoutes {
		if err := r.Validate(); err != nil {
			return err
		}

		if slices.ContainsFunc(s.SMSRoutes[:i], func(q SMSRoute) bool { return q.ServiceCentre == r.ServiceCentre }) {
			if r.ServiceCentre == "" {
				return conflictf("A default SMS route already exists")
			}

			return conflictf("An SMS route already has service centre %s", r.ServiceCentre)
		}

		for _, id := range r.Peers {
			j, ok := s.peer(id)
			if !ok {
				return invalidf("peers lists %s, which is not a Diameter peer", id)
			}

			switch p := s.Peers[j]; {
			case !p.Serves(ApplicationSGd):
				return conflictf("Diameter peer %s must serve sgd, as %s uses it", p.name(), r.describe())
			case p.Host == "":
				return conflictf("Diameter peer %s must have a host, as %s uses it", p.name(), r.describe())
			}
		}
	}

	return nil
}
