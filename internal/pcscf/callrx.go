package pcscf

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/transaction"
)

const DefaultRxCallTimeout = 3 * time.Second

type callRx struct {
	key        regKey
	identities []string
	service    string

	mu      sync.Mutex
	session *rxSession
	busy    bool
	queue   []heldReply
	offer   []byte
	answer  []byte
	flows   map[int]flowNumbers
	ended   bool
}

type heldReply struct {
	tx      *transaction.ServerTransaction
	res     *sip.Response
	x       *exchange
	initial bool
}

func (p *PCSCF) newCallRx(k regKey, identities []string, service string) *callRx {
	if p.rx == nil {
		return nil
	}

	return &callRx{key: k, identities: identities, service: service, flows: make(map[int]flowNumbers)}
}

// TS 29.214 Annex A.1, TS 29.213 Annex B.2
func (p *PCSCF) mediaReply(tx *transaction.ServerTransaction, d *proxy.Dialog, rep proxy.Reply, initial bool) proxy.Verdict {
	c := callOf(d)
	if c == nil || c.rx == nil || rep.Response == nil {
		return proxy.Relay
	}

	cr := c.rx
	h := heldReply{tx: tx, res: rep.Response, initial: initial}

	if rep.Err == nil {
		h.x = p.answerIn(c, d, rep.Response)
	}

	cr.mu.Lock()

	if cr.busy {
		cr.queue = append(cr.queue, h)
		cr.mu.Unlock()

		return proxy.Hold
	}

	if h.x == nil {
		cr.mu.Unlock()
		return proxy.Relay
	}

	cr.busy = true
	cr.mu.Unlock()

	if !p.rx.spawn(func() { p.drain(c, d, h) }) {
		cr.mu.Lock()
		cr.busy = false
		cr.mu.Unlock()

		return proxy.Relay
	}

	return proxy.Hold
}

func (p *PCSCF) answerIn(c *call, d *proxy.Dialog, res *sip.Response) *exchange {
	if res.StatusCode <= 100 || res.StatusCode >= 300 || len(res.Body) == 0 {
		return nil
	}

	offer, answer, answered := d.Session()
	if !answered || !bytes.Equal(answer.Data, res.Body) {
		return nil
	}

	c.rx.mu.Lock()
	seen := bytes.Equal(c.rx.offer, offer.Data) && bytes.Equal(c.rx.answer, answer.Data)
	c.rx.mu.Unlock()

	if seen {
		return nil
	}

	o, err := sdp.Parse(offer.Data)
	if err != nil {
		p.log.Warn("unusable SDP offer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	a, err := sdp.Parse(answer.Data)
	if err != nil {
		p.log.Warn("unusable SDP answer", slog.String("dialog", d.ID()), slog.Any("error", err))
		return nil
	}

	return &exchange{offer: o, answer: a, offerFromUE: offer.From == c.ue, offerData: offer.Data, answerData: answer.Data}
}

func (p *PCSCF) drain(c *call, d *proxy.Dialog, h heldReply) {
	cr := c.rx

	for {
		p.authorize(c, d, h)

		cr.mu.Lock()

		if len(cr.queue) == 0 {
			cr.busy = false
			cr.mu.Unlock()

			return
		}

		h = cr.queue[0]
		cr.queue = cr.queue[1:]

		cr.mu.Unlock()
	}
}

// TS 24.229 §5.2.7.2
func (p *PCSCF) authorize(c *call, d *proxy.Dialog, h heldReply) {
	if h.x != nil {
		if err := p.callAAR(c, d, h); err != nil && !errors.Is(err, errCallEnded) {
			attrs := []any{
				slog.String("dialog", d.ID()), slog.String("impi", c.rx.key.impi), slog.String("ue", c.rx.key.ue.String()),
				slog.String("response", h.res.StartLine()), slog.Any("error", err),
			}

			if result, ok := tgpp.ResultOf(err); ok {
				attrs = append(attrs, slog.String("result", result.String()))
			}

			if h.initial {
				p.log.Warn("media authorization refused: releasing the call", attrs...)

				if err := d.Release(proxy.Release{Toward: proxy.Both, Code: 500}); err != nil {
					p.log.Debug("releasing the call failed", slog.String("dialog", d.ID()), slog.Any("error", err))
				}

				return
			}

			p.log.Warn("media authorization refused for a session modification", attrs...)
		}
	}

	if err := p.cfg.Proxy.Relay(h.tx, h.res); err != nil {
		p.log.Debug("relaying a held response failed", slog.String("response", h.res.StartLine()), slog.Any("error", err))
	}
}

var errCallEnded = errors.New("the call has ended")

// TS 29.214 §4.4.1, §4.4.2
func (p *PCSCF) callAAR(c *call, d *proxy.Dialog, h heldReply) error {
	cr := c.rx

	cr.mu.Lock()

	if cr.ended {
		cr.mu.Unlock()
		return errCallEnded
	}

	flows := make(map[int]flowNumbers, len(cr.flows))
	for k, v := range cr.flows {
		flows[k] = v
	}

	s := cr.session
	if s == nil {
		s = p.rx.callSession(cr)
		cr.session = s
	}

	cr.mu.Unlock()

	components, err := mediaComponents(*h.x, flows)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended {
		return errCallEnded
	}

	initial := !s.opened
	kind := rx.RequestUpdate

	r := rx.AARequest{
		AFApplicationIdentifier: cr.service,
		MediaComponents:         components,
		SubscriptionIDs:         subscriptionIDs(cr.identities),
	}

	if initial {
		kind = rx.RequestInitial
		r.SpecificActions = []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer}
	}

	if r.AFApplicationIdentifier == "" {
		r.AFApplicationIdentifier = icsiRef(h.res.Header.Values("Feature-Caps"))

		cr.mu.Lock()
		cr.service = r.AFApplicationIdentifier
		cr.mu.Unlock()
	}

	r.RequestType = &kind

	if cr.key.ue.Is4() {
		r.FramedIPAddress = cr.key.ue
	} else {
		r.FramedIPv6Address = cr.key.ue
	}

	class, err := p.rx.callAAR(s, r)
	if err != nil {
		if initial {
			var refused *rx.ResultError
			if !errors.As(err, &refused) && !p.rx.closing() {
				_ = p.rx.endLocked(s, rx.TerminationAdministrative, 0)
			}

			s.ended = true
			p.rx.forget(s)

			cr.mu.Lock()
			if cr.session == s {
				cr.session = nil
			}
			cr.mu.Unlock()
		}

		return err
	}

	s.opened = true
	if len(class) > 0 {
		s.class = class
	}

	cr.mu.Lock()
	cr.offer, cr.answer = h.x.offerData, h.x.answerData
	cr.flows = flows
	cr.mu.Unlock()

	p.log.Info("media authorized", slog.String("dialog", d.ID()), slog.String("impi", cr.key.impi),
		slog.String("ue", cr.key.ue.String()), slog.String("session", s.id), slog.String("request", kind.String()))

	return nil
}

// TS 29.213 Annex B.4.1
func (p *PCSCF) callEnded(c *call) {
	cr := c.rx
	if cr == nil {
		return
	}

	cr.mu.Lock()
	cr.ended = true
	s := cr.session
	cr.mu.Unlock()

	if s != nil {
		p.rx.end(s, rx.TerminationLogout, 0)
	}
}

func (c *rxClient) callSession(cr *callRx) *rxSession {
	s := &rxSession{id: c.cfg.Diameter.NewSessionID(), key: cr.key, call: cr}

	c.mu.Lock()
	c.sessions[s.id] = s
	c.mu.Unlock()

	return s
}

func (c *rxClient) callAAR(s *rxSession, r rx.AARequest) ([][]byte, error) {
	req, err := rx.NewAARequest(c.envelope(s.id), r)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(c.ctx, c.cfg.CallTimeout)
	defer cancel()

	ans, err := c.cfg.Diameter.Do(ctx, c.cfg.PCRF.ID, req)
	if err != nil {
		return nil, err
	}

	a, err := rx.ParseAAAnswer(ans)

	return a.Class, err
}

// TS 29.214 §5.4, RFC 4006 §8.46
func subscriptionIDs(identities []string) []rx.SubscriptionID {
	var out []rx.SubscriptionID

	for _, id := range identities {
		u, err := sip.ParseURI(id)
		if err != nil {
			continue
		}

		if t := telForm(u); t.IsTel() {
			out = append(out, rx.SubscriptionID{Type: rx.SubscriptionIDE164, Data: strings.TrimPrefix(t.User, "+")})
			continue
		}

		out = append(out, rx.SubscriptionID{Type: rx.SubscriptionIDSIPURI, Data: id})
	}

	return out
}

// TS 29.214 Annex A.1 NOTE 3, RFC 6809
func icsiRef(values []string) string {
	for _, v := range values {
		for _, e := range sip.SplitList(v) {
			for part := range strings.SplitSeq(e, ";") {
				name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "+g.3gpp.icsi-ref") {
					continue
				}

				value = strings.Trim(strings.TrimSpace(value), `"`)
				if s, err := url.PathUnescape(value); err == nil {
					value = s
				}

				if first, _, _ := strings.Cut(value, ","); first != "" {
					return first
				}
			}
		}
	}

	return ""
}
