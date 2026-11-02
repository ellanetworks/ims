package sip

import (
	"fmt"
	"strings"
)

const (
	AnswerModeAuto   = "Auto"
	AnswerModeManual = "Manual"
)

// RFC 5373 section 6
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

// RFC 5373 section 6
func (a AnswerMode) Is(mode string) bool {
	return strings.EqualFold(a.Mode, mode)
}

// RFC 5373 section 7.1
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

// RFC 4964 section 7.1
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
