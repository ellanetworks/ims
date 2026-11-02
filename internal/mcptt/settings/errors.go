package settings

import (
	"errors"
	"strings"
)

var (
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

type FieldError struct {
	Path     string
	Message  string
	Conflict bool
	Of       string
}

func (e *FieldError) Error() string {
	var b strings.Builder

	for _, s := range []string{e.Of, e.Path} {
		if s != "" {
			b.WriteString(s + ": ")
		}
	}

	return b.String() + e.Message
}

type ChangeError struct {
	Kind     error
	Problems []FieldError
}

func (e *ChangeError) Error() string {
	msgs := make([]string, len(e.Problems))
	for i := range e.Problems {
		msgs[i] = e.Problems[i].Error()
	}

	return strings.Join(msgs, "; ")
}

func (e *ChangeError) Is(target error) bool { return target == e.Kind }

func refused(kind error, message string) *ChangeError {
	return &ChangeError{Kind: kind, Problems: []FieldError{{Message: message, Conflict: kind == ErrConflict}}}
}
