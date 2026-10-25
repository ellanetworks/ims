package settings

import (
	"errors"
	"strings"
)

// The kinds of error a change of settings fails with, which the API answers with 400, 409 and 404.
var (
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

// FieldError is an error of a value of settings, at Path, such as users[0].impus[1]. Conflict is of a value valid
// by itself that conflicts with the rest of the settings, such as an identity another has.
//
// Of an error of a change (ChangeError), Path is within the document the change is of, such as impus[1] of the user
// it changes, "" for the document itself; Of, if any, is what another document the change conflicts with is, such
// as the group sip:g@example.com, Path then within it.
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

// ChangeError is a change of settings refused, of Kind ErrInvalid if the changed document has an error of its own,
// ErrConflict if it conflicts with the rest of the settings only, or ErrNotFound if there is nothing to change.
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

// refused is the change refused of kind, of one problem with the document it is of.
func refused(kind error, message string) *ChangeError {
	return &ChangeError{Kind: kind, Problems: []FieldError{{Message: message, Conflict: kind == ErrConflict}}}
}
