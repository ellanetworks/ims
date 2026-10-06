package n5policy

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/n5"
	"github.com/ellanetworks/ims/internal/pcftest"
	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/internal/sbitls"
	"github.com/ellanetworks/ims/internal/sbitls/sbitlstest"
)

// newTLSFixture runs N5 over mutually authenticated TLS, with both ends named localhost.
func newTLSFixture(t *testing.T, ca *sbitlstest.CA) *fixture {
	t.Helper()

	pcfCert := ca.Issue(t, "pcf", sbitlstest.Leaf{Hosts: []string{"localhost"}})
	pcf := pcftest.New(t, pcftest.Config{Host: "localhost", TLS: sbitlstest.Peer(pcfCert.Cert, ca)})

	creds, err := sbitls.Load(ca.Issue(t, "ims", sbitlstest.Leaf{Hosts: []string{"localhost"}}).Files, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	root := "https://localhost:" + port

	b, err := New(Config{PCF: pcf.URL(), Notify: root, TLS: creds, Logger: slog.New(slog.NewTextHandler(t.Output(), nil))})
	if err != nil {
		t.Fatal(err)
	}

	sink := newSink()
	b.Bind(sink)

	srv := NewServer(b, creds, slog.New(slog.DiscardHandler))

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() {
		_ = srv.Close()
		b.Close()
	})

	return &fixture{t: t, pcf: pcf, b: b, sink: sink, notify: root}
}

// TS 33.501 §13.1.0: the P-CSCF and the PCF authenticate each other both ways.
func TestTLS(t *testing.T) {
	f := newTLSFixture(t, sbitlstest.NewCA(t, "ca"))
	id := f.b.NewSessionID()

	uri, err := f.b.OpenSignalling(f.ctx(), id, policy.Signalling{UE: ue4}, false)
	if err != nil {
		t.Fatal(err)
	}

	req := f.pcf.Next(t)
	if !strings.HasPrefix(uri, "https://localhost:") || req.Create.AscReqData.NotifURI != f.notify+SessionsPath+"/"+id {
		t.Fatalf("context %s, notifUri %s", uri, req.Create.AscReqData.NotifURI)
	}

	f.sink.add(id)

	res, err := f.pcf.Terminate(f.ctx(), uri, n5.TerminationPDUSessionTermination)
	if err != nil || res.Status != http.StatusNoContent {
		t.Fatalf("terminate = %+v, %v, want 204", res, err)
	}

	if a := <-f.sink.aborts; a.id != id {
		t.Fatalf("abort %+v", a)
	}

	if got := <-f.sink.terminated; got != id {
		t.Fatalf("terminated %s, want %s", got, id)
	}
}

// Only a client with a certificate from the CA reaches the notification server.
func TestTLSNotifier(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	other := sbitlstest.NewCA(t, "other")
	f := newTLSFixture(t, ca)
	target := f.notify + SessionsPath + "/x/" + n5.TerminateSegment

	stranger := other.Issue(t, "stranger", sbitlstest.Leaf{}).Cert
	pcf := ca.Issue(t, "pcf2", sbitlstest.Leaf{}).Cert

	for name, tc := range map[string]struct {
		cert *tls.Certificate
		ok   bool
	}{
		"no certificate":         {nil, false},
		"certificate of another": {&stranger, false},
		"certificate of the CA":  {&pcf, true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &tls.Config{RootCAs: sbitlstest.Pool(ca), NextProtos: []string{"h2"}}
			if tc.cert != nil {
				cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return tc.cert, nil }
			}

			var protocols http.Protocols

			protocols.SetHTTP2(true)

			tr := &http.Transport{Protocols: &protocols, TLSClientConfig: cfg}
			defer tr.CloseIdleConnections()

			status, err := post(f, tr, target)
			if tc.ok {
				// An unknown session: the server answered.
				if err != nil || status != http.StatusBadRequest {
					t.Fatalf("POST = %d, %v, want 400", status, err)
				}

				return
			}

			if err == nil {
				t.Fatalf("POST answered %d without a valid client certificate", status)
			}
		})
	}

	t.Run("cleartext", func(t *testing.T) {
		var protocols http.Protocols

		protocols.SetUnencryptedHTTP2(true)

		tr := &http.Transport{Protocols: &protocols}
		defer tr.CloseIdleConnections()

		if status, err := post(f, tr, strings.Replace(target, "https://", "http://", 1)); err == nil {
			t.Fatalf("cleartext POST answered %d", status)
		}
	})
}

// post sends a termination to target, and returns the status of the answer.
func post(f *fixture, tr *http.Transport, target string) (int, error) {
	body := `{"termCause":"PDU_SESSION_TERMINATION","resUri":"https://localhost/x"}`

	req, err := http.NewRequestWithContext(f.ctx(), http.MethodPost, target, bytes.NewReader([]byte(body)))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", n5.ContentJSON)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return 0, err
	}

	_ = resp.Body.Close()

	return resp.StatusCode, nil
}

func TestTLSConfig(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")

	creds, err := sbitls.Load(ca.Issue(t, "ims", sbitlstest.Leaf{}).Files, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := New(Config{PCF: "https://pcf:7777", Notify: "http://pcscf:7778", TLS: creds}); err == nil ||
		!strings.Contains(err.Error(), "want https://host[:port]") {
		t.Fatalf("an http notification root with an https PCF: %v", err)
	}

	b, err := New(Config{PCF: "https://pcf", Notify: "https://pcscf", TLS: creds})
	if err != nil {
		t.Fatal(err)
	}

	defer b.Close()

	for uri, want := range map[string]bool{
		"https://pcf" + n5.AppSessionsPath + "/1":      true,
		"https://pcf:443" + n5.AppSessionsPath + "/1":  true,
		"http://pcf" + n5.AppSessionsPath + "/1":       false,
		"https://pcf:7777" + n5.AppSessionsPath + "/1": false,
	} {
		if got := b.ours(uri); got != want {
			t.Errorf("ours(%q) = %v, want %v", uri, got, want)
		}
	}
}
