// Package pcscf is the P-CSCF. It forwards REGISTER to the I-CSCF without
// sec-agree, and hands every other request to a fallback handler.
package pcscf

import (
	"errors"
	"log/slog"
	"net/netip"

	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type Config struct {
	Proxy     *proxy.Proxy
	ICSCFPort uint16
	Fallback  transaction.Handler
	Logger    *slog.Logger
}

type PCSCF struct {
	cfg Config
	log *slog.Logger
}

func New(cfg Config) *PCSCF {
	p := &PCSCF{cfg: cfg, log: cfg.Logger}

	if p.log == nil {
		p.log = slog.Default()
	}

	return p
}

func (p *PCSCF) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if req.Method != "REGISTER" {
		p.cfg.Fallback.HandleRequest(tx, req)
		return
	}

	if res := p.cfg.Proxy.Check(req); res != nil {
		p.respond(tx, res)
		return
	}

	out, _, err := p.cfg.Proxy.Preprocess(req)
	if err != nil {
		p.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	if err := unprotected(out); err != nil {
		p.respond(tx, sip.NewResponse(req, 400, "Bad Authorization"))
		return
	}

	fromUE(out)

	to := proxy.Target{Flow: sip.Flow{
		Transport: req.Flow.Transport,
		Local:     req.Flow.Local,
		Remote:    netip.AddrPortFrom(req.Flow.Local.Addr(), p.cfg.ICSCFPort),
	}}

	err = p.cfg.Proxy.Forward(tx, out, to, proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict {
		if r.Response != nil {
			toUE(r.Response)
		}

		return proxy.Relay
	}})
	if err == nil || errors.Is(err, proxy.ErrAnswered) {
		return
	}

	code := 500

	var serr *sip.StatusError
	if errors.As(err, &serr) {
		code = serr.StatusCode
	}

	p.respond(tx, sip.NewResponse(req, code, ""))
}

func (p *PCSCF) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	p.cfg.Fallback.HandleCancel(tx, cancel)
}

func (p *PCSCF) HandleAck(ack *sip.Request) {
	p.cfg.Fallback.HandleAck(ack)
}

func (p *PCSCF) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	p.cfg.Fallback.HandleTransactionError(tx, err)
}

func (p *PCSCF) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		p.log.Debug("P-CSCF response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}

// unprotected marks the REGISTER as received without integrity protection
// (TS 24.229 §5.2.2.1), replacing every value the UE claimed.
func unprotected(req *sip.Request) error {
	values := req.Header.Values("Authorization")

	auths := make([]string, 0, len(values))

	for _, v := range values {
		a, err := sip.ParseAuth(v)
		if err != nil {
			return err
		}

		a.Params.Del("integrity-protected")
		a.Params.Set("integrity-protected", sip.Quote("no"))
		auths = append(auths, a.String())
	}

	req.Header.Del("Authorization")

	for _, a := range auths {
		req.Header.Add("Authorization", a)
	}

	return nil
}

// fromUE removes what the UE must not assert: the trust domain's header
// fields (§4.4), the charging header fields (§5.2.1), and the
// P-Visited-Network-ID and Path that the P-CSCF alone inserts (§5.2.2.1).
func fromUE(req *sip.Request) {
	trust.StripRequest(req)

	for _, name := range []string{"P-Charging-Vector", "P-Charging-Function-Addresses", "P-Visited-Network-ID", "Path"} {
		req.Header.Del(name)
	}
}

// toUE removes what the UE must not see: the trust domain's header fields,
// the charging header fields (§5.2.1), and the keys in a 401.
func toUE(res *sip.Response) {
	trust.StripResponse(res)
	res.Header.Del("P-Charging-Vector")

	if res.StatusCode == 401 {
		removeAKAKeys(res)
	}
}

// removeAKAKeys removes the ck and ik the S-CSCF sends to the P-CSCF in a 401
// (TS 24.229 §5.2.2.1).
func removeAKAKeys(res *sip.Response) {
	values := res.Header.Values("WWW-Authenticate")
	res.Header.Del("WWW-Authenticate")

	for _, v := range values {
		a, err := sip.ParseAuth(v)
		if err != nil {
			continue
		}

		a.Params.Del("ck")
		a.Params.Del("ik")
		res.Header.Add("WWW-Authenticate", a.String())
	}
}
