package server

import (
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/sbitls/sbitlstest"
)

func TestN5Credentials(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	ims := ca.Issue(t, "ims", sbitlstest.Leaf{Hosts: []string{"pcscf.example.org", "10.0.0.5"}})
	tlsFiles := &config.TLS{CA: ims.Files.CA, Cert: ims.Files.Cert, Key: ims.Files.Key}
	notify := config.N5Notify{Address: netip.MustParseAddr("10.0.0.5"), Port: 7778}
	logger := slog.New(slog.DiscardHandler)

	n := config.N5{PCFURI: "http://10.0.0.13:7777", Notify: notify}
	if creds, err := n5Credentials(n, logger); creds != nil || err != nil {
		t.Fatalf("over http: %v, %v", creds, err)
	}

	for _, uri := range []string{"", "https://pcscf.example.org:7778"} {
		n := config.N5{PCFURI: "https://pcf.example.org:7777", Notify: notify, TLS: tlsFiles}
		n.Notify.URI = uri

		if creds, err := n5Credentials(n, logger); creds == nil || err != nil {
			t.Fatalf("notify URI %q: %v, %v", uri, creds, err)
		}
	}

	for name, tc := range map[string]struct {
		uri  string
		tls  *config.TLS
		want string
	}{
		"another notify host": {"https://ims.example.org:7778", tlsFiles, "notification URI https://ims.example.org:7778"},
		"missing key":         {"", &config.TLS{CA: ims.Files.CA, Cert: ims.Files.Cert, Key: ims.Files.Key + ".missing"}, "no such file"},
	} {
		t.Run(name, func(t *testing.T) {
			n := config.N5{PCFURI: "https://pcf.example.org:7777", Notify: notify, TLS: tc.tls}
			n.Notify.URI = tc.uri

			if _, err := n5Credentials(n, logger); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("n5Credentials = %v, want an error with %q", err, tc.want)
			}
		})
	}
}
