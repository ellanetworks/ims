package xsd

import (
	"testing"
	"time"
)

// XML Schema Part 2 §3.2.7.1, §3.2.7.3, Appendix D.3.1.
func TestParseDateTime(t *testing.T) {
	for in, want := range map[string]struct {
		t     time.Time
		zoned bool
	}{
		"2002-10-10T12:00:00-05:00":        {time.Date(2002, 10, 10, 17, 0, 0, 0, time.UTC), true},
		"2005-03-04T20:00:00":              {time.Date(2005, 3, 4, 20, 0, 0, 0, time.UTC), false},
		" 2001-10-27T16:49:29.25Z\n":       {time.Date(2001, 10, 27, 16, 49, 29, 250000000, time.UTC), true},
		"1999-12-31T24:00:00Z":             {time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), true},
		"2016-12-31T23:59:60Z":             {time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC), true},
		"2024-02-29T00:00:00+14:00":        {time.Date(2024, 2, 28, 10, 0, 0, 0, time.UTC), true},
		"0001-01-01T00:00:00.123456789123": {time.Date(1, 1, 1, 0, 0, 0, 123456789, time.UTC), false},
	} {
		d, err := ParseDateTime(in)
		if err != nil || !d.Equal(want.t) || d.Zoned != want.zoned {
			t.Errorf("%q: %s %v, %v", in, d.Time, d.Zoned, err)
		}
	}

	for _, in := range []string{
		"", "2002-10-10", "2002-10-10T12:00", "2002-10-10 12:00:00Z", "0000-01-01T00:00:00Z", "10000-01-01T00:00:00Z",
		"-0001-01-01T00:00:00Z", "2023-02-29T00:00:00Z", "2002-13-10T12:00:00Z", "2002-10-10T24:00:01Z",
		"2002-10-10T12:60:00Z", "2002-10-10T12:00:61Z", "2002-10-10T12:00:00.Z", "2002-10-10T12:00:00+15:00",
		"2002-10-10T12:00:00+14:30", "2002-10-10T12:00:00+0500", "2002-10-10T12:00:00z", "+2002-10-10T12:00:00Z",
		"9999-12-31T24:00:00Z", "9999-12-31T23:59:60Z",
	} {
		if d, err := ParseDateTime(in); err == nil {
			t.Errorf("%q: %s", in, d.Time)
		}
	}
}

func TestDateTimeString(t *testing.T) {
	for want, d := range map[string]DateTime{
		"2005-03-04T20:00:00":         {time.Date(2005, 3, 4, 20, 0, 0, 0, time.UTC), false},
		"2005-03-04T20:00:00Z":        {time.Date(2005, 3, 4, 20, 0, 0, 0, time.UTC), true},
		"2005-03-04T20:00:00.5+02:00": {time.Date(2005, 3, 4, 20, 0, 0, 500000000, time.FixedZone("", 7200)), true},
		"":                            {time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), true},
	} {
		if got := d.String(); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}

		if want == "" {
			continue
		}

		if again, err := ParseDateTime(d.String()); err != nil || !again.Equal(d.Time) || again.Zoned != d.Zoned {
			t.Errorf("%q: %v, %v", want, again, err)
		}
	}
}

// §3.2.2.1, §3.3.22.1, §4.3.6.
func TestValues(t *testing.T) {
	for in, want := range map[string]int{"true": 1, "1": 1, " false\t": 0, "0": 0, "TRUE": -1, "yes": -1, " true": -1, "": -1} {
		v, ok := ParseBoolean(in)
		if want < 0 && ok || want >= 0 && (!ok || v != (want == 1)) {
			t.Errorf("boolean %q: %v, %v", in, v, ok)
		}
	}

	for in, want := range map[string]int64{"0": 0, "4294967295": 4294967295, " 007 ": 7, "4294967296": -1, "+1": 1, "+": -1, "++1": -1, "-1": -1, "": -1, "1.0": -1} {
		v, ok := ParseUnsignedInt(in)
		if want < 0 && ok || want >= 0 && (!ok || int64(v) != want) {
			t.Errorf("unsignedInt %q: %d, %v", in, v, ok)
		}
	}
}

func FuzzDateTime(f *testing.F) {
	for _, s := range []string{"2002-10-10T12:00:00-05:00", "2005-03-04T20:00:00", "1999-12-31T24:00:00Z", "2016-12-31T23:59:60.5Z"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDateTime(s)
		if err != nil || d.String() == "" {
			return
		}

		again, err := ParseDateTime(d.String())
		if err != nil || !again.Equal(d.Time) || again.Zoned != d.Zoned {
			t.Fatalf("%q: %q parses to %v, %v", s, d.String(), again, err)
		}
	})
}
