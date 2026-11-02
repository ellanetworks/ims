package settings

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

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

func Invalidf(format string, args ...any) error {
	return invalidf(format, args...)
}
