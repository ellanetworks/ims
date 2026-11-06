//go:build linux && (amd64 || arm64)

package integration

import (
	"crypto/tls"
	"encoding/pem"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/sbitls/sbitlstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
	"github.com/ellanetworks/ims/testue"
)

func TestTrunkTLS(t *testing.T) {
	imsCA, carrierCA := sbitlstest.NewCA(t, "ims"), sbitlstest.NewCA(t, "carriers")
	ibcfCert := imsCA.Issue(t, "ibcf", sbitlstest.Leaf{Hosts: []string{"ibcf.example"}})
	carrierCert := carrierCA.Issue(t, "carrier", sbitlstest.Leaf{Hosts: []string{carrierDomain}})

	s := newSceneWith(t, func(srv *server.Server) {
		srv.Config.IBCF = &config.IBCF{
			Port: 5085,
			External: config.IBCFExternal{
				Addresses: []netip.Addr{ibcfExternal.Addr()}, Port: int(ibcfExternal.Port()),
				TLS: &config.IBCFTLS{Port: 5061, Cert: ibcfCert.Files.Cert, Key: ibcfCert.Files.Key},
			},
			Keepalive: config.IBCFKeepalive{Failures: 1},
		}
	})

	serve, err := transport.NewTLSServerConfig(transport.TLSOptions{
		Certificates: []tls.Certificate{carrierCert.Cert}, Roots: sbitlstest.Pool(imsCA), Identities: []string{"ibcf.example"},
		RequireClientCertificate: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	c := newCarrier(t, carrierSIP)

	addr, err := c.a.Layer().ListenTLS(t.Context(), netip.AddrPortFrom(carrierSIP.Addr(), 5061), serve)
	if err != nil {
		t.Fatal(err)
	}

	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: carrierCA.Cert.Raw}))

	var trunk struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}

	s.post("/api/v1/sip-trunks", map[string]any{
		"name":          "TLS",
		"addresses":     []map[string]any{{"host": addr.Addr().String(), "port": addr.Port(), "transport": "tls"}},
		"inbound_cidrs": []string{carrierSIP.Addr().String() + "/32"},
		"domain":        carrierDomain,
		"enabled":       true,
		"tls":           map[string]any{"required": true, "ca_certificates": caPEM, "identities": []string{carrierDomain}},
	}, &trunk)
	s.post("/api/v1/sip-routes", map[string]any{"prefix": "+33", "trunks": []string{trunk.Result.ID}}, nil)

	a := s.caller(0, false, testue.Config{})
	ctx := s.ctx()

	ac := invite(t, a, "tel:"+offNet, testue.CallOptions{})

	in := c.next("invite").req
	if f := in.Message().Flow; f.Transport != sip.TLS || f.Local != addr {
		t.Fatalf("carrier got the INVITE over %s on %s", f.Transport, f.Local)
	}

	answerWithOffer(t, in)

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	c.next("established")

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	if bye := c.next("bye"); bye.msg.Env().Flow.Transport != sip.TLS {
		t.Errorf("BYE over %s", bye.msg.Env().Flow.Transport)
	}

	t.Run("required trunk over UDP", func(t *testing.T) {
		c.invite("+15550002")

		if e := c.next("failed"); e.res == nil || e.res.StatusCode != 403 {
			t.Fatalf("got %v, want 403", e.res)
		}
	})
}
