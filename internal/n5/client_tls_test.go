package n5

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/sbitls"
	"github.com/ellanetworks/ims/internal/sbitls/sbitlstest"
)

type tlsPCF struct {
	*httptest.Server
	// peer is the certificate the last client presented.
	peer chan string
}

// newTLSPCF serves handler over HTTP/2 and TLS only, and requires a client certificate issued by ca.
func newTLSPCF(t *testing.T, ca *sbitlstest.CA, cert tls.Certificate, handler http.HandlerFunc) *tlsPCF {
	t.Helper()

	p := &tlsPCF{peer: make(chan string, 16)}

	p.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			select {
			case p.peer <- r.TLS.PeerCertificates[0].Subject.CommonName:
			default:
			}
		}

		handler(w, r)
	}))

	p.TLS = sbitlstest.Peer(cert, ca)
	p.TLS.NextProtos = []string{"h2"}
	p.Config.Protocols = new(http.Protocols)
	p.Config.Protocols.SetHTTP2(true)
	p.StartTLS()
	t.Cleanup(p.Close)

	return p
}

// named is the URL of the PCF with localhost in place of its address.
func (p *tlsPCF) named() string {
	return strings.Replace(p.URL, "127.0.0.1", "localhost", 1)
}

func newTLSClient(t *testing.T, pcf string, files sbitls.Files) *Client {
	t.Helper()

	creds, err := sbitls.Load(files, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(Config{PCF: pcf, TLS: creds})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(c.Close)

	return c
}

func created(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", "https://"+r.Host+AppSessionsPath+"/1")
	w.Header().Set("Content-Type", ContentJSON)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"ascRespData":{"suppFeat":"10"}}`))
}

// TS 33.501 §13.1.0: mutually authenticated TLS, with the PCF named by domain name or IP address.
func TestTLS(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	ims := ca.Issue(t, "ims", sbitlstest.Leaf{Hosts: []string{"ims.example.org"}})

	pcf := newTLSPCF(t, ca, ca.Issue(t, "pcf", sbitlstest.Leaf{Hosts: []string{"localhost", "127.0.0.1"}}).Cert, created)

	for _, root := range []string{pcf.named(), pcf.URL} {
		t.Run(root, func(t *testing.T) {
			c := newTLSClient(t, root, ims.Files)

			got, err := c.Create(context.Background(), callContext())
			if err != nil {
				t.Fatal(err)
			}

			if got.URI != root+AppSessionsPath+"/1" || got.Features != "10" {
				t.Fatalf("created %+v", got)
			}

			if cn := <-pcf.peer; cn != "ims" {
				t.Fatalf("client certificate %q, want the IMS's", cn)
			}
		})
	}
}

func TestTLSRejectsPCF(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	other := sbitlstest.NewCA(t, "other")
	ims := ca.Issue(t, "ims", sbitlstest.Leaf{})

	for name, cert := range map[string]tls.Certificate{
		"another CA":   other.Issue(t, "stranger", sbitlstest.Leaf{Hosts: []string{"localhost"}}).Cert,
		"another name": ca.Issue(t, "elsewhere", sbitlstest.Leaf{Hosts: []string{"pcf.example.org"}}).Cert,
	} {
		t.Run(name, func(t *testing.T) {
			pcf := newTLSPCF(t, ca, cert, created)
			c := newTLSClient(t, pcf.named(), ims.Files)

			_, err := c.Create(context.Background(), callContext())

			var e *Error
			if !errors.As(err, &e) || e.Status != 0 || !errors.Is(err, ErrConnect) {
				t.Fatalf("error %v, want a connection failure", err)
			}

			if strings.Count(err.Error(), ErrConnect.Error()) != 1 {
				t.Fatalf("error %q", err)
			}
		})
	}
}

// The PCF cannot move a request, or a context, off TLS.
func TestTLSDowngrade(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")
	ims := ca.Issue(t, "ims", sbitlstest.Leaf{})
	cert := ca.Issue(t, "pcf", sbitlstest.Leaf{Hosts: []string{"localhost"}}).Cert

	pcf := newTLSPCF(t, ca, cert, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == AppSessionsPath {
			w.Header().Set("Location", "http://"+r.Host+AppSessionsPath+"/1")
			w.WriteHeader(http.StatusCreated)

			return
		}

		w.Header().Set("Location", "http://"+r.Host+r.URL.Path)
		w.WriteHeader(http.StatusPermanentRedirect)
	})

	c := newTLSClient(t, pcf.named(), ims.Files)
	ctx := context.Background()

	_, err := c.Create(ctx, callContext())
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("Create with an http Location: %v, want a malformed response", err)
	}

	_, err = c.Delete(ctx, pcf.named()+AppSessionsPath+"/1", nil)

	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusPermanentRedirect {
		t.Fatalf("Delete redirected to http: %v, want the redirection as the answer", err)
	}

	if _, err := c.resource("http://localhost"+AppSessionsPath+"/1", ""); err == nil {
		t.Fatal("http context URI accepted")
	}
}

func TestTLSConfig(t *testing.T) {
	ca := sbitlstest.NewCA(t, "ca")

	creds, err := sbitls.Load(ca.Issue(t, "ims", sbitlstest.Leaf{}).Files, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := New(Config{PCF: "https://pcf:7777"}); err == nil || !strings.Contains(err.Error(), "without TLS credentials") {
		t.Errorf("https without credentials: %v", err)
	}

	if _, err := New(Config{PCF: "http://pcf:7777", TLS: creds}); err == nil || !strings.Contains(err.Error(), "TLS credentials for http") {
		t.Errorf("http with credentials: %v", err)
	}
}
