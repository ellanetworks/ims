package testue

import (
	"context"
	"fmt"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/ue"
)

const SMSContentType = "application/vnd.3gpp.sms"

// TS 24.341
type SMS struct {
	u *UE
}

func (s *SMS) service() ue.Service {
	smsip := sip.FeatureSet{sip.NewFeature("+g.3gpp.smsip")}

	return ue.Service{
		Name:             SMSService,
		RegisterFeatures: smsip,
		AcceptContact:    []sip.CallerPreference{{Features: smsip}},
		Allow:            []string{"MESSAGE"},
		Match: func(req *sip.Request) bool {
			return req.Method == "MESSAGE" && strings.EqualFold(req.Header.ContentType(), SMSContentType)
		},
		OnRequest: s.received,
	}
}

// TS 24.341 clause 5.3.2.1
func (s *SMS) received(r *ua.Request) {
	s.u.event(ue.Event{Request: r.Message().Clone()})

	_ = r.Respond(ua.Reply{Code: 200})
}

// TS 24.229 clause 5.1.2A.1
// TS 24.341 clause 5.3.1.2
func (s *SMS) Send(ctx context.Context, target string, body []byte, headers ...sip.Field) (*sip.Response, error) {
	u := s.u

	uri, err := sip.ParseURI(target)
	if err != nil {
		return nil, fmt.Errorf("testue: target: %w", err)
	}

	req := sip.NewRequest("MESSAGE", uri)
	if err := u.core().Originate(req, SMSService); err != nil {
		return nil, err
	}

	from, _ := sip.ParseAddress(req.Header.Get("From"))
	from.Params.Set("tag", sip.NewTag())

	req.Header.Set("From", from.String())
	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Call-ID", u.core().NewCallID())
	req.Header.Add("CSeq", "1 MESSAGE")
	req.SetBody(SMSContentType, body)

	for _, f := range headers {
		req.Header.Set(f.Name, f.Value)
	}

	return u.core().Send(ctx, req)
}
