package sip

import (
	"slices"
	"strings"
)

// Field is one header field. Name is kept as received, compact forms
// included. Value is the field value after unfolding, without leading or
// trailing whitespace.
//
// A parsed Field also remembers its wire form, so == and reflect.DeepEqual
// tell it apart from a Field built with the same Name and Value.
type Field struct {
	Name  string
	Value string

	// wire is the field as received when that differs from "Name: Value"
	// (folding, unusual spacing). It is written back for as long as Name
	// and Value are unchanged, so an unmodified field keeps its bytes.
	wire *wireField
}

type wireField struct {
	text, name, value string
}

func (f Field) appendTo(b []byte) []byte {
	if f.wire != nil && f.Name == f.wire.name && f.Value == f.wire.value {
		return append(b, f.wire.text...)
	}

	b = appendText(b, f.Name)
	b = append(b, ": "...)

	return appendText(b, f.Value)
}

// compactNames are the compact header names registered with IANA.
var compactNames = [...]struct{ compact, long string }{
	{"a", "Accept-Contact"},
	{"b", "Referred-By"},
	{"c", "Content-Type"},
	{"d", "Request-Disposition"},
	{"e", "Content-Encoding"},
	{"f", "From"},
	{"i", "Call-ID"},
	{"j", "Reject-Contact"},
	{"k", "Supported"},
	{"l", "Content-Length"},
	{"m", "Contact"},
	{"o", "Event"},
	{"r", "Refer-To"},
	{"s", "Subject"},
	{"t", "To"},
	{"u", "Allow-Events"},
	{"v", "Via"},
	{"x", "Session-Expires"},
	{"y", "Identity"},
}

// headerName matches a header name in its long and compact forms,
// ignoring case.
type headerName struct {
	long, compact string
}

func nameOf(name string) headerName {
	for _, c := range compactNames {
		if strings.EqualFold(name, c.long) || strings.EqualFold(name, c.compact) {
			return headerName{long: c.long, compact: c.compact}
		}
	}

	return headerName{long: name}
}

func (h headerName) matches(name string) bool {
	return strings.EqualFold(name, h.long) || (h.compact != "" && strings.EqualFold(name, h.compact))
}

// LongName returns the long form of a compact header name, such as "Via"
// for "v". Other names are returned unchanged.
func LongName(name string) string {
	if len(name) == 1 {
		if h := nameOf(name); h.compact != "" {
			return h.long
		}
	}

	return name
}

// Header is a list of header fields in wire order. Lookups ignore case and
// find compact forms by their long name and the reverse.
//
// Header is a slice: a copy shares its fields. Use Clone, or Request.Clone
// and Response.Clone, before changing a copy.
type Header []Field

// Values returns the value of every field with this name, in wire order.
// A field holding a comma-separated list is returned as one value.
func (fs Header) Values(name string) []string {
	var out []string

	h := nameOf(name)

	for _, f := range fs {
		if h.matches(f.Name) {
			out = append(out, f.Value)
		}
	}

	return out
}

// Get returns the value of the first field with this name, or "".
func (fs Header) Get(name string) string {
	h := nameOf(name)

	for _, f := range fs {
		if h.matches(f.Name) {
			return f.Value
		}
	}

	return ""
}

// Has reports whether a field with this name is present.
func (fs Header) Has(name string) bool {
	return fs.Count(name) > 0
}

// Count returns the number of fields with this name.
func (fs Header) Count(name string) int {
	n := 0
	h := nameOf(name)

	for _, f := range fs {
		if h.matches(f.Name) {
			n++
		}
	}

	return n
}

// Elements returns the elements of a list header (RFC 3261 §7.3.1) across all
// its fields, split on commas. Empty elements are dropped.
func (fs Header) Elements(name string) []string {
	var out []string

	for _, v := range fs.Values(name) {
		for _, e := range SplitList(v) {
			if e != "" {
				out = append(out, e)
			}
		}
	}

	return out
}

// Add appends a field.
func (fs *Header) Add(name, value string) {
	*fs = append(*fs, Field{Name: name, Value: value})
}

// Prepend inserts a field before all others, as a proxy does with Via and
// Record-Route.
func (fs *Header) Prepend(name, value string) {
	*fs = slices.Insert(*fs, 0, Field{Name: name, Value: value})
}

// Set replaces the value of the first field with this name and removes the
// others. The field keeps its position and the name it was received with.
// If there is none, the field is appended.
func (fs *Header) Set(name, value string) {
	h := nameOf(name)

	i := slices.IndexFunc(*fs, func(f Field) bool { return h.matches(f.Name) })
	if i < 0 {
		fs.Add(name, value)
		return
	}

	(*fs)[i].Value = value
	tail := (*fs)[i+1:]
	*fs = append((*fs)[:i+1], slices.DeleteFunc(tail, func(f Field) bool { return h.matches(f.Name) })...)
}

// Del removes every field with this name and returns how many there
// were.
func (fs *Header) Del(name string) int {
	h := nameOf(name)
	n := len(*fs)
	*fs = slices.DeleteFunc(*fs, func(f Field) bool { return h.matches(f.Name) })

	return n - len(*fs)
}

// PopFirst removes the first element of a list header, such as the
// top Via or the top Route. When the first field holds several elements,
// only the first one is removed from it. It returns the removed element.
func (fs *Header) PopFirst(name string) (string, bool) {
	h := nameOf(name)

	i := slices.IndexFunc(*fs, func(f Field) bool { return h.matches(f.Name) })
	if i < 0 {
		return "", false
	}

	// Empty elements, as in "Route: , <sip:p;lr>", are dropped with the
	// element that follows them.
	for {
		first, rest := firstListElement((*fs)[i].Value)
		if rest == "" {
			*fs = slices.Delete(*fs, i, i+1)
		} else {
			(*fs)[i].Value = rest
		}

		if first != "" {
			return first, true
		}

		if i = slices.IndexFunc(*fs, func(f Field) bool { return h.matches(f.Name) }); i < 0 {
			return "", false
		}
	}
}

// Clone returns a copy of the list that shares no storage with fs.
func (fs Header) Clone() Header {
	return slices.Clone(fs)
}

func (fs Header) appendTo(b []byte) []byte {
	for _, f := range fs {
		b = f.appendTo(b)
		b = append(b, "\r\n"...)
	}

	return b
}
