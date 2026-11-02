package affiliation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/mcpttinfo"
	"github.com/ellanetworks/ims/sip/pidf"
	"github.com/ellanetworks/ims/sip/pidfmc"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 24.379 clause 7.3.2
type Bindings interface {
	Resolve(req *sip.Request, ids []sip.URI, client string) (sip.URI, bool)
	Bound(id sip.URI, client string) bool
}

type Request struct {
	*ua.Request

	Config     *settings.Snapshot
	Now        time.Time
	Originator []sip.URI
}

// TS 24.379 clause 9.2.2.2.3
// TS 24.379 clause 9.2.1.2
// RFC 3903 section 6
func (p *Participating) HandlePublish(ctx context.Context, r Request, bs Bindings) {
	req := r.Message()
	snap := r.Config

	if settings.Key(req.URI) != settings.Key(snap.ParticipatingPSI()) {
		respond(r, 404)
		return
	}

	if ev, ok, err := req.Header.Event(); !ok || err != nil || ev.Type() != "presence" {
		respond(r, 489, sip.Field{Name: "Allow-Events", Value: "presence"})
		return
	}

	id, code := servedID(req)

	switch code {
	case 0:
	case 403:
		respond(r, code, identity.Warning(snap.ServiceConfiguration().Domain, identity.WarnUnableToDecrypt))
		return
	default:
		respond(r, code)
		return
	}

	u := snap.User(id)
	if u == nil {
		respond(r, 404)
		return
	}

	etag, conditional, err := req.Header.SIPIfMatch()
	if err != nil {
		respond(r, 400)
		return
	}

	var known string

	if conditional {
		c, ok := p.ETag(id, etag)
		if !ok {
			respond(r, 412)
			return
		}

		known = c
	}

	doc, clientID, groups, code := perUser(req)

	switch {
	case code != 0:
		respond(r, code)
		return
	case doc == nil && !conditional:
		respond(r, 400)
		return
	case doc == nil:
		clientID = known
	}

	if orig, ok := bs.Resolve(req, r.Originator, clientID); !ok || settings.Key(orig) != settings.Key(id) {
		respond(r, 403)
		return
	}

	expires, err := req.Header.Expires()

	switch {
	case !req.Header.Has("Expires"), err == nil && expires != 0 && time.Duration(expires)*time.Second < MaxExpires:
		respond(r, 423, sip.Field{Name: "Min-Expires", Value: strconv.FormatInt(int64(MaxExpires/time.Second), 10)})
		return
	case err != nil:
		respond(r, 400)
		return
	}

	next := newETag()
	respond(r, 200, sip.Field{Name: "Expires", Value: strconv.FormatUint(uint64(expires), 10)},
		sip.Field{Name: "SIP-ETag", Value: next})

	switch {
	case doc != nil && settings.Key(must(doc.Entity)) != settings.Key(id):
		return
	case doc == nil && expires != 0:
		p.Publish(ctx, r.Now, Publication{User: u, Client: clientID, Groups: p.current(r.Now, id, clientID), Expires: MaxExpires}, next)
	default:
		var pid string
		if doc != nil {
			pid = doc.pid
		}

		p.Publish(ctx, r.Now, Publication{
			User: u, Client: clientID, Groups: groups, PID: pid, Expires: time.Duration(expires) * time.Second,
		}, next)
	}

	if !bs.Bound(id, clientID) {
		p.LogOff(ctx, r.Now, id, clientID)
	}
}

func (p *Participating) current(now time.Time, id sip.URI, client string) []sip.URI {
	var out []sip.URI

	for _, g := range p.Groups(now, id, client) {
		if g.Status == Affiliating || g.Status == Affiliated {
			out = append(out, g.ID)
		}
	}

	return out
}

// TS 24.379 clause 6.6.2.4.2
func servedID(req *sip.Request) (sip.URI, int) {
	part, ok, err := req.Content().Find(mcpttinfo.ContentType)
	if err != nil || !ok {
		return sip.URI{}, 400
	}

	info, _ := mcpttinfo.Decode(part.Body)
	if info.Params == nil || info.Params.RequestURI == nil {
		return sip.URI{}, 400
	}

	if info.Params.RequestURI.Type == mcpttinfo.Encrypted {
		return sip.URI{}, 403
	}

	v, ok := info.Params.RequestURI.URI()
	if !ok {
		return sip.URI{}, 400
	}

	id, err := sip.ParseURI(v)
	if err != nil {
		return sip.URI{}, 400
	}

	return id, 0
}

type document struct {
	Entity string
	pid    string
}

// TS 24.379 clause 9.3.1
func perUser(req *sip.Request) (*document, string, []sip.URI, int) {
	part, ok, err := req.Content().Find(pidf.ContentType)
	if err != nil {
		return nil, "", nil, 400
	}

	if !ok {
		return nil, "", nil, 0
	}

	pres, err := pidf.Decode(part.Body)
	if err != nil && pres.Entity == "" {
		return nil, "", nil, 400
	}

	ext, _ := pidfmc.DecodePresence(pres)
	doc := &document{Entity: pres.Entity, pid: ext.PID}

	if len(pres.Tuples) == 0 {
		return doc, "", nil, 0
	}

	t := pres.Tuples[0]
	st, _ := pidfmc.DecodeStatus(t.Status)

	var groups []sip.URI

	for _, a := range st.Affiliations {
		if g, err := sip.ParseURI(a.Group); err == nil {
			groups = append(groups, g)
		}
	}

	return doc, t.ID, groups, 0
}

func respond(r Request, code int, header ...sip.Field) {
	_ = r.Respond(ua.Reply{Code: code, Header: header})
}

// RFC 3903 section 6
func newETag() string {
	var b [12]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

func must(s string) sip.URI {
	u, _ := sip.ParseURI(s)
	return u
}
