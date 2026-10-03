package sdp

import (
	"errors"
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
	kind = strings.ToLower(kind)
	f := strings.Fields(strings.ToLower(value))

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

func (p Precondition) split() bool {
	return p.Direction == QoSSend || p.Direction == QoSRecv
}

func preconditionKind(name string) (string, bool) {
	for _, k := range []string{Current, Desired, Confirmed} {
		if strings.EqualFold(name, k) {
			return k, true
		}
	}

	return "", false
}

func (ls Lines) Preconditions() ([]Precondition, error) {
	var (
		out  []Precondition
		errs []error
	)

	for _, l := range ls {
		if l.Type != 'a' {
			continue
		}

		name, value := splitAttr(l.Value)

		kind, ok := preconditionKind(name)
		if !ok {
			continue
		}

		p, err := ParsePrecondition(kind, value)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		out = append(out, p)
	}

	return out, errors.Join(errs...)
}

func (ls *Lines) SetPrecondition(p Precondition) {
	var (
		first = -1
		whole *Precondition
		drop  []int
	)

	for i, l := range *ls {
		q, ok := linePrecondition(l)
		if !ok || q.Kind != p.Kind || q.Type != p.Type || q.Status != p.Status {
			continue
		}

		switch {
		case p.Kind != Desired || !p.split() || q.Direction == p.Direction:
		case !q.split():
			whole = &q
		default:
			continue
		}

		if first < 0 {
			first = i
		} else {
			drop = append(drop, i)
		}
	}

	if first < 0 {
		ls.AddAttr(p.Kind, p.String())
		return
	}

	(*ls)[first].Value = attrValue(p.Kind, p.String())

	for _, i := range slices.Backward(drop) {
		*ls = slices.Delete(*ls, i, i+1)
	}

	if whole != nil && whole.Direction == QoSSendRecv {
		rest := *whole
		rest.Direction = QoSRecv

		if p.Direction == QoSRecv {
			rest.Direction = QoSSend
		}

		*ls = slices.Insert(*ls, first+1, Line{Type: 'a', Value: attrValue(rest.Kind, rest.String()), lf: (*ls)[first].lf})
	}
}

func linePrecondition(l Line) (Precondition, bool) {
	if l.Type != 'a' {
		return Precondition{}, false
	}

	name, value := splitAttr(l.Value)

	kind, ok := preconditionKind(name)
	if !ok {
		return Precondition{}, false
	}

	p, err := ParsePrecondition(kind, value)

	return p, err == nil
}

func (p Precondition) Invert() Precondition {
	switch p.Status {
	case StatusLocal:
		p.Status = StatusRemote
	case StatusRemote:
		p.Status = StatusLocal
	}

	switch p.Direction {
	case QoSSend:
		p.Direction = QoSRecv
	case QoSRecv:
		p.Direction = QoSSend
	}

	return p
}

func PreconditionsMet(ps []Precondition) bool {
	type row struct{ typ, status, dir string }

	reserved := map[row]bool{}

	for _, p := range ps {
		if p.Kind == Current {
			for _, d := range qosRows(p.Direction) {
				reserved[row{p.Type, p.Status, d}] = true
			}
		}
	}

	for _, p := range ps {
		if p.Kind != Desired {
			continue
		}

		switch p.Strength {
		case StrengthFailure, StrengthUnknown:
			return false
		case StrengthMandatory:
			for _, d := range qosRows(p.Direction) {
				if !reserved[row{p.Type, p.Status, d}] {
					return false
				}
			}
		}
	}

	return true
}

func qosRows(dir string) []string {
	switch dir {
	case QoSSendRecv:
		return []string{QoSSend, QoSRecv}
	case QoSSend, QoSRecv:
		return []string{dir}
	}

	return nil
}
