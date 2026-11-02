package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/sh"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/ipsmgw"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip/transaction"
)

var errNoCore = errors.New("the IMS core is restarting")

func (s *Server) startIPSMGW(ctx context.Context, homeDomain string) error {
	a, err := ipsmgw.Start(ctx, ipsmgw.Config{
		Addresses: s.Config.SIP.Addresses, Transaction: transaction.Config{Network: s.ases.network, TracerProvider: s.tracerProvider}, HomeDomain: homeDomain,
		Store:   s.database,
		Metrics: s.metrics.ipsmgw,
		HSS:     shClient{s}, SMSCs: smscClient{s}, Router: s6cClient{s}, Nodes: nodesClient{s}, Number: func() string { return s.settings.Get().IPSMGW.Number }, Logger: s.Logger,
	})
	if err != nil {
		return err
	}

	s.ases.resolver.Set(ipsmgw.Host(homeDomain), a.Servers())
	s.ipsmgwHomeDomain = homeDomain
	s.ipsmgw.Store(a)

	return nil
}

func (s *Server) stopIPSMGW(ctx context.Context) {
	a := s.ipsmgw.Swap(nil)
	if a == nil {
		return
	}

	s.ases.resolver.Set(ipsmgw.Host(s.ipsmgwHomeDomain), nil)

	if err := a.Shutdown(ctx); err != nil {
		s.Logger.WarnContext(ctx, "failed to stop the IP-SM-GW cleanly", slog.Any("error", err))
	}
}

func (s *Server) resubscribeIPSMGW(ctx context.Context) {
	if a := s.ipsmgw.Load(); a != nil {
		a.Resubscribe(ctx)
	}
}

func (s *Server) ipsmgwNumberChanged(ctx context.Context) {
	if a := s.ipsmgw.Load(); a != nil {
		a.NumberChanged(ctx)
	}
}

type shClient struct{ s *Server }

// TS 29.328 clause 6.1.2.1
func (c shClient) UpdateSMSRegistration(ctx context.Context, user ipsmgw.User, number string) error {
	core := c.s.core.Load()
	if core == nil {
		return errNoCore
	}

	data, err := sh.MarshalShData(sh.ShData{SMSRegistration: &sh.SMSRegistrationInfo{IPSMGWNumber: number}})
	if err != nil {
		return err
	}

	id := sh.UserIdentity{MSISDN: user.MSISDN}
	if user.MSISDN == "" {
		id.PublicIdentity = user.IMPU.Identity().String()
	}

	req, err := sh.NewProfileUpdateRequest(tgpp.Envelope{
		SessionID:        core.node.NewSessionID(),
		Origin:           core.node.Identity(),
		DestinationRealm: core.realms.of(settings.ApplicationSh)(),
	}, sh.ProfileUpdateRequest{
		User:           id,
		DataReferences: []sh.DataReference{sh.DataSMSRegistrationInfo},
		UserData:       data,
	})
	if err != nil {
		return err
	}

	ans, err := observedDiameter{core.node, c.s.metrics}.Send(ctx, req, diameter.FailFast())
	if err != nil {
		return fmt.Errorf("Sh-Update: %w", err)
	}

	if _, err := sh.ParseProfileUpdateAnswer(ans); err != nil {
		if r, ok := tgpp.ResultOf(err); ok && r.Code >= 5000 && r.Code < 6000 {
			return fmt.Errorf("Sh-Update: %w: %w", ipsmgw.ErrRejected, err)
		}

		return fmt.Errorf("Sh-Update: %w", err)
	}

	return nil
}

// TS 24.011 clause 10
const smscAttemptTimeout = 10 * time.Second

type smscClient struct{ s *Server }

func (c smscClient) Any() bool {
	for _, p := range c.s.settings.Get().Peers {
		if len(p.ServiceCentres) > 0 {
			return true
		}
	}

	return false
}

// TS 29.338 clause 6.3.1.1
func (c smscClient) Forward(ctx context.Context, m sgd.MOForwardShortMessage) ([]byte, error) {
	smscs := c.s.settings.Get().SMSCs(m.ServiceCentreAddress)
	if len(smscs) == 0 {
		return nil, ipsmgw.ErrUnknownServiceCentre
	}

	core := c.s.core.Load()
	if core == nil {
		return nil, errNoCore
	}

	realms := map[string]string{}

	for _, st := range core.node.Peers() {
		if st.State == diameter.PeerOpen {
			realms[st.ID] = st.Realm
		}
	}

	err := diameter.ErrNotConnected

	for _, p := range smscs {
		realm, ok := realms[p.ID]
		if !ok {
			continue
		}

		var req *diameter.Message

		req, err = sgd.NewMOForwardShortMessageRequest(tgpp.Envelope{
			SessionID: core.node.NewSessionID(), Origin: core.node.Identity(), DestinationHost: p.Host,
			DestinationRealm: realm,
		}, m)
		if err != nil {
			return nil, err
		}

		actx, cancel := context.WithTimeout(ctx, smscAttemptTimeout)

		var ans *diameter.Message

		ans, err = observedDiameter{core.node, c.s.metrics}.Send(actx, req, diameter.FailFast())

		cancel()

		if errors.Is(err, diameter.ErrNotConnected) || errors.Is(err, diameter.ErrUnableToDeliver) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("OFR: %w", err)
		}

		res, err := sgd.ParseMOForwardShortMessageAnswer(ans)
		if err != nil {
			return nil, err
		}

		return res.SMRPUI, nil
	}

	return nil, fmt.Errorf("OFR: %w", err)
}

type s6cClient struct{ s *Server }

func (c s6cClient) send(ctx context.Context, build func(tgpp.Envelope) (*diameter.Message, error)) (*diameter.Message, error) {
	core := c.s.core.Load()
	if core == nil {
		return nil, errNoCore
	}

	req, err := build(tgpp.Envelope{
		SessionID: core.node.NewSessionID(), Origin: core.node.Identity(),
		DestinationRealm: core.realms.of(settings.ApplicationS6c)(),
	})
	if err != nil {
		return nil, err
	}

	return observedDiameter{core.node, c.s.metrics}.Send(ctx, req)
}

func (c s6cClient) SendRoutingInfoForSM(ctx context.Context, r s6c.RoutingRequest) (s6c.Routing, error) {
	ans, err := c.send(ctx, func(env tgpp.Envelope) (*diameter.Message, error) {
		return s6c.NewSendRoutingInfoForSMRequest(env, r)
	})
	if err != nil {
		return s6c.Routing{}, fmt.Errorf("SRR: %w", err)
	}

	return s6c.ParseSendRoutingInfoForSMAnswer(ans)
}

func (c s6cClient) ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error) {
	ans, err := c.send(ctx, func(env tgpp.Envelope) (*diameter.Message, error) {
		return s6c.NewReportSMDeliveryStatusRequest(env, rep)
	})
	if err != nil {
		return s6c.ReportResult{}, fmt.Errorf("RDR: %w", err)
	}

	return s6c.ParseReportSMDeliveryStatusAnswer(ans)
}

type nodesClient struct{ s *Server }

func (c nodesClient) Forward(ctx context.Context, host, realm string, m sgd.MTForwardShortMessage) ([]byte, error) {
	core := c.s.core.Load()
	if core == nil {
		return nil, errNoCore
	}

	req, err := sgd.NewMTForwardShortMessageRequest(tgpp.Envelope{
		SessionID: core.node.NewSessionID(), Origin: core.node.Identity(), DestinationHost: host, DestinationRealm: realm,
	}, m)
	if err != nil {
		return nil, err
	}

	ans, err := observedDiameter{core.node, c.s.metrics}.Send(ctx, req, diameter.FailFast())
	if err != nil {
		return nil, fmt.Errorf("TFR to %s: %w", host, err)
	}

	res, err := sgd.ParseMTForwardShortMessageAnswer(ans)
	if err != nil {
		return nil, err
	}

	return res.SMRPUI, nil
}

type ipsmgwHandler struct {
	as func() *ipsmgw.AS
}

func (h ipsmgwHandler) routingInfo(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	a := h.as()
	if a == nil {
		return s6c.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply})
	}

	return a.RoutingInfo(ctx, c.LocalIdentity(), req)
}

func (h ipsmgwHandler) forwardShortMessage(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	a := h.as()
	if a == nil {
		return tgpp.NewResultAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply})
	}

	return a.ForwardShortMessage(ctx, c.LocalIdentity(), req)
}
