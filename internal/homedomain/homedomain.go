package homedomain

import "strings"

type Domains []string

func New(primary string, aliases ...string) Domains {
	d := Domains{normalise(primary)}

	for _, a := range aliases {
		if a = normalise(a); a != "" && !d.Has(a) {
			d = append(d, a)
		}
	}

	return d
}

func (d Domains) Primary() string {
	if len(d) == 0 {
		return ""
	}

	return d[0]
}

func (d Domains) Has(host string) bool {
	host = normalise(host)

	for _, name := range d {
		if name != "" && host == name {
			return true
		}
	}

	return false
}

func (d Domains) Covers(host string) bool {
	host = normalise(host)

	for _, name := range d {
		if name != "" && (host == name || strings.HasSuffix(host, "."+name)) {
			return true
		}
	}

	return false
}

func normalise(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
