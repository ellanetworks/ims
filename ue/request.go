package ue

import (
	"context"
	"net/netip"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

// RFC 3261 §8.1.2, RFC 3263 §4.1
func (u *UE) requestTransport(req *sip.Request) sip.Transport {
	if route, err := req.Header.TopRoute(); err == nil {
		if v, ok := route.URI.Params.Get("transport"); ok && v != "" {
			return sip.Transport(strings.ToUpper(v))
		}
	}

	return u.cfg.Transport
}

// prepare completes a request of the UE for its flow (TS 24.229 §5.1.2A.1): the agent's requests and the UE's own.
func (u *UE) prepare(req *sip.Request) error {
	flow, verify, err := u.requestFlow(u.requestTransport(req))
	if err != nil {
		return err
	}

	req.Flow = flow

	via := sip.NewVia(flow.Transport, netip.AddrPortFrom(u.cfg.Local, u.port(!u.cfg.Plain)))
	via.Params.Set("rport", "")

	req.Header.Prepend("Via", via.String())

	if u.cfg.Plain {
		return nil
	}

	for _, v := range verify {
		req.Header.Add("Security-Verify", v)
	}

	req.Header.Add("Require", "sec-agree")
	req.Header.Add("Proxy-Require", "sec-agree")

	if req.Method != "ACK" {
		req.Header.Add("P-Access-Network-Info", u.cfg.AccessNetworkInfo)
	}

	return nil
}

// respondHeaders completes a response of the agent (TS 24.229 §5.1.2A.2).
func (u *UE) respondHeaders(_ *sip.Request, res *sip.Response) {
	if !u.cfg.Plain && !res.Header.Has("P-Access-Network-Info") {
		res.Header.Add("P-Access-Network-Info", u.cfg.AccessNetworkInfo)
	}
}

// Send sends a request outside a dialog that a service originated (Originate), with its Max-Forwards, To, Call-ID
// and CSeq: on the UE's flow, protected by its SAs (TS 24.229 §5.1.2A.1). It returns the final response.
func (u *UE) Send(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	if err := u.prepare(req); err != nil {
		return nil, err
	}

	return u.request(ctx, req)
}
