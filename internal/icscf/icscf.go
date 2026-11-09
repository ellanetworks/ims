package icscf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/homedomain"
	"github.com/ellanetworks/ims/internal/regmetrics"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	defaultCxTimeout = 10 * time.Second

	budgetMargin   = 4
	contactTimeout = 8

	resolveTimeout = 4 * time.Second
)

const BreakoutParam = "breakout"

type Diameter interface {
	Identity() diameter.Identity
	NewSessionID() string
	Send(ctx context.Context, req *diameter.Message, opts ...diameter.RequestOption) (*diameter.Message, error)
}

type SCSCF struct {
	Name         sip.URI
	Capabilities []uint32
	Listeners    []netip.AddrPort
}

// RFC 3263 section 4
type Resolver interface {
	Resolve(ctx context.Context, u sip.URI) ([]sip.Server, error)
}

type Config struct {
	Home      homedomain.Domains
	NetworkID string

	Layer *transaction.Layer
	Proxy *proxy.Proxy
	Port  uint16

	Trust *trust.Domain
	SCSCF SCSCF
	IBCF  []netip.AddrPort

	Resolver     Resolver
	PSISubdomain func(host string) bool

	HSSRealm  func() string
	Diameter  Diameter
	CxTimeout time.Duration

	Records              *callrecords.Recorder
	RegistrationAttempts *regmetrics.Registrations

	Logger *slog.Logger
}

type ICSCF struct {
	cfg Config
	log *slog.Logger
	t1  time.Duration
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

	if i.cfg.Resolver == nil {
		i.cfg.Resolver = &sip.Resolver{}
	}

	return i
}

func (i *ICSCF) HandleRequest(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
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
		i.register(ctx, tx, req)
	case to.Tag() != "":
		i.subsequent(ctx, tx, req)
	default:
		i.initial(ctx, tx, req)
	}
}

func (i *ICSCF) HandleCancel(ctx context.Context, tx *transaction.ServerTransaction, cancel *sip.Request) {
	i.cfg.Proxy.Cancel(ctx, tx, cancel)
}

func (i *ICSCF) HandleAck(ctx context.Context, ack *sip.Request) {
	i.log.DebugContext(ctx, "dropped an ACK outside a transaction", slog.String("call-id", ack.Header.CallID()))
}

func (i *ICSCF) HandleTransactionError(ctx context.Context, tx *transaction.ServerTransaction, err error) {
	i.log.DebugContext(ctx, "I-CSCF transaction failed", slog.String("request", tx.Request().StartLine()), slog.Any("error", err))
}

func (i *ICSCF) deadline() time.Time {
	return time.Now().Add((64 - budgetMargin) * i.t1)
}

func (i *ICSCF) branchTimeout(deadline time.Time) time.Duration {
	return max(time.Until(deadline), i.t1)
}

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

func (i *ICSCF) outgoing(req *sip.Request, res *sip.Response) {
	res.Header.Del("P-Profile-Key")

	if !i.trusted(req) {
		trust.StripResponse(res)
	}
}

func (i *ICSCF) forward(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, to proxy.Target, opts proxy.Options) {
	err := i.cfg.Proxy.Forward(ctx, tx, out, to, opts)
	if err == nil {
		return
	}

	if errors.Is(err, proxy.ErrAnswered) {
		i.log.DebugContext(ctx, "request answered before it was forwarded", slog.String("request", out.StartLine()))
		return
	}

	code := 500

	var serr *sip.StatusError
	if errors.As(err, &serr) {
		code = serr.StatusCode
	}

	i.log.DebugContext(ctx, "forwarding failed", slog.String("request", out.StartLine()), slog.Any("error", err))
	i.answer(ctx, tx, code)
}

func (i *ICSCF) answer(ctx context.Context, tx *transaction.ServerTransaction, code int) {
	res := sip.NewResponse(tx.Request(), code, "")

	i.cfg.Records.RejectingRequest(tx.Request(), code, tx.ToTag())
	i.cfg.RegistrationAttempts.Answered(tx.Request(), res, false)

	if err := i.cfg.Proxy.Relay(ctx, tx, res); err == nil {
		return
	}

	i.send(tx, res)
}

func (i *ICSCF) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	i.cfg.Records.RejectingRequest(tx.Request(), res.StatusCode, tx.ToTag())
	i.cfg.RegistrationAttempts.Answered(tx.Request(), res, false)
	i.send(tx, res)
}

func (i *ICSCF) send(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		i.log.Debug("I-CSCF response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}

func (i *ICSCF) spawn(ctx context.Context, tx *transaction.ServerTransaction, f func(ctx context.Context)) {
	if err := i.cfg.Layer.Go(ctx, f); err != nil {
		i.log.DebugContext(ctx, "dropped a request", slog.String("request", tx.Request().StartLine()), slog.Any("error", err))
		i.answer(ctx, tx, 500)
	}
}
