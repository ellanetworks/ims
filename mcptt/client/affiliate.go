package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/pidf"
	"github.com/ellanetworks/ims/sip/pidfmc"
	"github.com/ellanetworks/ims/sip/ua"
)

// maxExpires is the Expires of an affiliation PUBLISH of a user interested in a group (TS 24.379 §9.2.1.2 step 5a).
const maxExpires = "4294967295"

// ErrNotPublished is the error of de-affiliating from all groups without an affiliation published: an initial
// PUBLISH carries a body (RFC 3903 §4.2), so the client does not remove the implicit affiliations of its user
// profile, which end with the registration.
var ErrNotPublished = errors.New("client: no affiliation published")

// errPublicationEnded is the error of a PUBLISH whose publication ended without a response to tell.
var errPublicationEnded = errors.New("client: the affiliation publication ended")

// publication is the publication of the affiliations of the client (TS 24.379 §9.2.1.2) on a sip/ua publication:
// its PUBLISHes one at a time, refreshed as their 2xx grants (RFC 3903 §4.1), published anew after a 412 (§4.4).
type publication struct {
	p *ua.Publication
	// waiting are told by the next 2xx or the end of the publication: the caller of its initial PUBLISH, those of
	// a PUBLISH answered 412.
	waiting []func(*sip.Response, error)
	// removing is set by the removal; after are the groups to publish once removed, and their caller.
	removing  bool
	after     []sip.URI
	afterDone func(*sip.Response, error)
}

// Affiliate makes groups the groups the user is interested in at the client (TS 24.379 §9.2.1.2): the per-user
// affiliation information of all of them published to the participating function or, without any, the publication
// removed (step 4, Expires 0), which de-affiliates the client from those it had; ErrNotPublished without one. done
// receives the final response, or the error of the transaction; of a modification answered 412, the response to the
// publication sent anew (RFC 3903 §4.4).
//
// Every PUBLISH carries the mcptt-info naming the user (step 2), its refreshes and its removal too, and the
// modifications the state of all the groups (NOTE 4). They go one at a time, the latest state sent after the one in
// flight; the client refreshes the publication before the expiry its 2xx grants (RFC 3903 §6 step 4).
func (c *Client) Affiliate(groups []sip.URI, done func(res *sip.Response, err error)) error {
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
		return c.publish(groups, done)
	case pb.removing:
		// Published once removed, the latest groups only.
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
		err := pb.p.Remove(done)
		if errors.Is(err, ua.ErrState) {
			// Ended, its handler yet to run.
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

	err = pb.p.Modify([]sip.BodyPart{state}, func(res *sip.Response, err error) { c.modified(pb, done, res, err) })
	if errors.Is(err, ua.ErrState) {
		c.aff = nil
		return c.publish(groups, done)
	}

	return err
}

// publish sends the initial PUBLISH of groups (TS 24.379 §9.2.1.2), with c.mu held.
func (c *Client) publish(groups []sip.URI, done func(*sip.Response, error)) error {
	state, err := c.presence(groups)
	if err != nil {
		return err
	}

	info, err := mcpttinfo.Encode(mcpttinfo.Info{Params: &mcpttinfo.Params{RequestURI: mcpttinfo.URI(c.cfg.MCPTTID.String())}})
	if err != nil {
		return err
	}

	// Steps 1, 5a.
	req := sip.NewRequest("PUBLISH", c.cfg.ParticipatingPSI)
	req.Header.Set("To", sip.Address{URI: c.cfg.ParticipatingPSI}.String())
	req.Header.Set("Event", "presence")
	req.Header.Set("Expires", maxExpires)
	req.SetBodies(state)

	if err := c.originate(req); err != nil {
		return err
	}

	pb := &publication{waiting: []func(*sip.Response, error){done}}

	p, err := c.cfg.Agent.Publish(req, ua.PublicationHandler{
		Published: func(_ *ua.Publication, res *sip.Response) { c.published(pb, res) },
		Ended:     func(_ *ua.Publication, e ua.End) { c.publicationEnded(pb, e) },
	}, ua.On(c.st), ua.WithBodies(sip.NewBodyPart(mcpttinfo.ContentType, info)))
	if err != nil {
		return err
	}

	pb.p, c.aff = p, pb

	return nil
}

// modified is the final response to a modification: a 412 waits for the publication sent anew.
func (c *Client) modified(pb *publication, done func(*sip.Response, error), res *sip.Response, err error) {
	if err == nil && res.StatusCode == 412 {
		c.mu.Lock()
		defer c.mu.Unlock()

		pb.waiting = append(pb.waiting, done)

		return
	}

	done(res, err)
}

// published is a 2xx to a PUBLISH of the publication.
func (c *Client) published(pb *publication, res *sip.Response) {
	c.mu.Lock()
	ws := pb.waiting
	pb.waiting = nil
	c.mu.Unlock()

	for _, w := range ws {
		w(res, nil)
	}
}

// publicationEnded is the end of the publication: removed, rejected or expired; the groups asked for meanwhile then
// published anew.
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
			err = c.publish(groups, done)
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

// presence is the per-user affiliation information of groups (TS 24.379 §9.3.1.2, §9.2.1.2 step 5).
func (c *Client) presence(groups []sip.URI) (sip.BodyPart, error) {
	p := pidf.Presence{Entity: c.cfg.MCPTTID.String(), Tuples: []pidf.Tuple{{ID: c.cfg.ClientID}}}

	var st pidfmc.Status
	for _, g := range groups {
		// 5e: neither status nor expires.
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

// pid is a globally unique <p-id> (step 5f): 128 random bits.
func pid() string {
	var b [16]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}
