// Package xsd is the lexical spaces of the XML Schema Part 2 datatypes the documents of SIP bodies use: values as an
// XML document has them, to and from Go's.
package xsd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Collapse is s without the leading and trailing white space the whiteSpace facet collapse removes (§4.3.6): of
// #x20, #x9, #xA and #xD, not other Unicode spaces. The values of the types with that facet but xs:string have no
// inner white space.
func Collapse(s string) string { return strings.Trim(s, " \t\n\r") }

// ParseBoolean parses an xs:boolean (§3.2.2.1): true, false, 1 or 0.
func ParseBoolean(s string) (v, ok bool) {
	switch Collapse(s) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}

	return false, false
}

// FormatBoolean is the canonical xs:boolean of v (§3.2.2.2).
func FormatBoolean(v bool) string { return strconv.FormatBool(v) }

// ParseUnsignedInt parses an xs:unsignedInt (§3.3.22.1): decimal digits, of 32 bits, after an optional "+", which
// the lexical space it derives from, of xs:nonNegativeInteger, allows (§3.3.20.1).
func ParseUnsignedInt(s string) (uint32, bool) {
	s = strings.TrimPrefix(Collapse(s), "+")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}

	n, err := strconv.ParseUint(s, 10, 32)

	return uint32(n), err == nil
}

// DateTime is an xs:dateTime (§3.2.7): an instant, of a timezone or not. Of a value without one, Time is in UTC,
// the local time of an unspecified locality.
type DateTime struct {
	time.Time
	Zoned bool
}

// ParseDateTime parses an xs:dateTime (§3.2.7.1) of a year of four digits, 0001 to 9999: '24:00:00', the first
// instant of the next day, and a leap second, of a seconds field of 60 (Appendix D.3.1), are taken, but into the
// year 10000.
func ParseDateTime(s string) (DateTime, error) {
	s = Collapse(s)
	in := s

	date, clock, ok := strings.Cut(s, "T")
	if !ok || len(date) != len("yyyy-mm-dd") || date[4] != '-' || date[7] != '-' || len(clock) < len("hh:mm:ss") ||
		clock[2] != ':' || clock[5] != ':' {
		return DateTime{}, fmt.Errorf("xsd: dateTime %q", in)
	}

	field := func(s string) (int, bool) {
		n, err := strconv.Atoi(s)
		return n, err == nil && strings.Trim(s, "0123456789") == ""
	}

	year, okY := field(date[:4])
	month, okM := field(date[5:7])
	day, okD := field(date[8:10])
	hour, okH := field(clock[:2])
	minute, okMin := field(clock[3:5])
	sec, okS := field(clock[6:8])

	rest := clock[8:]

	var nsec int

	if strings.HasPrefix(rest, ".") {
		end := 1 + len(rest[1:]) - len(strings.TrimLeft(rest[1:], "0123456789"))
		frac := rest[1:end]

		if frac == "" {
			return DateTime{}, fmt.Errorf("xsd: dateTime %q", in)
		}

		frac = (frac + "000000000")[:9]
		nsec, _ = strconv.Atoi(frac)
		rest = rest[end:]
	}

	d := DateTime{}
	loc := time.UTC

	switch {
	case rest == "":
	case rest == "Z":
		d.Zoned = true
	case len(rest) == len("+hh:mm") && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':':
		h, okZH := field(rest[1:3])
		m, okZM := field(rest[4:6])

		if !okZH || !okZM || m > 59 || h > 14 || h == 14 && m != 0 {
			return DateTime{}, fmt.Errorf("xsd: dateTime %q: timezone", in)
		}

		off := h*3600 + m*60
		if rest[0] == '-' {
			off = -off
		}

		d.Zoned, loc = true, time.FixedZone("", off)
	default:
		return DateTime{}, fmt.Errorf("xsd: dateTime %q", in)
	}

	if !okY || !okM || !okD || !okH || !okMin || !okS || year < 1 || month < 1 || month > 12 || day < 1 ||
		day > daysIn(month, year) || minute > 59 || sec > 60 || hour > 24 || hour == 24 && (minute != 0 || sec != 0 || nsec != 0) {
		return DateTime{}, fmt.Errorf("xsd: dateTime %q", in)
	}

	// time.Date normalizes 24:00:00 and a second 60 into the next day and minute: of 9999-12-31, into a year of five
	// digits, out of the years taken.
	d.Time = time.Date(year, time.Month(month), day, hour, minute, sec, nsec, loc)
	if d.Year() > 9999 {
		return DateTime{}, fmt.Errorf("xsd: dateTime %q after the year 9999", in)
	}

	return d, nil
}

func daysIn(month, year int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// String is the xs:dateTime of d, of a year of four digits, its timezone as Time has it if Zoned; the empty string
// if its year is not 0001 to 9999.
func (d DateTime) String() string {
	if y := d.Year(); y < 1 || y > 9999 {
		return ""
	}

	if !d.Zoned {
		return d.Format("2006-01-02T15:04:05.999999999")
	}

	return d.Format("2006-01-02T15:04:05.999999999Z07:00")
}
