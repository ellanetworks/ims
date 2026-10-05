package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/n5policy"
	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/internal/rxpolicy"
)

// policyFunction is the PCRF or PCF of the P-CSCF, if any, with the server of the PCF's notifications.
type policyFunction struct {
	backend policy.Backend
	rx      *rxpolicy.Backend
	n5      *n5policy.Backend
	notify  *http.Server
	ln      net.Listener
}

func newPolicyFunction(ctx context.Context, cfg config.Config, node *diameter.Node, logger *slog.Logger) (*policyFunction, error) {
	pf := &policyFunction{}

	if p, ok := cfg.RxPeer(); ok {
		pf.rx = rxpolicy.New(rxpolicy.Config{
			Diameter: node, PCRF: rxpolicy.PCRF{ID: p.ID, Host: p.Host, Realm: p.Realm},
		})
		pf.backend = pf.rx

		return pf, nil
	}

	n := cfg.PCSCF.Policy.N5
	if n == nil {
		logger.Info("no policy function: the P-CSCF runs without policy sessions")
		return pf, nil
	}

	b, err := n5policy.New(n5policy.Config{PCF: n.PCFURI, Notify: n.Notify.URI(), Logger: logger})
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
	pf.notify = n5policy.NewServer(b, logger)

	return pf, nil
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
