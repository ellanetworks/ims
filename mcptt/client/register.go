package client

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/ua"
)

// UnsignedToken is an access token for tests and the lab, while no identity management server issues them [PM 4]:
// an Unsecured JWT (RFC 7519 §6.1), of alg "none" and without signature, with the claims TS 33.180 requires: exp,
// scope of the MCPTT service and client_id (Table B.2.2.2-1, B.4.2.2), and mcptt_id (Table B.2.2.3-1). Only a
// server that does not validate tokens, as registrar.Unvalidated, takes it.
func UnsignedToken(mcpttID, clientID string, exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString

	// A map of strings and a number marshals.
	claims, _ := json.Marshal(map[string]any{
		"exp":       exp.Unix(),
		"scope":     tokenScope,
		"client_id": clientID,
		"mcptt_id":  mcpttID,
	})

	return enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + "."
}

// tokenScope is the scope of an access token of the MCPTT service (TS 33.180 B.4.2.2).
const tokenScope = "3gpp:mc:ptt_service"

// tokenLifetime is the lifetime of the UnsignedToken the client makes for each REGISTER without Config.Token.
const tokenLifetime = time.Hour

// token is the access token of a REGISTER: the configured one, or an UnsignedToken of the user and client.
func (c *Client) token() string {
	if c.cfg.Token != "" {
		return c.cfg.Token
	}

	return UnsignedToken(c.cfg.MCPTTID.String(), c.cfg.ClientID, c.clock.Now().Add(tokenLifetime))
}

// registration is the state of the REGISTERs of the client (RFC 3261 §10.2.4): one Call-ID, the CSeq incremented.
type registration struct {
	callID  string
	cseq    uint32
	refresh *ua.Timer
}

// RegisterBodies are the bodies of the client's REGISTERs (TS 24.379 §7.2.1): the mcptt-info of its access token
// and client ID, confidentiality and integrity protection disabled [PM 9]. Through the IMS, the UE hosting the client
// puts them in its own REGISTERs.
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

// Register registers the client with the server as its registrar, in direct mode [PM 14] (TS 24.379 §7.2.1): its
// Contact with the MCPTT feature tags, for expires, its service authorisation in the mcptt-info body. done receives
// the final response, or the error of the transaction. The client refreshes the registration halfway through the
// duration the 200 grants its contact, each response told in an EventRegistration; Unregister ends it.
func (c *Client) Register(expires time.Duration, done func(res *sip.Response, err error)) error {
	return c.register(expires, done)
}

// Unregister removes the registration (RFC 3261 §10.2.2): logging off (TS 23.280 §5.2.5).
func (c *Client) Unregister(done func(res *sip.Response, err error)) error {
	return c.register(0, done)
}

func (c *Client) register(expires time.Duration, done func(res *sip.Response, err error)) error {
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

	return c.cfg.Agent.Request(req, func(res *sip.Response, err error) {
		if err == nil && res.IsSuccess() && expires > 0 {
			c.scheduleRefresh(cseq, granted(res, c.contact(), expires), expires)
		}

		if done != nil {
			done(res, err)
		}
	}, ua.On(c.st))
}

// granted is the duration the 200 of a REGISTER grants the contact (RFC 3261 §10.2.4): its expires parameter, or
// the Expires of the 200, or the one asked for.
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

// scheduleRefresh refreshes the registration halfway through d, the duration the 200 to the REGISTER of cseq
// granted, unless a later REGISTER went since: a refresh or the removal of Unregister.
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
		err := c.register(expires, func(res *sip.Response, err error) {
			c.emit(Event{Kind: EventRegistration, Response: res, Err: err})
		})
		if err != nil {
			c.emit(Event{Kind: EventRegistration, Err: err})
		}
	})
}
