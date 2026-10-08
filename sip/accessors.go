package sip

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

var ErrMissingHeader = errors.New("missing header")

func missing(name string) error {
	return fmt.Errorf("%w: %s", ErrMissingHeader, name)
}

func (fs Header) first(name string) (string, error) {
	h := nameOf(name)

	for _, f := range fs {
		if h.matches(f.Name) {
			return f.Value, nil
		}
	}

	return "", missing(h.long)
}

type CSeq struct {
	Seq    uint32
	Method string
}

func (c CSeq) String() string {
	return strconv.FormatUint(uint64(c.Seq), 10) + " " + c.Method
}

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

func (fs Header) CSeq() (CSeq, error) {
	v, err := fs.first("CSeq")
	if err != nil {
		return CSeq{}, err
	}

	return ParseCSeq(v)
}

func (fs Header) CallID() string {
	return fs.Get("Call-ID")
}

func (fs Header) MaxForwards() (int, error) {
	return fs.uint("Max-Forwards", 255)
}

// MaxBreadth is the Max-Breadth header field value, a positive integer (RFC 5393 §5.3.1). A value too large for an
// int32 reads as math.MaxInt32: a proxy overwrites a value above its maximum anyway (RFC 5393 §5.3.3).
func (fs Header) MaxBreadth() (int, error) {
	v, err := fs.first("Max-Breadth")
	if err != nil {
		return 0, err
	}

	n, err := parseUint(v, math.MaxInt32)

	switch {
	case errors.Is(err, errRange):
		return math.MaxInt32, nil
	case err != nil:
		return 0, fmt.Errorf("Max-Breadth %q: %w", v, err)
	case n == 0:
		return 0, errors.New("Max-Breadth 0: not a positive integer")
	}

	return int(n), nil
}

func (fs Header) ContentLength() (int, error) {
	return fs.uint("Content-Length", maxBodySize)
}

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

func (fs Header) ContentType() string {
	return fs.Get("Content-Type")
}

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

func (fs Header) TopVia() (Via, error) {
	v, err := fs.first("Via")
	if err != nil {
		return Via{}, err
	}

	top, _ := firstListElement(v)

	return ParseVia(top)
}

func (fs *Header) SetTopVia(v Via) error {
	h := nameOf("Via")

	i := slices.IndexFunc(*fs, func(f Field) bool { return h.matches(f.Name) })
	if i < 0 {
		return missing("Via")
	}

	value := v.String()
	if _, rest := firstListElement((*fs)[i].Value); rest != "" {
		value += ", " + rest
	}

	(*fs)[i].Value = value

	return nil
}

func (fs Header) From() (Address, error) {
	return fs.address("From")
}

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

func (fs Header) Contacts() ([]Address, error) {
	return fs.Addresses("Contact")
}

func (fs Header) Routes() ([]Address, error) {
	return fs.Addresses("Route")
}

func (fs Header) RecordRoutes() ([]Address, error) {
	return fs.Addresses("Record-Route")
}

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
