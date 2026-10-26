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

// errNoCore is of a request of the IP-SM-GW while the core restarts, without a Diameter node to send it.
var errNoCore = errors.New("the IMS core is restarting")

// startIPSMGW starts the IP-SM-GW under homeDomain: named after it, its host resolved to it in memory through the
// IMS.
func (s *Server) startIPSMGW(ctx context.Context, homeDomain string) error {
	a, err := ipsmgw.Start(ctx, ipsmgw.Config{
		Addresses: s.Config.SIP.Addresses, Transaction: transaction.Config{Network: s.ases.network}, HomeDomain: homeDomain,
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

// stopIPSMGW stops the IP-SM-GW, if it runs, until ctx is done.
func (s *Server) stopIPSMGW(ctx context.Context) {
	a := s.ipsmgw.Swap(nil)
	if a == nil {
		return
	}

	s.ases.resolver.Set(ipsmgw.Host(s.ipsmgwHomeDomain), nil)

	if err := a.Shutdown(ctx); err != nil {
		s.Logger.Warn("failed to stop the IP-SM-GW cleanly", slog.Any("error", err))
	}
}

// resubscribeIPSMGW has the IP-SM-GW, if it runs, subscribe to the reg event package of its registrations of no
// subscription, now that a core runs to take the SUBSCRIBEs.
func (s *Server) resubscribeIPSMGW() {
	if a := s.ipsmgw.Load(); a != nil {
		a.Resubscribe()
	}
}

// ipsmgwNumberChanged has the IP-SM-GW, if it runs, register the new number of the settings at the HSS.
func (s *Server) ipsmgwNumberChanged() {
	if a := s.ipsmgw.Load(); a != nil {
		a.NumberChanged()
	}
}

// shClient is the Sh client of the IP-SM-GW, through the Diameter node of the running core, to the realm of the
// settings for sh.
type shClient struct{ s *Server }

// UpdateSMSRegistration sends the Sh-Update of the SMSRegistrationInfo of user (TS 29.328 §6.1.2.1 step 4b): of the
// IP-SM-GW number, or of an empty one, which deregisters the IP-SM-GW.
func (c shClient) UpdateSMSRegistration(ctx context.Context, user ipsmgw.User, number string) error {
	core := c.s.core.Load()
	if core == nil {
		return errNoCore
	}

	data, err := sh.MarshalShData(sh.ShData{SMSRegistration: &sh.SMSRegistrationInfo{IPSMGWNumber: number}})
	if err != nil {
		return err
	}

	// An IMPU is in its canonical form over Diameter (TS 23.003 §13.4).
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

	// Not connected to the HSS, the update fails at once, and is tried again later (or not, at shutdown).
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

// smscAttemptTimeout bounds the forwarding of a short message to an SMSC: within the TR1M of the UE, 35 to 45 s (TS
// 24.011 §10), for the next SMSC to have time too.
const smscAttemptTimeout = 10 * time.Second

// smscClient is the SGd client of the IP-SM-GW toward the SMSCs of the settings, through the Diameter node of the
// running core.
type smscClient struct{ s *Server }

func (c smscClient) Any() bool {
	for _, p := range c.s.settings.Get().Peers {
		if len(p.ServiceCentres) > 0 {
			return true
		}
	}

	return false
}

// Forward sends the short message to the SMSCs of its service centre address, by priority, to the host of each
// connected one, in the realm it gave in its capabilities exchange (TS 29.338 §6.3.1.1), until one answers: the
// answer of an SMSC is final, that it accepted the message or not.
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

		// The next SMSC only of a request not sent: another, sent again, could deliver the message twice.
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

// s6cClient is the S6c client of the IP-SM-GW toward the HSS, through the Diameter node of the running core, to the
// realm of the settings for s6c.
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

// nodesClient is the SGd client of the IP-SM-GW toward the MMEs, SGSNs and SMSFs the HSS names, through the Diameter
// node of the running core, to a peer of their host.
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

// ipsmgwHandler serves the requests of the IP-SM-GW of the HSS and the SMSCs: the Send Routing Info for SM the HSS
// forwards (S6c), and the MT Forward Short Message of the SMS-GMSC (SGd), to the IP-SM-GW that runs.
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
