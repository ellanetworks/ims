// Package icscf is the I-CSCF (TS 24.229 §5.3): it finds the S-CSCF of a user
// through Cx and forwards the request there.
package icscf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	defaultCxTimeout = 10 * time.Second

	defaultSCSCFTimeout = 32 * transaction.DefaultT1

	// budgetMargin, in T1, is kept from the 64·T1 that a non-INVITE
	// transaction lasts, for the I-CSCF's final response to reach the UE
	// before the hops in between give up.
	budgetMargin = 4

	// minBranch, in T1, is the least time worth giving another S-CSCF.
	minBranch = 8
)

type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Do(ctx context.Context, peerID string, req *diameter.Message, opts ...diameter.DoOption) (*diameter.Message, error)
}

type HSS struct {
	ID    string
	Realm string
}

// SCSCF is an S-CSCF the I-CSCF can select, with the listeners serving it.
type SCSCF struct {
	Name         sip.URI
	Capabilities []uint32
	Listeners    []netip.AddrPort
}

type Config struct {
	HomeDomain string

	Layer *transaction.Layer
	Proxy *proxy.Proxy
	Port  uint16

	Trust  *trust.Domain
	SCSCFs []SCSCF

	HSS       HSS
	Diameter  Diameter
	CxTimeout time.Duration

	// SCSCFTimeout is how long an S-CSCF has to send a final response to a
	// REGISTER or another non-INVITE request before another S-CSCF replaces
	// it (TS 24.229 §5.3.1.3 NOTE 2). It applies only when one can.
	SCSCFTimeout time.Duration

	Logger *slog.Logger
}

type ICSCF struct {
	cfg    Config
	log    *slog.Logger
	t1     time.Duration
	scscfs []*SCSCF
}

func New(cfg Config) *ICSCF {
	i := &ICSCF{cfg: cfg, log: cfg.Logger, t1: transaction.DefaultT1}

	if cfg.Layer != nil {
		i.t1 = cfg.Layer.T1()
	}

	if i.log == nil {
		i.log = slog.Default()
	}

	if i.cfg.CxTimeout <= 0 {
		i.cfg.CxTimeout = defaultCxTimeout
	}

	if i.cfg.SCSCFTimeout <= 0 {
		i.cfg.SCSCFTimeout = defaultSCSCFTimeout
	}

	for k := range cfg.SCSCFs {
		i.scscfs = append(i.scscfs, &cfg.SCSCFs[k])
	}

	return i
}

func (i *ICSCF) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if res := i.cfg.Proxy.Check(req); res != nil {
		i.respond(tx, res)
		return
	}

	to, err := req.Header.To()
	if err != nil {
		i.respond(tx, sip.NewResponse(req, 400, "Bad To"))
		return
	}

	switch {
	case req.Method == "REGISTER":
		i.register(tx, req)
	case to.Tag() != "":
		i.subsequent(tx, req)
	default:
		i.initial(tx, req)
	}
}

func (i *ICSCF) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	i.cfg.Proxy.Cancel(tx, cancel)
}

func (i *ICSCF) HandleAck(ack *sip.Request) {
	i.log.Debug("dropped an ACK outside a transaction", slog.String("call-id", ack.Header.CallID()))
}

func (i *ICSCF) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	i.log.Debug("I-CSCF transaction failed", slog.String("request", tx.Request().StartLine()), slog.Any("error", err))
}

// deadline is when the I-CSCF answers a non-INVITE request that arrives now,
// at the latest: its server transaction ends at 64·T1.
func (i *ICSCF) deadline() time.Time {
	return time.Now().Add((64 - budgetMargin) * i.t1)
}

// branchTimeout is how long an S-CSCF has to answer a non-INVITE request: the
// time left before the deadline, and no more than SCSCFTimeout when another
// S-CSCF can replace it.
func (i *ICSCF) branchTimeout(deadline time.Time, replaceable bool) time.Duration {
	d := time.Until(deadline)

	if replaceable {
		d = min(d, i.cfg.SCSCFTimeout)
	}

	return max(d, i.t1)
}

// timeLeft reports whether another S-CSCF can be tried before the deadline.
func (i *ICSCF) timeLeft(deadline time.Time) bool {
	return time.Until(deadline) >= minBranch*i.t1
}

// unresponsive reports whether the S-CSCF could not be reached (TS 24.229
// §5.3.1.3, §5.3.2.2): it sent no final response in time, or the request
// could not be sent to it, which RFC 3261 §16.9 treats like a 503. An
// INVITE that got a provisional response and then hit Timer C was answered.
func unresponsive(r proxy.Reply, invite bool) bool {
	switch {
	case r.Err == nil:
		return false
	case errors.Is(r.Err, transaction.ErrTimeout):
		return !invite || !r.Responded
	default:
		return true
	}
}

func (i *ICSCF) trusted(req *sip.Request) bool {
	return i.cfg.Trust.Trusted(req.Flow.Remote.Addr())
}

// outgoing applies what the I-CSCF removes from every response it relays.
func (i *ICSCF) outgoing(req *sip.Request, res *sip.Response) {
	res.Header.Del("P-Profile-Key")

	if !i.trusted(req) {
		trust.StripResponse(res)
	}
}

// forward sends the request statefully and reports whether it did. A failure
// is answered with its status code, unless the request was answered already:
// cancelled, for one.
func (i *ICSCF) forward(tx *transaction.ServerTransaction, out *sip.Request, to proxy.Target, opts proxy.Options) bool {
	err := i.cfg.Proxy.Forward(tx, out, to, opts)
	if err == nil {
		return true
	}

	if errors.Is(err, proxy.ErrAnswered) {
		i.log.Debug("request answered before it was forwarded", slog.String("request", out.StartLine()))
		return false
	}

	code := 500

	var serr *sip.StatusError
	if errors.As(err, &serr) {
		code = serr.StatusCode
	}

	i.log.Debug("forwarding failed", slog.String("request", out.StartLine()), slog.Any("error", err))
	i.answer(tx, code)

	return false
}

// answer sends a final response, through the proxy once the request has been
// forwarded.
func (i *ICSCF) answer(tx *transaction.ServerTransaction, code int) {
	res := sip.NewResponse(tx.Request(), code, "")

	if err := i.cfg.Proxy.Relay(tx, res); err == nil {
		return
	}

	i.respond(tx, res)
}

func (i *ICSCF) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		i.log.Debug("I-CSCF response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}

// spawn runs f in the background, or answers 500 when it cannot: the layer
// is closing.
func (i *ICSCF) spawn(tx *transaction.ServerTransaction, f func(ctx context.Context)) {
	if err := i.cfg.Layer.Go(f); err != nil {
		i.log.Debug("dropped a request", slog.String("request", tx.Request().StartLine()), slog.Any("error", err))
		i.answer(tx, 500)
	}
}
