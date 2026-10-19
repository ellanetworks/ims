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

	req := sip.NewRequest("MESSAGE", uri)
	if err := u.Originate(req, ""); err != nil {
		return nil, err
	}

	if err := u.prepare(req); err != nil {
		return nil, err
	}

	from, _ := sip.ParseAddress(req.Header.Get("From"))
	from.Params.Set("tag", sip.NewTag())

	req.Header.Set("From", from.String())
	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Call-ID", timeUUID()+"@"+sip.FormatHost(u.cfg.Local))
	req.Header.Add("CSeq", "1 MESSAGE")

	if contentType == SMSContentType {
		req.Header.Add("Accept-Contact", "*;+g.3gpp.smsip")
	}

	req.SetBody(contentType, body)

	for _, f := range headers {
		req.Header.Set(f.Name, f.Value)
	}

	return u.request(ctx, req)
}
