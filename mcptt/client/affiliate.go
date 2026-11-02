package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/pidf"
	"github.com/ellanetworks/ims/sip/pidfmc"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 24.379 clause 9.2.1.2
const maxExpires = "4294967295"

var ErrNotPublished = errors.New("client: no affiliation published")

var errPublicationEnded = errors.New("client: the affiliation publication ended")

// TS 24.379 clause 9.2.1.2
// RFC 3903 section 4.1
// RFC 3903 section 4.4
type publication struct {
	p         *ua.Publication
	waiting   []func(*sip.Response, error)
	removing  bool
	after     []sip.URI
	afterDone func(*sip.Response, error)
}

// TS 24.379 clause 9.2.1.2
// RFC 3903 section 4.4
// RFC 3903 section 6
func (c *Client) Affiliate(ctx context.Context, groups []sip.URI, done func(res *sip.Response, err error)) error {
	if done == nil {
		done = func(*sip.Response, error) {}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	pb := c.aff

	switch {
	case pb == nil && len(groups) == 0:
		return ErrNotPublished
	case pb == nil:
		return c.publish(ctx, groups, done)
	case pb.removing:
		if stale := pb.afterDone; stale != nil {
			c.st.Post(func() { stale(nil, ua.ErrStale) })
		}

		pb.after, pb.afterDone = nil, nil

		if len(groups) == 0 {
			pb.waiting = append(pb.waiting, done)
		} else {
			pb.after, pb.afterDone = groups, done
		}

		return nil
	case len(groups) == 0:
		err := pb.p.Remove(ctx, done)
		if errors.Is(err, ua.ErrState) {
			c.aff = nil
			return ErrNotPublished
		}

		pb.removing = err == nil

		return err
	}

	state, err := c.presence(groups)
	if err != nil {
		return err
	}

	err = pb.p.Modify(ctx, []sip.BodyPart{state}, func(res *sip.Response, err error) { c.modified(pb, done, res, err) })
	if errors.Is(err, ua.ErrState) {
		c.aff = nil
		return c.publish(ctx, groups, done)
	}

	return err
}

// TS 24.379 clause 9.2.1.2
func (c *Client) publish(ctx context.Context, groups []sip.URI, done func(*sip.Response, error)) error {
	state, err := c.presence(groups)
	if err != nil {
		return err
	}

	info, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{RequestURI: mcpttinfo.URI(c.cfg.MCPTTID.String())}})
	if err != nil {
		return err
	}

	req := sip.NewRequest("PUBLISH", c.cfg.ParticipatingPSI)
	req.Header.Set("To", sip.Address{URI: c.cfg.ParticipatingPSI}.String())
	req.Header.Set("Event", "presence")
	req.Header.Set("Expires", maxExpires)
	req.SetBodies(state)

	if err := c.originate(req); err != nil {
		return err
	}

	pb := &publication{waiting: []func(*sip.Response, error){done}}

	p, err := c.cfg.Agent.Publish(ctx, req, ua.PublicationHandler{
		Published: func(_ context.Context, _ *ua.Publication, res *sip.Response) { c.published(pb, res) },
		Ended:     func(_ context.Context, _ *ua.Publication, e ua.End) { c.publicationEnded(pb, e) },
	}, ua.On(c.st), ua.WithBodies(sip.NewBodyPart(mcpttinfo.ContentType, info)))
	if err != nil {
		return err
	}

	pb.p, c.aff = p, pb

	return nil
}

func (c *Client) modified(pb *publication, done func(*sip.Response, error), res *sip.Response, err error) {
	if err == nil && res.StatusCode == 412 {
		c.mu.Lock()
		defer c.mu.Unlock()

		pb.waiting = append(pb.waiting, done)

		return
	}

	done(res, err)
}

func (c *Client) published(pb *publication, res *sip.Response) {
	c.mu.Lock()
	ws := pb.waiting
	pb.waiting = nil
	c.mu.Unlock()

	for _, w := range ws {
		w(res, nil)
	}
}

func (c *Client) publicationEnded(pb *publication, e ua.End) {
	c.mu.Lock()

	ws, groups, done := pb.waiting, pb.after, pb.afterDone
	pb.waiting, pb.after, pb.afterDone = nil, nil, nil

	if c.aff == pb {
		c.aff = nil
	}

	var err error

	if done != nil {
		if c.aff == nil {
			err = c.publish(context.Background(), groups, done)
		} else {
			err = ua.ErrStale
		}
	}

	c.mu.Unlock()

	res, endErr := e.Response, e.Err
	if res == nil && endErr == nil {
		endErr = errPublicationEnded
	}

	for _, w := range ws {
		w(res, endErr)
	}

	if err != nil {
		done(nil, err)
	}
}

// TS 24.379 clause 9.3.1.2
// TS 24.379 clause 9.2.1.2
func (c *Client) presence(groups []sip.URI) (sip.BodyPart, error) {
	p := pidf.Presence{Entity: c.cfg.MCPTTID.String(), Tuples: []pidf.Tuple{{ID: c.cfg.ClientID}}}

	var st pidfmc.Status
	for _, g := range groups {
		st.Affiliations = append(st.Affiliations, pidfmc.Affiliation{Group: g.String()})
	}

	if err := pidfmc.SetStatus(&p.Tuples[0].Status, st); err != nil {
		return sip.BodyPart{}, err
	}

	if err := pidfmc.SetPresence(&p, pidfmc.Presence{PID: pid()}); err != nil {
		return sip.BodyPart{}, err
	}

	doc, err := pidf.Encode(p)
	if err != nil {
		return sip.BodyPart{}, err
	}

	return sip.NewBodyPart(pidf.ContentType, doc), nil
}

func pid() string {
	var b [16]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}
