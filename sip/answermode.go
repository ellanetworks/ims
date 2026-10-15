package sip

import (
	"fmt"
	"strings"
)

// single returns the value of a header field that a message carries at most once.
func (fs Header) single(name string) (string, bool, error) {
	switch fs.Count(name) {
	case 0:
		return "", false, nil
	case 1:
		return fs.Get(name), true, nil
	}

	return "", true, fmt.Errorf("more than one %s", LongName(name))
}

const (
	AnswerModeAuto   = "Auto"
	AnswerModeManual = "Manual"
)

// AnswerMode is an Answer-Mode or Priv-Answer-Mode value (RFC 5373 §6). Unknown modes are kept, for the recipient
// to ignore.
type AnswerMode struct {
	Mode   string
	Params Params
}

func ParseAnswerMode(s string) (AnswerMode, error) {
	mode, ps, err := ParseTokenParams(s)
	if err != nil {
		return AnswerMode{}, fmt.Errorf("answer-mode-value %w", err)
	}

	return AnswerMode{Mode: mode, Params: ps}, nil
}

// Is compares the mode case-insensitively (RFC 5373 §6).
func (a AnswerMode) Is(mode string) bool {
	return strings.EqualFold(a.Mode, mode)
}

// Require reports whether the UAS must honour the mode or reject the request (RFC 5373 §7.1).
func (a AnswerMode) Require() bool {
	return a.Params.Has("require")
}

func (a AnswerMode) String() string {
	return a.Mode + a.Params.String()
}

func (fs Header) AnswerMode() (AnswerMode, bool, error) {
	return fs.answerMode("Answer-Mode")
}

func (fs Header) PrivAnswerMode() (AnswerMode, bool, error) {
	return fs.answerMode("Priv-Answer-Mode")
}

func (fs Header) answerMode(name string) (AnswerMode, bool, error) {
	v, ok, err := fs.single(name)
	if !ok || err != nil {
		return AnswerMode{}, ok, err
	}

	a, err := ParseAnswerMode(v)

	return a, true, err
}

const (
	AnswerStateConfirmed   = "Confirmed"
	AnswerStateUnconfirmed = "Unconfirmed"
)

// PAnswerState is a P-Answer-State value (RFC 4964 §7.1).
type PAnswerState struct {
	State  string
	Params Params
}

func ParsePAnswerState(s string) (PAnswerState, error) {
	state, ps, err := ParseTokenParams(s)
	if err != nil {
		return PAnswerState{}, fmt.Errorf("answer-type %w", err)
	}

	return PAnswerState{State: state, Params: ps}, nil
}

func (a PAnswerState) Is(state string) bool {
	return strings.EqualFold(a.State, state)
}

func (a PAnswerState) String() string {
	return a.State + a.Params.String()
}

func (fs Header) PAnswerState() (PAnswerState, bool, error) {
	v, ok, err := fs.single("P-Answer-State")
	if !ok || err != nil {
		return PAnswerState{}, ok, err
	}

	a, err := ParsePAnswerState(v)

	return a, true, err
}
