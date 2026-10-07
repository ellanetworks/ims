package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/n5policy"
	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/internal/rxpolicy"
	"github.com/ellanetworks/ims/internal/sbitls"
	"github.com/ellanetworks/ims/internal/settings"
)

// policyFunction is the PCRF or PCF of the P-CSCF, if any, with the server of the PCF's notifications.
type policyFunction struct {
	backend policy.Backend
	rx      *rxpolicy.Backend
	n5      *n5policy.Backend
	notify  *n5policy.Server
	ln      net.Listener
}

func newPolicyFunction(ctx context.Context, cfg config.Config, s settings.Settings, node *diameter.Node,
	logger *slog.Logger,
) (*policyFunction, error) {
	pf := &policyFunction{}

	switch s.Policy.Interface {
	case settings.PolicyRx:
		p, _ := s.PeerServing(settings.ApplicationRx)
		pf.rx = rxpolicy.New(rxpolicy.Config{
			Diameter: node, PCRF: rxpolicy.PCRF{ID: p.ID, Host: p.Host, Realm: p.Realm},
		})
		pf.backend = pf.rx

		return pf, nil
	case settings.PolicyN5:
	default:
		logger.Info("no policy function: the P-CSCF runs without policy sessions")
		return pf, nil
	}

	// The API checks policies against the configuration file, which may since have changed. Policy sessions then
	// wait for the file or the policy to be fixed, rather than the IMS and its API.
	if err := checkPolicy(cfg, s); err != nil {
		logger.Error("no policy function: the P-CSCF runs without policy sessions", slog.Any("error", err))
		return pf, nil
	}

	n := cfg.N5

	creds, err := n5Credentials(*n, cfg.N5NotifyURI(), logger)
	if err != nil {
		return nil, fmt.Errorf("N5: %w", err)
	}

	b, err := n5policy.New(n5policy.Config{PCF: s.Policy.PCFURI, Notify: cfg.N5NotifyURI(), TLS: creds, Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("N5: %w", err)
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", netip.AddrPortFrom(n.Notify.Address, uint16(n.Notify.Port)).String())
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("listen for N5 notifications: %w", err)
	}

	pf.n5, pf.backend, pf.ln = b, b, ln
	pf.notify = n5policy.NewServer(b, creds, logger)

	return pf, nil
}

// checkPolicy checks the policy settings against the configuration file, which says where to hear from a PCF and
// with which certificates.
func checkPolicy(cfg config.Config, s settings.Settings) error {
	if s.Policy.Interface != settings.PolicyN5 {
		return nil
	}

	switch https := s.Policy.HTTPS(); {
	case cfg.N5 == nil:
		return settings.Invalidf("n5 requires n5 in the configuration file")
	case https && cfg.N5.TLS == nil:
		return settings.Invalidf("n5 over https requires n5.tls in the configuration file")
	case !https && cfg.N5.TLS != nil:
		return settings.Invalidf("n5 over http requires no n5.tls in the configuration file")
	}

	return nil
}

// n5Credentials loads the TLS credentials of N5, if any. The PCF checks the certificate against the host of the
// notification URIs, so the certificate must be valid for it.
func n5Credentials(n config.N5, notifyURI string, logger *slog.Logger) (*sbitls.Credentials, error) {
	if n.TLS == nil {
		return nil, nil
	}

	creds, err := sbitls.Load(sbitls.Files{CA: n.TLS.CA, Cert: n.TLS.Cert, Key: n.TLS.Key})
	if err != nil {
		return nil, fmt.Errorf("TLS: %w", err)
	}

	u, err := url.Parse(notifyURI)
	if err != nil {
		return nil, err
	}

	if err := creds.Certificate().VerifyHostname(u.Hostname()); err != nil {
		return nil, fmt.Errorf("TLS: notification URI %s: %w", notifyURI, err)
	}

	logger.Info("N5 over TLS", slog.String("cert", n.TLS.Cert), slog.Time("not_after", creds.Certificate().NotAfter))

	return creds, nil
}

func (pf *policyFunction) serve(logger *slog.Logger) {
	if pf.notify == nil {
		return
	}

	go func() {
		if err := pf.notify.Serve(pf.ln); !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("N5 notification server stopped", slog.Any("error", err))
		}
	}()
}

func (pf *policyFunction) address() string {
	if pf.ln == nil {
		return ""
	}

	return pf.ln.Addr().String()
}

// close stops the notification server and the N5 client, once the P-CSCF has ended its sessions.
func (pf *policyFunction) close(ctx context.Context) error {
	var err error

	if pf.notify != nil {
		err = pf.notify.Shutdown(ctx)
	}

	// A listener that was never served is not the server's to close.
	if pf.ln != nil {
		if e := pf.ln.Close(); !errors.Is(e, net.ErrClosed) {
			err = errors.Join(err, e)
		}
	}

	if pf.n5 != nil {
		pf.n5.Close()
	}

	return err
}

func (pf *policyFunction) PolicyStatus() api.PolicyStatus {
	switch {
	case pf.rx != nil:
		return api.PolicyStatus{Interface: "rx", Endpoint: pf.rx.Endpoint()}
	case pf.n5 != nil:
		st := pf.n5.Status()
		out := api.PolicyStatus{Interface: "n5", Endpoint: st.PCF, Notify: pf.address()}

		if !st.At.IsZero() {
			out.Last = &api.PolicyResult{At: st.At, Reachable: st.Reachable, Result: st.Result}
		}

		return out
	}

	return api.PolicyStatus{}
}
