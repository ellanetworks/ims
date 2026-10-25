package affiliation

import (
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

// Bindings are the bindings of service authorisation (TS 24.379 §7.3.2 step 4) the participating function reads.
type Bindings interface {
	// Resolve is the MCPTT ID of the served user of req (registrar.Resolve): the originating MCPTT ID of a request
	// of P-Asserted-Identity ids (§9.2.2.2.3 step 2) and client, of the registration of its registration token, if
	// any (TS 24.229 §5.7.1.3B: a standalone transaction).
	Resolve(req *sip.Request, ids []sip.URI, client string) (sip.URI, bool)
	// Bound reports whether client is bound to the MCPTT ID id, at any IMPU.
	Bound(id sip.URI, client string) bool
}

// Request is a PUBLISH as the participating function receives it: the snapshot of the configuration it is handled
// with, the time, and the IMS public user identities of its originator: those of P-Asserted-Identity through the IMS,
// the From of a client in direct mode [PM 14].
type Request struct {
	*ua.Request

	Config     *settings.Snapshot
	Now        time.Time
	Originator []sip.URI
}

// HandlePublish is the receipt of an affiliation status change from a client (TS 24.379 §9.2.2.2.3), processed as
// RFC 3903 §6 has it, with the deviations of §9.2.1.2 NOTE 4, and answered; bs tells the bindings of service
// authorisation. A served user is the originator itself: the authorisation of another to change its affiliation
// (step 4) is of no user. The originating MCPTT ID is the one bound to the client of the publication, its tuple's or
// its entity-tag's, at the originator's IMPU (step 2): an IMPU may be of several users (TS 23.280 §8.3.1), a client
// of one, and the affiliations of a client live as long as its service authorisation (TS 23.280 §5.2.5). A log-off
// on another strand that the publication raced with is applied after it: the client is logged off again.
//
// The publications are not those of ua.PublicationStore: its resource is the Request-URI, the PSI, where here it is
// the served MCPTT ID of the body (step 1), and §9.2.1.2 NOTE 4 deviates from RFC 3903: the client's affiliations,
// not the publication, are the state, and a later publication replaces them whatever its entity-tag.
func (p *Participating) HandlePublish(r Request, bs Bindings) {
	req := r.Message()
	snap := r.Config

	// TS 24.379 §6.3.7.1.
	if settings.Key(req.URI) != settings.Key(snap.ParticipatingPSI()) {
		respond(r, 404)
		return
	}

	// RFC 3903 §6 step 2; condition 4.
	if ev, ok, err := req.Header.Event(); !ok || err != nil || ev.Type() != "presence" {
		respond(r, 489, sip.Field{Name: "Allow-Events", Value: "presence"})
		return
	}

	// Condition 2, step 1.
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

	// RFC 3903 §6 step 3.
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

	// Step 10, the client of the publication, for step 2.
	doc, clientID, groups, code := perUser(req)

	switch {
	case code != 0:
		respond(r, code)
		return
	case doc == nil && !conditional:
		// An initial publication carries the state (RFC 3903 §6 step 3).
		respond(r, 400)
		return
	case doc == nil:
		clientID = known
	}

	// Steps 2, 4.
	if orig, ok := bs.Resolve(req, r.Originator, clientID); !ok || settings.Key(orig) != settings.Key(id) {
		respond(r, 403)
		return
	}

	// Step 5; a malformed Expires is a malformed request (RFC 3261 §21.4.1).
	expires, err := req.Header.Expires()

	switch {
	case !req.Header.Has("Expires"), err == nil && expires != 0 && time.Duration(expires)*time.Second < MaxExpires:
		respond(r, 423, sip.Field{Name: "Min-Expires", Value: strconv.FormatInt(int64(MaxExpires/time.Second), 10)})
		return
	case err != nil:
		respond(r, 400)
		return
	}

	// Steps 6 to 8: the candidate expiration interval is the one asked for, the only one allowed.
	next := newETag()
	respond(r, 200, sip.Field{Name: "Expires", Value: strconv.FormatUint(uint64(expires), 10)},
		sip.Field{Name: "SIP-ETag", Value: next})

	switch {
	// Step 9.
	case doc != nil && settings.Key(must(doc.Entity)) != settings.Key(id):
		return
	// A refresh of a publication without state (RFC 3903 §6 step 5).
	case doc == nil && expires != 0:
		p.Publish(r.Now, Publication{User: u, Client: clientID, Groups: p.current(r.Now, id, clientID), Expires: MaxExpires}, next)
	default:
		var pid string
		if doc != nil {
			pid = doc.pid
		}

		p.Publish(r.Now, Publication{
			User: u, Client: clientID, Groups: groups, PID: pid, Expires: time.Duration(expires) * time.Second,
		}, next)
	}

	// A log-off of the client between Resolve and Publish, by another strand, found nothing to remove.
	if !bs.Bound(id, clientID) {
		p.LogOff(r.Now, id, clientID)
	}
}

// current are the groups client of id is interested in: affiliating or affiliated, for a refresh.
func (p *Participating) current(now time.Time, id sip.URI, client string) []sip.URI {
	var out []sip.URI

	for _, g := range p.Groups(now, id, client) {
		if g.Status == Affiliating || g.Status == Affiliated {
			out = append(out, g.ID)
		}
	}

	return out
}

// servedID is the MCPTT ID of the <mcptt-request-uri> of the mcptt-info of req, or the code to refuse it with: 400
// without, 403 if it is confidentiality protected, which the server, without keys, cannot decrypt [PM 9]: with the
// warning text "140 unable to decrypt XML content" (TS 24.379 §6.6.2.4.2 step 3).
func servedID(req *sip.Request) (sip.URI, int) {
	part, ok, err := req.Content().Find(mcpttinfo.ContentType)
	if err != nil || !ok {
		return sip.URI{}, 400
	}

	// A malformed element of another is no matter (F.1.3).
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

// perUser is the per-user affiliation information of req (TS 24.379 §9.3.1): its document, nil if none, the client
// ID of its tuple and the groups of its affiliations, or the code to refuse it with.
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

// respond sends the response; failing, the transaction is gone, and its client retransmits or gives up.
func respond(r Request, code int, header ...sip.Field) {
	_ = r.Respond(ua.Reply{Code: code, Header: header})
}

// newETag is an entity-tag not used before (RFC 3903 §6 step 6).
func newETag() string {
	var b [12]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

func must(s string) sip.URI {
	u, _ := sip.ParseURI(s)
	return u
}
