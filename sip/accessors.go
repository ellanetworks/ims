package sip

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrMissingHeader is returned, wrapped, by typed accessors when the
// header is absent.
var ErrMissingHeader = errors.New("missing header")

func missing(name string) error {
	return fmt.Errorf("%w: %s", ErrMissingHeader, name)
}

// first returns the value of the first field with this name.
func (fs Header) first(name string) (string, error) {
	h := nameOf(name)

	for _, f := range fs {
		if h.matches(f.Name) {
			return f.Value, nil
		}
	}

	return "", missing(h.long)
}

// CSeq is the CSeq header field (RFC 3261 §20.16).
type CSeq struct {
	Seq    uint32
	Method string
}

// String returns the CSeq value, such as "1 INVITE".
func (c CSeq) String() string {
	return strconv.FormatUint(uint64(c.Seq), 10) + " " + c.Method
}

// ParseCSeq parses a CSeq value. The number must fit in 32 bits
// (RFC 3261 §20.16). The tighter bound of §8.1.1.5, below 2^31, applies
// only to the first number a UA chooses for a dialog.
func ParseCSeq(s string) (CSeq, error) {
	s = trimWSP(s)

	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return CSeq{}, fmt.Errorf("CSeq %q: missing method", s)
	}

	num, method := s[:i], trimLeftWSP(s[i:])
	if !isToken(method) {
		return CSeq{}, fmt.Errorf("CSeq %q: invalid method", s)
	}

	n, err := parseUint(num, 1<<32-1)
	if err != nil {
		return CSeq{}, fmt.Errorf("CSeq %q: invalid sequence number", s)
	}

	return CSeq{Seq: uint32(n), Method: method}, nil
}

// CSeq returns the CSeq header.
func (fs Header) CSeq() (CSeq, error) {
	v, err := fs.first("CSeq")
	if err != nil {
		return CSeq{}, err
	}

	return ParseCSeq(v)
}

// CallID returns the Call-ID, or "" when there is none.
func (fs Header) CallID() string {
	return fs.Get("Call-ID")
}

// MaxForwards returns the Max-Forwards value, from 0 to 255.
func (fs Header) MaxForwards() (int, error) {
	return fs.uint("Max-Forwards", 255)
}

// ContentLength returns the Content-Length value.
func (fs Header) ContentLength() (int, error) {
	return fs.uint("Content-Length", maxBodySize)
}

// Expires returns the Expires value in seconds (RFC 3261 §20.19). Values
// above 2^32-1 are read as 2^32-1, as §20.19 allows.
func (fs Header) Expires() (uint32, error) {
	v, err := fs.first("Expires")
	if err != nil {
		return 0, err
	}

	if !isDigits(v) {
		return 0, fmt.Errorf("Expires %q: not a number of seconds", v)
	}

	n, err := parseUint(v, 1<<32-1)
	if err != nil {
		return 1<<32 - 1, nil
	}

	return uint32(n), nil
}

func (fs Header) uint(name string, maxValue uint64) (int, error) {
	v, err := fs.first(name)
	if err != nil {
		return 0, err
	}

	n, err := parseUint(v, maxValue)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", name, v, err)
	}

	return int(n), nil
}

// ContentType returns the Content-Type value, or "" when there is none.
func (fs Header) ContentType() string {
	return fs.Get("Content-Type")
}

// Vias returns every via-parm, top first.
func (fs Header) Vias() ([]Via, error) {
	var out []Via

	for _, v := range fs.Values("Via") {
		for _, e := range SplitList(v) {
			via, err := ParseVia(e)
			if err != nil {
				return nil, err
			}

			out = append(out, via)
		}
	}

	if out == nil {
		return nil, missing("Via")
	}

	return out, nil
}

// TopVia returns the first via-parm.
func (fs Header) TopVia() (Via, error) {
	v, err := fs.first("Via")
	if err != nil {
		return Via{}, err
	}

	top, _ := firstListElement(v)

	return ParseVia(top)
}

// From returns the From header.
func (fs Header) From() (Address, error) {
	return fs.address("From")
}

// To returns the To header.
func (fs Header) To() (Address, error) {
	return fs.address("To")
}

func (fs Header) address(name string) (Address, error) {
	v, err := fs.first(name)
	if err != nil {
		return Address{}, err
	}

	a, err := ParseAddress(v)
	if err != nil {
		return Address{}, err
	}

	if a.Star {
		return Address{}, fmt.Errorf("%s: unexpected *", name)
	}

	return a, nil
}

// Addresses returns the addresses of every field with this name, split on
// commas, such as Contact, Route, Path or P-Asserted-Identity. It returns
// nil when there is none.
func (fs Header) Addresses(name string) ([]Address, error) {
	var out []Address

	for _, v := range fs.Values(name) {
		as, err := ParseAddressList(v)
		if err != nil {
			return nil, err
		}

		out = append(out, as...)
	}

	return out, nil
}

// Contacts returns the Contact addresses.
func (fs Header) Contacts() ([]Address, error) {
	return fs.Addresses("Contact")
}

// Routes returns the Route addresses, top first.
func (fs Header) Routes() ([]Address, error) {
	return fs.Addresses("Route")
}

// RecordRoutes returns the Record-Route addresses, top first.
func (fs Header) RecordRoutes() ([]Address, error) {
	return fs.Addresses("Record-Route")
}

// SetToTag adds a tag parameter to the To header when it has none, as a
// UAS does in its responses (RFC 3261 §8.2.6.2). An existing tag, as in a
// response to a request within a dialog, is left unchanged.
func (fs *Header) SetToTag(tag string) error {
	to, err := fs.To()
	if err != nil {
		return err
	}

	if to.Params.Has("tag") {
		return nil
	}

	to.Params.Set("tag", tag)
	fs.Set("To", to.String())

	return nil
}
