package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/ua"
)

// RFC 7519 section 6.1
// TS 33.180 clause B.2.2.2
// TS 33.180 clause B.2.2.3
// TS 33.180 clause B.4.2.2
func UnsignedToken(mcpttID, clientID string, exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString

	claims, _ := json.Marshal(map[string]any{
		"exp":       exp.Unix(),
		"scope":     tokenScope,
		"client_id": clientID,
		"mcptt_id":  mcpttID,
	})

	return enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + "."
}

// TS 33.180 clause B.4.2.2
const tokenScope = "3gpp:mc:ptt_service"

const tokenLifetime = time.Hour

func (c *Client) token() string {
	if c.cfg.Token != "" {
		return c.cfg.Token
	}

	return UnsignedToken(c.cfg.MCPTTID.String(), c.cfg.ClientID, c.clock.Now().Add(tokenLifetime))
}

// RFC 3261 section 10.2.4
type registration struct {
	callID  string
	cseq    uint32
	refresh *ua.Timer
}

// TS 24.379 clause 7.2.1
func (c *Client) RegisterBodies() []sip.BodyPart {
	b, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{
		AccessToken: mcpttinfo.Text(c.token()),
		ClientID:    mcpttinfo.Text(c.cfg.ClientID),
	}})
	if err != nil {
		return nil
	}

	return []sip.BodyPart{sip.NewBodyPart(mcpttinfo.ContentType, b)}
}

// TS 24.379 clause 7.2.1
func (c *Client) Register(ctx context.Context, expires time.Duration, done func(res *sip.Response, err error)) error {
	return c.register(ctx, expires, done)
}

// RFC 3261 section 10.2.2
func (c *Client) Unregister(ctx context.Context, done func(res *sip.Response, err error)) error {
	return c.register(ctx, 0, done)
}

func (c *Client) register(ctx context.Context, expires time.Duration, done func(res *sip.Response, err error)) error {
	req := sip.NewRequest("REGISTER", c.cfg.Registrar)
	aor := sip.Address{URI: c.cfg.IMPU}.String()

	c.mu.Lock()

	if c.reg.callID == "" {
		c.reg.callID = sip.NewTag() + "@" + c.contact().URI.Host
	}

	if c.reg.refresh != nil {
		c.reg.refresh.Stop()
		c.reg.refresh = nil
	}

	c.reg.cseq++
	cseq := c.reg.cseq
	req.Header.Set("Call-ID", c.reg.callID)
	req.Header.Set("CSeq", strconv.FormatUint(uint64(c.reg.cseq), 10)+" REGISTER")
	c.mu.Unlock()

	req.Header.Set("From", aor)
	req.Header.Set("To", aor)
	req.Header.Set("Contact", c.contact().String())
	req.Header.Set("Expires", strconv.FormatInt(int64(expires/time.Second), 10))

	if expires > 0 {
		req.SetBodies(c.RegisterBodies()...)
	}

	return c.cfg.Agent.Request(ctx, req, func(res *sip.Response, err error) {
		if err == nil && res.IsSuccess() && expires > 0 {
			c.scheduleRefresh(cseq, granted(res, c.contact(), expires), expires)
		}

		if done != nil {
			done(res, err)
		}
	}, ua.On(c.st))
}

// RFC 3261 section 10.2.4
func granted(res *sip.Response, contact sip.Address, asked time.Duration) time.Duration {
	if cs, err := res.Header.Contacts(); err == nil {
		for _, a := range cs {
			if !a.URI.Equivalent(contact.URI) {
				continue
			}

			if v, ok := a.Params.Get("expires"); ok {
				if n, err := strconv.ParseUint(v, 10, 32); err == nil {
					return time.Duration(n) * time.Second
				}
			}
		}
	}

	if n, err := res.Header.Expires(); err == nil {
		return time.Duration(n) * time.Second
	}

	return asked
}

func (c *Client) scheduleRefresh(cseq uint32, d, expires time.Duration) {
	if d <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if cseq != c.reg.cseq {
		return
	}

	if c.reg.refresh != nil {
		c.reg.refresh.Stop()
	}

	c.reg.refresh = c.st.AfterFunc(d/2, func() {
		err := c.register(context.Background(), expires, func(res *sip.Response, err error) {
			c.emit(Event{Kind: EventRegistration, Response: res, Err: err})
		})
		if err != nil {
			c.emit(Event{Kind: EventRegistration, Err: err})
		}
	})
}
