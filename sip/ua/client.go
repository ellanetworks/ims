package ua

import (
	"errors"
	"fmt"

	"github.com/ellanetworks/ims/sip"
)

// Request sends a request outside a dialog other than INVITE and SUBSCRIBE: REGISTER, MESSAGE, OPTIONS, PUBLISH. The
// agent adds what req leaves out: the From tag, a Call-ID, CSeq and Max-Forwards; it answers 401 and 407 challenges
// through Config.Authenticate. done receives the final response, or the error of the transaction, as a handler.
func (a *Agent) Request(req *sip.Request, done func(res *sip.Response, err error), opts ...Option) error {
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

	c := &outOfDialog{a: a, st: a.options(opts).strand(a), tmpl: tmpl, done: done}

	if err := a.addRequest(c); err != nil {
		return err
	}

	if err := c.st.do(c.send); err != nil {
		a.removeRequest(c)
		return err
	}

	return nil
}

// Option is an option of a request the agent sends outside a dialog: Invite, Subscribe, Request.
type Option func(*options)

type options struct {
	st       *Strand
	policy   *Policy
	without  []string
	attached []sip.BodyPart
}

// On puts the dialogs of the request on st, with those of other requests: the legs of a call bridged by a B2BUA,
// for one. Without it, they get a strand of their own.
func On(st *Strand) Option {
	return func(o *options) { o.st = st }
}

// WithBodies has the bodies sent with every PUBLISH of a publication, beside its state: on its refreshes and its
// removal too, which carry no state (RFC 3903 §4.3, §4.5). TS 24.379 §9.2.1.2 step 2 needs it, of the mcptt-info
// identifying the client.
func WithBodies(parts ...sip.BodyPart) Option {
	return func(o *options) { o.attached = append(o.attached, parts...) }
}

// WithoutOptionTags leaves the option tags out of the Supported of an INVITE: the extensions its caller cannot
// honour end to end, as 100rel and 199 for a B2BUA whose other leg lacks them (RFC 3262 §4, RFC 6228 §8).
func WithoutOptionTags(tags ...string) Option {
	return func(o *options) { o.without = append(o.without, tags...) }
}

// WithPolicy sets the policy of the sessions of an INVITE, in place of Config.Policy.
func WithPolicy(p Policy) Option {
	return func(o *options) { o.policy = &p }
}

func (o options) policyOr(p Policy) Policy {
	if o.policy != nil {
		return *o.policy
	}

	return p
}

func (a *Agent) options(opts []Option) options {
	var o options

	for _, f := range opts {
		f(&o)
	}

	return o
}

func (o options) strand(a *Agent) *Strand {
	if o.st != nil {
		return o.st
	}

	return a.NewStrand()
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

	// A random word, globally unique by itself (RFC 3261 §8.1.1.4): the "@host" the grammar allows is the UAC's host,
	// not the Request-URI's, and a tel URI has none (§25.1: callid = word ["@" word]).
	if !req.Header.Has("Call-ID") {
		req.Header.Set("Call-ID", sip.NewTag())
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
	st    *Strand
	tmpl  *sip.Request
	done  func(*sip.Response, error)
	tries int
	final bool
}

func (c *outOfDialog) send() error {
	_, err := c.a.prepareAndSend(c.tmpl.Clone(), c.st.client(c.response, func(err error) { c.finish(nil, err) }))
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
	c.a.removeRequest(c)

	if c.done != nil {
		c.st.emit(func() { c.done(res, err) })
	}
}
