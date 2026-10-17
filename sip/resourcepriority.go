package sip

import (
	"fmt"
	"strings"
)

// Namespaces of RFC 8101 for MCPTT: mcpttp for priority, mcpttq for queueing, values 0 (lowest) to 15.
const (
	ResourcePriorityMCPTTP = "mcpttp"
	ResourcePriorityMCPTTQ = "mcpttq"
)

// ResourcePriority is an r-value of Resource-Priority or Accept-Resource-Priority (RFC 4412 §3.1).
type ResourcePriority struct {
	Namespace string
	Priority  string
}

func ParseResourcePriority(s string) (ResourcePriority, error) {
	ns, p, ok := strings.Cut(trimWSP(s), ".")
	if !ok || !isTokenNoDot(ns) || !isTokenNoDot(p) {
		return ResourcePriority{}, fmt.Errorf("r-value %q: want namespace.priority", s)
	}

	return ResourcePriority{Namespace: ns, Priority: p}, nil
}

// RFC 4412 §3.1, RFC 6665 §8.4: token-nodot.
func isTokenNoDot(s string) bool {
	return s != "" && !strings.Contains(s, ".") && isToken(s)
}

// Equal compares case-insensitively (RFC 4412 §3.1).
func (r ResourcePriority) Equal(o ResourcePriority) bool {
	return strings.EqualFold(r.Namespace, o.Namespace) && strings.EqualFold(r.Priority, o.Priority)
}

func (r ResourcePriority) String() string {
	return r.Namespace + "." + r.Priority
}

// ResourcePriorities are the r-values of the Resource-Priority header fields. A namespace appears at most once
// (RFC 4412 §3.1): the values come with the error when it does not, for the server to answer or apply its policy.
func (fs Header) ResourcePriorities() ([]ResourcePriority, error) {
	rs, err := fs.rValues("Resource-Priority")
	if err != nil {
		return nil, err
	}

	for i, r := range rs {
		for _, o := range rs[:i] {
			if strings.EqualFold(r.Namespace, o.Namespace) {
				return rs, fmt.Errorf("Resource-Priority: namespace %s more than once", r.Namespace)
			}
		}
	}

	return rs, nil
}

// AcceptResourcePriorities are the r-values of the Accept-Resource-Priority header fields (RFC 4412 §3.2).
func (fs Header) AcceptResourcePriorities() ([]ResourcePriority, error) {
	return fs.rValues("Accept-Resource-Priority")
}

func (fs Header) rValues(name string) ([]ResourcePriority, error) {
	var out []ResourcePriority

	for _, v := range fs.Elements(name) {
		r, err := ParseResourcePriority(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		out = append(out, r)
	}

	return out, nil
}
