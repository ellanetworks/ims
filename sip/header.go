package sip

import (
	"slices"
	"strings"
)

type Field struct {
	Name  string
	Value string

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

func LongName(name string) string {
	if len(name) == 1 {
		if h := nameOf(name); h.compact != "" {
			return h.long
		}
	}

	return name
}

type Header []Field

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

func (fs Header) Get(name string) string {
	h := nameOf(name)

	for _, f := range fs {
		if h.matches(f.Name) {
			return f.Value
		}
	}

	return ""
}

func (fs Header) Has(name string) bool {
	return fs.Count(name) > 0
}

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

func (fs *Header) Add(name, value string) {
	*fs = append(*fs, Field{Name: name, Value: value})
}

func (fs *Header) Prepend(name, value string) {
	*fs = slices.Insert(*fs, 0, Field{Name: name, Value: value})
}

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

func (fs *Header) Del(name string) int {
	h := nameOf(name)
	n := len(*fs)
	*fs = slices.DeleteFunc(*fs, func(f Field) bool { return h.matches(f.Name) })

	return n - len(*fs)
}

func (fs *Header) PopFirst(name string) (string, bool) {
	h := nameOf(name)

	i := slices.IndexFunc(*fs, func(f Field) bool { return h.matches(f.Name) })
	if i < 0 {
		return "", false
	}

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
