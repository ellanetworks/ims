package testue

import (
	"context"
	"fmt"

	"github.com/ellanetworks/ims/sip"
)

const SMSContentType = "application/vnd.3gpp.sms"

// TS 24.229 §5.1.2A.1, TS 24.341 §5.3.1.2
func (u *UE) Message(ctx context.Context, target, contentType string, body []byte, headers ...sip.Field) (*sip.Response, error) {
	uri, err := sip.ParseURI(target)
	if err != nil {
		return nil, fmt.Errorf("testue: target: %w", err)
	}

	u.mu.Lock()
	closed, impu, route := u.closed, u.state.DefaultIMPU, u.state.ServiceRoute
	u.mu.Unlock()

	if closed {
		return nil, ErrClosed
	}

	if impu == "" {
		impu = u.id.impu
	}

	req := sip.NewRequest("MESSAGE", uri)
	if err := u.prepare(req); err != nil {
		return nil, err
	}

	req.Header.Add("Route", "<"+preloadedRoute(req.Flow).String()+">")

	for _, r := range route {
		req.Header.Add("Route", r)
	}

	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<"+impu+">;tag="+sip.NewTag())
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Call-ID", timeUUID()+"@"+sip.FormatHost(u.cfg.Local))
	req.Header.Add("CSeq", "1 MESSAGE")
	req.Header.Add("P-Preferred-Identity", "<"+impu+">")

	if contentType == SMSContentType {
		req.Header.Add("Accept-Contact", "*;+g.3gpp.smsip")
	}

	req.SetBody(contentType, body)

	for _, f := range headers {
		req.Header.Set(f.Name, f.Value)
	}

	return u.request(ctx, req)
}
