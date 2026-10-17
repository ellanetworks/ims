package ua

import (
	"errors"
	"fmt"

	"github.com/ellanetworks/ims/sip"
)

// Request sends a request outside a dialog other than INVITE and SUBSCRIBE: REGISTER, MESSAGE, OPTIONS, PUBLISH. The
// agent adds what req leaves out: the From tag, a Call-ID, CSeq and Max-Forwards; it answers 401 and 407 challenges
// through Config.Authenticate. done receives the final response, or the error of the transaction, as a handler.
func (a *Agent) Request(req *sip.Request, done func(res *sip.Response, err error)) error {
	switch req.Method {
	case "INVITE", "ACK", "CANCEL", "BYE", "PRACK", "UPDATE":
		return fmt.Errorf("sip/ua: %s outside a session", req.Method)
	case "SUBSCRIBE":
		return errors.New("sip/ua: SUBSCRIBE through Subscribe")
	}

	tmpl := req.Clone()
	if err := a.completeInitial(tmpl); err != nil {
		return err
	}

	return a.loop.call(func() error {
		if a.closed {
			return ErrClosed
		}

		c := &outOfDialog{a: a, tmpl: tmpl, done: done}

		if err := c.send(); err != nil {
			return err
		}

		a.requests[c] = struct{}{}

		return nil
	})
}

// completeInitial adds to an initial request the header fields the application left out.
func (a *Agent) completeInitial(req *sip.Request) error {
	from, err := req.Header.From()
	if err != nil {
		return fmt.Errorf("sip/ua: From: %w", err)
	}

	if from.Tag() == "" {
		from.Params.Set("tag", sip.NewTag())
		req.Header.Set("From", from.String())
	}

	if !req.Header.Has("To") {
		return errors.New("sip/ua: no To")
	}

	if !req.Header.Has("Call-ID") {
		req.Header.Set("Call-ID", sip.NewTag()+"@"+req.URI.Host)
	}

	if !req.Header.Has("CSeq") {
		req.Header.Set("CSeq", sip.CSeq{Seq: 1, Method: req.Method}.String())
	}

	if !req.Header.Has("Max-Forwards") {
		req.Header.Set("Max-Forwards", "70")
	}

	return nil
}

// nextCSeq moves a request to the next CSeq, for a new request of the same Call-ID (RFC 3261 §8.1.3.5, §22.2).
func nextCSeq(req *sip.Request) {
	cseq, _ := req.Header.CSeq()
	req.Header.Set("CSeq", sip.CSeq{Seq: cseq.Seq + 1, Method: req.Method}.String())
}

// outOfDialog is a request of Agent.Request, sent again after a challenge.
type outOfDialog struct {
	a     *Agent
	tmpl  *sip.Request
	done  func(*sip.Response, error)
	tries int
	final bool
}

func (c *outOfDialog) send() error {
	_, err := c.a.prepareAndSend(c.tmpl.Clone(), c.a.client(c.response, func(err error) { c.finish(nil, err) }))
	return err
}

func (c *outOfDialog) response(res *sip.Response) {
	if res.IsProvisional() || c.final {
		return
	}

	next := c.tmpl.Clone()
	nextCSeq(next)

	if c.a.authenticate(next, res, c.tries) {
		c.tries++
		c.tmpl = next

		if err := c.send(); err != nil {
			c.finish(nil, err)
		}

		return
	}

	c.finish(res, nil)
}

func (c *outOfDialog) finish(res *sip.Response, err error) {
	if c.final {
		return
	}

	c.final = true
	delete(c.a.requests, c)

	if c.done != nil {
		c.a.loop.emit(func() { c.done(res, err) })
	}
}
