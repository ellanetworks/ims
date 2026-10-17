package sip

import (
	"fmt"
	"strings"
	"time"
)

// Event is an Event value (RFC 6665 §8.2.1): an event package with its templates, and the id of the subscription.
type Event struct {
	Package   string
	Templates []string
	ID        string
	Params    Params
}

func ParseEvent(s string) (Event, error) {
	typ, ps, err := ParseTokenParams(s)
	if err != nil {
		return Event{}, fmt.Errorf("Event %w", err)
	}

	labels := strings.Split(typ, ".")
	for _, l := range labels {
		if !isTokenNoDot(l) {
			return Event{}, fmt.Errorf("Event %q: invalid event-type", s)
		}
	}

	e := Event{Package: labels[0], Templates: labels[1:]}

	for _, p := range ps {
		if strings.EqualFold(p.Name, "id") {
			if !isToken(p.Value) {
				return Event{}, fmt.Errorf("Event %q: invalid id", s)
			}

			e.ID = p.Value

			continue
		}

		e.Params = append(e.Params, p)
	}

	return e, nil
}

// Type is the event-type: the package and its templates.
func (e Event) Type() string {
	return strings.Join(append([]string{e.Package}, e.Templates...), ".")
}

// Matches reports whether two Event values name the same subscription: same event-type and id, byte by byte
// (RFC 6665 §8.2.1).
func (e Event) Matches(o Event) bool {
	return e.Type() == o.Type() && e.ID == o.ID
}

func (e Event) String() string {
	s := e.Type()
	if e.ID != "" {
		s += ";id=" + e.ID
	}

	return s + e.Params.String()
}

func (fs Header) Event() (Event, bool, error) {
	v, ok, err := fs.single("Event")
	if !ok || err != nil {
		return Event{}, ok, err
	}

	e, err := ParseEvent(v)

	return e, true, err
}

// AllowEvents are the event types of the Allow-Events header fields (RFC 6665 §8.2.2).
func (fs Header) AllowEvents() ([]string, error) {
	var out []string

	for _, v := range fs.Elements("Allow-Events") {
		for l := range strings.SplitSeq(v, ".") {
			if !isTokenNoDot(l) {
				return nil, fmt.Errorf("Allow-Events: invalid event-type %q", v)
			}
		}

		out = append(out, v)
	}

	return out, nil
}

const (
	SubscriptionActive     = "active"
	SubscriptionPending    = "pending"
	SubscriptionTerminated = "terminated"
)

// Reasons of a terminated subscription (RFC 6665 §4.1.3).
const (
	SubscriptionDeactivated = "deactivated"
	SubscriptionProbation   = "probation"
	SubscriptionRejected    = "rejected"
	SubscriptionTimeout     = "timeout"
	SubscriptionGiveUp      = "giveup"
	SubscriptionNoResource  = "noresource"
	SubscriptionInvariant   = "invariant"
)

// SubscriptionState is a Subscription-State value (RFC 6665 §8.2.3). State and Reason are lower-cased.
type SubscriptionState struct {
	State      string
	Reason     string
	Expires    *uint32
	RetryAfter *uint32
	Params     Params
}

func ParseSubscriptionState(s string) (SubscriptionState, error) {
	state, ps, err := ParseTokenParams(s)
	if err != nil {
		return SubscriptionState{}, fmt.Errorf("Subscription-State %w", err)
	}

	ss := SubscriptionState{State: strings.ToLower(state)}

	for _, p := range ps {
		switch strings.ToLower(p.Name) {
		case "reason":
			if !isToken(p.Value) {
				return SubscriptionState{}, fmt.Errorf("Subscription-State %q: invalid reason", s)
			}

			ss.Reason = strings.ToLower(p.Value)
		case "expires", "retry-after":
			v, err := parseDeltaSeconds(p.Value)
			if err != nil {
				return SubscriptionState{}, fmt.Errorf("Subscription-State %q: invalid %s", s, p.Name)
			}

			if strings.EqualFold(p.Name, "expires") {
				ss.Expires = &v
			} else {
				ss.RetryAfter = &v
			}
		default:
			ss.Params = append(ss.Params, p)
		}
	}

	return ss, nil
}

func (ss SubscriptionState) String() string {
	s := ss.State
	if ss.Reason != "" {
		s += ";reason=" + ss.Reason
	}

	if ss.Expires != nil {
		s += fmt.Sprintf(";expires=%d", *ss.Expires)
	}

	if ss.RetryAfter != nil {
		s += fmt.Sprintf(";retry-after=%d", *ss.RetryAfter)
	}

	return s + ss.Params.String()
}

// Resubscribe is what a subscriber should do once a subscription terminated with this state (RFC 6665 §4.1.3):
// whether to subscribe again, not before after. A rejected, noresource or invariant subscription is not retried; a
// deactivated or timeout one at once; the others, and unknown reasons, after their retry-after, if any.
func (ss SubscriptionState) Resubscribe() (ok bool, after time.Duration) {
	switch ss.Reason {
	case SubscriptionRejected, SubscriptionNoResource, SubscriptionInvariant:
		return false, 0
	case SubscriptionDeactivated, SubscriptionTimeout:
		return true, 0
	}

	if ss.RetryAfter != nil {
		return true, time.Duration(*ss.RetryAfter) * time.Second
	}

	return true, 0
}

func (fs Header) SubscriptionState() (SubscriptionState, bool, error) {
	v, ok, err := fs.single("Subscription-State")
	if !ok || err != nil {
		return SubscriptionState{}, ok, err
	}

	ss, err := ParseSubscriptionState(v)

	return ss, true, err
}

// SIPETag is the entity-tag a PUBLISH response assigns (RFC 3903 §11.3.1).
func (fs Header) SIPETag() (string, bool, error) {
	return fs.entityTag("SIP-ETag")
}

// SIPIfMatch is the entity-tag a PUBLISH refreshes, modifies or removes (RFC 3903 §11.3.2).
func (fs Header) SIPIfMatch() (string, bool, error) {
	return fs.entityTag("SIP-If-Match")
}

func (fs Header) entityTag(name string) (string, bool, error) {
	v, ok, err := fs.single(name)
	if !ok || err != nil {
		return "", ok, err
	}

	if !isToken(v) {
		return "", true, fmt.Errorf("%s %q: invalid entity-tag", name, v)
	}

	return v, true, nil
}
