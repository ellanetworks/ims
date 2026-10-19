package testue

import (
	"context"
	"fmt"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

const SMSContentType = "application/vnd.3gpp.sms"

// SMS is the SMS over IP service of a UE (TS 24.341): MESSAGEs of RP messages, sent and received. It has no ICSI:
// the UE registers it as g.3gpp.smsip (§5.3.1.2), and takes the MESSAGEs with its Content-Type.
type SMS struct {
	u *UE
}

// service is SMS over IP as a service of the UE.
func (s *SMS) service() Service {
	smsip := sip.FeatureSet{sip.NewFeature("+g.3gpp.smsip")}

	return Service{
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

// received answers a MESSAGE of the network (TS 24.341 §5.3.2.1).
func (s *SMS) received(r *ua.Request) {
	s.u.event(Event{Request: r.Message().Clone()})

	_ = r.Respond(ua.Reply{Code: 200})
}

// Send sends an RP message in a MESSAGE to the PSI of the SMSC or to target (TS 24.229 §5.1.2A.1, TS 24.341 §5.3.1.2),
// and returns its final response. headers replace those of the same name.
func (s *SMS) Send(ctx context.Context, target string, body []byte, headers ...sip.Field) (*sip.Response, error) {
	u := s.u

	uri, err := sip.ParseURI(target)
	if err != nil {
		return nil, fmt.Errorf("testue: target: %w", err)
	}

	req := sip.NewRequest("MESSAGE", uri)
	if err := u.Originate(req, SMSService); err != nil {
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
	req.SetBody(SMSContentType, body)

	for _, f := range headers {
		req.Header.Set(f.Name, f.Value)
	}

	return u.request(ctx, req)
}
