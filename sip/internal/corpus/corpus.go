package corpus

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

//go:embed testdata
var testdata embed.FS

type Fixture struct {
	Name string
	Raw  []byte
}

func Corpus() []Fixture {
	var out []Fixture

	for _, root := range []string{"testdata/ella", "testdata/synthetic"} {
		err := fs.WalkDir(testdata, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path.Ext(p) != ".sip" {
				return err
			}

			raw, err := testdata.ReadFile(p)
			if err != nil {
				return err
			}

			out = append(out, Fixture{Name: strings.TrimPrefix(p, "testdata/"), Raw: raw})

			return nil
		})
		if err != nil {
			panic(err)
		}
	}

	slices.SortFunc(out, func(a, b Fixture) int { return strings.Compare(a.Name, b.Name) })

	return out
}

type Mismatch struct {
	Check  string
	Header string
	Got    string
	Want   string
}

func (m Mismatch) String() string {
	if m.Header == "" {
		return fmt.Sprintf("%s: got %s, want %s", m.Check, m.Got, m.Want)
	}

	return fmt.Sprintf("%s %s: got %s, want %s", m.Check, m.Header, m.Got, m.Want)
}

func CheckRoundTrip(raw []byte) []Mismatch {
	ref, ok := scan(raw)
	if !ok {
		return []Mismatch{{Check: "fixture", Got: "unscannable", Want: "valid fixture"}}
	}

	msg, err := sip.Parse(raw)
	if err != nil {
		return []Mismatch{{Check: "parse", Got: err.Error(), Want: "no error"}}
	}

	var out []Mismatch

	if msg.StartLine() != ref.StartLine {
		out = append(out, Mismatch{Check: "start-line", Got: msg.StartLine(), Want: ref.StartLine})
	}

	for _, name := range ref.Names() {
		got := elements(name, msg.Env().Header.Values(name))
		if want := ref.values(name); !slices.Equal(got, want) {
			out = append(out, Mismatch{Check: "parsed-view", Header: name, Got: fmt.Sprintf("%q", got), Want: fmt.Sprintf("%q", want)})
		}
	}

	if body := msg.Env().Body; !bytes.Equal(body, ref.Body) {
		out = append(out, Mismatch{Check: "body", Got: strconv.Quote(string(body)), Want: strconv.Quote(string(ref.Body))})
	}

	want := raw[:bytes.Index(raw, []byte("\r\n\r\n"))+4+len(ref.Body)]
	if got := msg.Bytes(); !bytes.Equal(got, want) {
		out = append(out, Mismatch{Check: "bytes", Got: strconv.Quote(string(got)), Want: strconv.Quote(string(want))})
	}

	if err := msg.Validate(); err != nil {
		out = append(out, Mismatch{Check: "validate", Got: err.Error(), Want: "no error"})
	}

	if _, err := sip.Parse(msg.Bytes()); err != nil {
		out = append(out, Mismatch{Check: "reparse", Got: err.Error(), Want: "no error"})
	}

	return out
}

var addressHeaders = []string{
	"From", "To", "Contact", "P-Asserted-Identity", "P-Preferred-Identity",
	"P-Associated-URI", "P-Called-Party-ID", "Path", "Service-Route", "Route", "Record-Route",
}

type refURI struct {
	Scheme string
	User   string
	Host   string
	Port   int
	Text   string
}

func (u refURI) String() string {
	return fmt.Sprintf("{scheme=%q user=%q host=%q port=%d text=%q}", u.Scheme, u.User, u.Host, u.Port, u.Text)
}

func fromURI(u sip.URI) refURI {
	return refURI{Scheme: strings.ToLower(u.Scheme), User: u.User, Host: u.Host, Port: int(u.Port), Text: u.String()}
}

func CheckURIs(raw []byte) []Mismatch {
	ref, ok := scan(raw)
	if !ok {
		return []Mismatch{{Check: "fixture", Got: "unscannable", Want: "valid fixture"}}
	}

	msg, err := sip.Parse(raw)
	if err != nil {
		return []Mismatch{{Check: "parse", Got: err.Error(), Want: "no error"}}
	}

	var out []Mismatch

	if req, ok := msg.(*sip.Request); ok {
		_, rest, _ := strings.Cut(ref.StartLine, " ")
		want := referenceURI(strings.TrimSuffix(rest, " SIP/2.0"))

		if got := fromURI(req.URI); got != want {
			out = append(out, Mismatch{Check: "uri", Header: "Request-URI", Got: got.String(), Want: want.String()})
		}
	}

	for _, name := range addressHeaders {
		var want []refURI

		for _, v := range ref.values(name) {
			if u, ok := addressURI(v); ok {
				want = append(want, referenceURI(u))
			}
		}

		if len(want) == 0 {
			continue
		}

		as, err := msg.Env().Header.Addresses(name)

		if slices.ContainsFunc(want, func(u refURI) bool { return strings.ContainsAny(u.Text, "<>") }) {
			if err == nil {
				out = append(out, Mismatch{Check: "uri", Header: name, Got: "accepted", Want: "invalid URI rejected"})
			}

			continue
		}

		if err != nil {
			out = append(out, Mismatch{Check: "uri", Header: name, Got: err.Error(), Want: fmt.Sprint(want)})
			continue
		}

		var got []refURI
		for _, a := range as {
			got = append(got, fromURI(a.URI))
		}

		if !slices.Equal(got, want) {
			out = append(out, Mismatch{Check: "uri", Header: name, Got: fmt.Sprint(got), Want: fmt.Sprint(want)})
		}
	}

	return out
}

func addressURI(v string) (string, bool) {
	if v == "*" {
		return "", false
	}

	if i := strings.IndexByte(v, '<'); i >= 0 {
		j := strings.IndexByte(v[i:], '>')
		if j < 0 {
			return "", false
		}

		return v[i+1 : i+j], true
	}

	u, _, _ := strings.Cut(v, ";")

	return strings.TrimSpace(u), true
}

func referenceURI(s string) refURI {
	u := refURI{Text: s}

	scheme, rest, ok := strings.Cut(s, ":")
	if !ok {
		return u
	}

	u.Scheme = strings.ToLower(scheme)

	switch u.Scheme {
	case "tel":
		u.User, _, _ = strings.Cut(rest, ";")
		return u
	case "sip", "sips":
	default:
		return u
	}

	if i := strings.IndexByte(rest, '@'); i >= 0 {
		u.User, _, _ = strings.Cut(rest[:i], ":")
		rest = rest[i+1:]
	}

	hostport := rest
	if i := strings.IndexAny(rest, ";?"); i >= 0 {
		hostport = rest[:i]
	}

	host, port := hostport, ""
	if strings.HasPrefix(hostport, "[") {
		if i := strings.IndexByte(hostport, ']'); i >= 0 {
			host = hostport[:i+1]
			port = strings.TrimPrefix(hostport[i+1:], ":")
		}
	} else if h, p, ok := strings.Cut(hostport, ":"); ok {
		host, port = h, p
	}

	u.Host = host
	u.Port, _ = strconv.Atoi(port)

	return u
}

func RunCorpus(t *testing.T, check func([]byte) []Mismatch) {
	t.Helper()

	fixtures := Corpus()
	if len(fixtures) == 0 {
		t.Fatal("no fixtures")
	}

	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			for _, m := range check(f.Raw) {
				t.Errorf("%v", m)
			}
		})
	}
}
