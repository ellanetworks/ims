package ua

import (
	"context"
	"errors"
	"fmt"

	"github.com/ellanetworks/ims/sip"
)

func (a *Agent) Request(ctx context.Context, req *sip.Request, done func(res *sip.Response, err error), opts ...Option) error {
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

	if err := c.st.do(func() error { return c.send(ctx) }); err != nil {
		a.removeRequest(c)
		return err
	}

	return nil
}

type Option func(*options)

type options struct {
	st       *Strand
	policy   *Policy
	without  []string
	attached []sip.BodyPart
}

func On(st *Strand) Option {
	return func(o *options) { o.st = st }
}

// RFC 3903 section 4.3
// RFC 3903 section 4.5
// TS 24.379 clause 9.2.1.2
func WithBodies(parts ...sip.BodyPart) Option {
	return func(o *options) { o.attached = append(o.attached, parts...) }
}

func WithoutOptionTags(tags ...string) Option {
	return func(o *options) { o.without = append(o.without, tags...) }
}

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

// RFC 3261 section 8.1.3.5
// RFC 3261 section 22.2
func nextCSeq(req *sip.Request) {
	cseq, _ := req.Header.CSeq()
	req.Header.Set("CSeq", sip.CSeq{Seq: cseq.Seq + 1, Method: req.Method}.String())
}

type outOfDialog struct {
	a     *Agent
	st    *Strand
	tmpl  *sip.Request
	done  func(*sip.Response, error)
	tries int
	final bool
}

func (c *outOfDialog) send(ctx context.Context) error {
	_, err := c.a.prepareAndSend(ctx, c.tmpl.Clone(), c.st.client(c.response, func(_ context.Context, err error) { c.finish(nil, err) }))
	return err
}

func (c *outOfDialog) response(ctx context.Context, res *sip.Response) {
	if res.IsProvisional() || c.final {
		return
	}

	next := c.tmpl.Clone()
	nextCSeq(next)

	if c.a.authenticate(next, res, c.tries) {
		c.tries++
		c.tmpl = next

		if err := c.send(ctx); err != nil {
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
