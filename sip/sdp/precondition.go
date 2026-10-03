package sdp

import (
	"fmt"
	"slices"
	"strings"
)

const (
	Current   = "curr"
	Desired   = "des"
	Confirmed = "conf"
)

const QoS = "qos"

const (
	StrengthMandatory = "mandatory"
	StrengthOptional  = "optional"
	StrengthNone      = "none"
	StrengthFailure   = "failure"
	StrengthUnknown   = "unknown"
)

const (
	StatusE2E    = "e2e"
	StatusLocal  = "local"
	StatusRemote = "remote"
)

const (
	QoSNone     = "none"
	QoSSend     = "send"
	QoSRecv     = "recv"
	QoSSendRecv = "sendrecv"
)

var (
	strengths     = []string{StrengthMandatory, StrengthOptional, StrengthNone, StrengthFailure, StrengthUnknown}
	statusTypes   = []string{StatusE2E, StatusLocal, StatusRemote}
	qosDirections = []string{QoSNone, QoSSend, QoSRecv, QoSSendRecv}
)

type Precondition struct {
	Kind      string
	Type      string
	Strength  string
	Status    string
	Direction string
}

func ParsePrecondition(kind, value string) (Precondition, error) {
	f := strings.Fields(value)

	want := 3
	if kind == Desired {
		want = 4
	} else if kind != Current && kind != Confirmed {
		return Precondition{}, fmt.Errorf("%s: not a precondition attribute", kind)
	}

	if len(f) != want {
		return Precondition{}, fmt.Errorf("%s:%q: want %d fields, got %d", kind, value, want, len(f))
	}

	p := Precondition{Kind: kind, Type: f[0]}
	if kind == Desired {
		p.Strength, f = f[1], f[1:]
	}

	p.Status, p.Direction = f[1], f[2]

	switch {
	case !isToken(p.Type):
		return Precondition{}, fmt.Errorf("%s:%q: invalid precondition type", kind, value)
	case kind == Desired && !slices.Contains(strengths, p.Strength):
		return Precondition{}, fmt.Errorf("%s:%q: invalid strength tag", kind, value)
	case !slices.Contains(statusTypes, p.Status):
		return Precondition{}, fmt.Errorf("%s:%q: invalid status type", kind, value)
	case !slices.Contains(qosDirections, p.Direction):
		return Precondition{}, fmt.Errorf("%s:%q: invalid direction tag", kind, value)
	}

	return p, nil
}

func (p Precondition) String() string {
	if p.Kind == Desired {
		return p.Type + " " + p.Strength + " " + p.Status + " " + p.Direction
	}

	return p.Type + " " + p.Status + " " + p.Direction
}

func (p Precondition) same(o Precondition) bool {
	return p.Kind == o.Kind && p.Type == o.Type && p.Status == o.Status
}

func (ls Lines) Preconditions() ([]Precondition, error) {
	var out []Precondition

	for _, l := range ls {
		if l.Type != 'a' {
			continue
		}

		name, value := splitAttr(l.Value)
		if name != Current && name != Desired && name != Confirmed {
			continue
		}

		p, err := ParsePrecondition(name, value)
		if err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	return out, nil
}

func (ls *Lines) SetPrecondition(p Precondition) {
	for i, l := range *ls {
		if l.Type != 'a' {
			continue
		}

		name, value := splitAttr(l.Value)
		if name != p.Kind {
			continue
		}

		if q, err := ParsePrecondition(name, value); err == nil && q.same(p) {
			(*ls)[i].Value = attrValue(p.Kind, p.String())
			return
		}
	}

	ls.AddAttr(p.Kind, p.String())
}
