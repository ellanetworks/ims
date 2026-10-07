package settings

import (
	"errors"
	"fmt"
)

// The kinds of error a change of settings fails with, which the API answers with 400, 409 and 404.
var (
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

// kindError is an error whose message is for the client, of one of the kinds above.
type kindError struct {
	kind error
	msg  string
}

func (e kindError) Error() string { return e.msg }

func (e kindError) Is(target error) bool { return target == e.kind }

func invalidf(format string, args ...any) error {
	return kindError{ErrInvalid, fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) error {
	return kindError{ErrConflict, fmt.Sprintf(format, args...)}
}

// Invalidf is an invalid change of settings, for checks the settings cannot make themselves, such as against the
// configuration file.
func Invalidf(format string, args ...any) error {
	return invalidf(format, args...)
}
