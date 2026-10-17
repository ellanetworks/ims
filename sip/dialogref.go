package sip

import (
	"errors"
	"fmt"
	"strings"
)

// TargetDialog is a Target-Dialog value (RFC 4538 §7). The tags are those of the dialog seen by the recipient of the
// request (RFC 4538 §3).
type TargetDialog struct {
	CallID    string
	LocalTag  string
	RemoteTag string
	Params    Params
}

func ParseTargetDialog(s string) (TargetDialog, error) {
	callID, ps, err := parseCallIDParams(s)
	if err != nil {
		return TargetDialog{}, fmt.Errorf("Target-Dialog %w", err)
	}

	td := TargetDialog{CallID: callID}

	var local, remote int

	for _, p := range ps {
		switch strings.ToLower(p.Name) {
		case "local-tag":
			td.LocalTag = p.Value
			local++
		case "remote-tag":
			td.RemoteTag = p.Value
			remote++
		default:
			td.Params = append(td.Params, p)
		}
	}

	// RFC 4538 §4: without both tags, the header field is ignored.
	if local != 1 || remote != 1 || !isToken(td.LocalTag) || !isToken(td.RemoteTag) {
		return TargetDialog{}, fmt.Errorf("Target-Dialog %q: local-tag and remote-tag are required", s)
	}

	return td, nil
}

func (td TargetDialog) String() string {
	return td.CallID + ";local-tag=" + td.LocalTag + ";remote-tag=" + td.RemoteTag + td.Params.String()
}

func (fs Header) TargetDialog() (TargetDialog, bool, error) {
	v, ok, err := fs.single("Target-Dialog")
	if !ok || err != nil {
		return TargetDialog{}, ok, err
	}

	td, err := ParseTargetDialog(v)

	return td, true, err
}

// Replaces is a Replaces value (RFC 3891 §6.1). ToTag is the local tag of the dialog for the recipient, FromTag its
// remote tag (RFC 3891 §3).
type Replaces struct {
	CallID    string
	ToTag     string
	FromTag   string
	EarlyOnly bool
	Params    Params
}

func ParseReplaces(s string) (Replaces, error) {
	callID, ps, err := parseCallIDParams(s)
	if err != nil {
		return Replaces{}, fmt.Errorf("Replaces %w", err)
	}

	r := Replaces{CallID: callID}

	var to, from int

	for _, p := range ps {
		switch strings.ToLower(p.Name) {
		case "to-tag":
			r.ToTag = p.Value
			to++
		case "from-tag":
			r.FromTag = p.Value
			from++
		case "early-only":
			if p.Value != "" {
				return Replaces{}, fmt.Errorf("Replaces %q: early-only takes no value", s)
			}

			r.EarlyOnly = true
		default:
			r.Params = append(r.Params, p)
		}
	}

	// RFC 3891 §6.1: exactly one of each; a tag of 0 stands for an RFC 2543 dialog.
	if to != 1 || from != 1 || !isToken(r.ToTag) || !isToken(r.FromTag) {
		return Replaces{}, fmt.Errorf("Replaces %q: want one to-tag and one from-tag", s)
	}

	return r, nil
}

func (r Replaces) String() string {
	s := r.CallID + ";to-tag=" + r.ToTag + ";from-tag=" + r.FromTag
	if r.EarlyOnly {
		s += ";early-only"
	}

	return s + r.Params.String()
}

// Replaces is the Replaces of an INVITE, which carries at most one (RFC 3891 §3).
func (fs Header) Replaces() (Replaces, bool, error) {
	v, ok, err := fs.single("Replaces")
	if !ok || err != nil {
		return Replaces{}, ok, err
	}

	r, err := ParseReplaces(v)

	return r, true, err
}

// callid *(SEMI param), with the callid of RFC 3261 §25.1: word ["@" word].
func parseCallIDParams(s string) (string, Params, error) {
	callID, rest, _ := strings.Cut(trimWSP(s), ";")
	callID = trimWSP(callID)

	if !isCallID(callID) {
		return "", nil, errors.New("invalid callid")
	}

	ps, err := ParseParams(rest)
	if err != nil {
		return "", nil, err
	}

	return callID, ps, nil
}

func isCallID(s string) bool {
	word, host, _ := strings.Cut(s, "@")

	return isWord(word) && (!strings.Contains(s, "@") || isWord(host))
}

// RFC 3261 §25.1: word.
func isWord(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if c := s[i]; !isAlnum(c) && strings.IndexByte("-.!%*_+`'~()<>:\\\"/[]?{}", c) < 0 {
			return false
		}
	}

	return true
}
