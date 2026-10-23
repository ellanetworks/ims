package scscf

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/ellanetworks/ims/sip"
)

// asPeer is what the S-CSCF keeps of an AS for a public identity, by the identity's key and the AS's SIP URI as the
// iFC names it, with all its parameters (TS 24.229 §5.4.0): the server it sent the identity's first request or
// third-party REGISTER to, where the later ones go too (TS 24.229 §5.4.0, §5.4.1.7, §5.4.3.2 step 5i, §5.4.3.3 step
// 4h), the Call-ID and CSeq of its third-party REGISTERs (RFC 3261 §10.2), and the type 3 term-ioi of the last
// response to one (TS 24.229 §5.4.1.7). pending are the third-party REGISTERs to it, in the order of their
// registration events, each sent once the previous one has its final response or has timed out (RFC 3261 §10.2);
// sending tells that one of them is under way.
type asPeer struct {
	server  sip.Server
	callID  string
	cseq    uint32
	termIOI string

	pending []peerJob
	sending bool
}

// peerJob is a third-party REGISTER to an AS, or, with forget, the point from which the S-CSCF no longer keeps what
// it knew of the AS for the identity: the identity was deregistered.
type peerJob struct {
	tp     *thirdParty
	forget bool
}

func (p *asPeer) blank() bool {
	return !p.server.Addr.IsValid() && p.callID == "" && p.termIOI == "" && !p.sending && len(p.pending) == 0
}

var errUntrustedAS = errors.New("the AS is outside the trust domain")

// asServer is the server of the AS named serverName for the public identity u: the one stored for them, or else the
// first that the AS URI resolves to (RFC 3263 §4) in the address family of a listener of the S-CSCF and in the trust
// domain, which is then stored. The trust domain goes by the addresses the URI resolves to, whatever its form.
func (r *Registrar) asServer(ctx context.Context, u sip.URI, serverName string) (sip.Server, error) {
	id := identityKey(u)

	r.mu.Lock()
	p := r.peers[id][serverName]
	r.mu.Unlock()

	if p != nil && p.server.Addr.IsValid() {
		return p.server, nil
	}

	asURI, err := sip.ParseURI(serverName)
	if err != nil {
		return sip.Server{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.cfg.ASTimeout)
	defer cancel()

	servers, err := r.cfg.Resolver.Resolve(ctx, asURI)
	if err != nil {
		return sip.Server{}, err
	}

	// An AS named by its address resolves to it every time: nothing to store.
	_, literal := asURI.Addr()

	err = fmt.Errorf("no S-CSCF listener in the address family of %s", serverName)

	for _, srv := range servers {
		switch _, ok := r.listener(srv.Addr.Addr()); {
		case !ok:
			continue
		case !r.trusted(srv.Addr.Addr()):
			err = fmt.Errorf("%w: %s", errUntrustedAS, srv.Addr.Addr())
			continue
		case literal:
			return srv, nil
		}

		r.mu.Lock()

		if p := r.peerLocked(id, serverName); p.server.Addr.IsValid() {
			srv = p.server
		} else {
			p.server = srv
		}

		r.mu.Unlock()

		return srv, nil
	}

	return sip.Server{}, err
}

// registerSequence is the Call-ID and the next CSeq of the third-party REGISTERs to an AS for a public identity: the
// same Call-ID for all of them, the CSeq one more each time (RFC 3261 §10.2).
func (r *Registrar) registerSequence(u sip.URI, serverName string, local netip.AddrPort) (string, uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p := r.peerLocked(identityKey(u), serverName)

	if p.callID == "" {
		p.callID = sip.NewTag() + "@" + sip.FormatHost(local.Addr())
	}

	p.cseq++

	return p.callID, p.cseq
}

// storeTermIOI keeps the term-ioi of a response of an AS to a third-party REGISTER with the Call-ID callID for the
// public identity impu (TS 24.229 §5.4.1.7), unless what the S-CSCF kept of the AS for the identity is gone: it was
// deregistered since.
func (r *Registrar) storeTermIOI(impu, serverName, callID, ioi string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p := r.peers[identityKeyOf(impu)][serverName]; p != nil && p.callID == callID {
		p.termIOI = ioi
	}
}

// forgetASs drops what the S-CSCF keeps of the ASs for public identities once they are deregistered (TS 24.229
// §5.4.1.4, §5.4.1.5) or their unregistered profile is released: at once for an AS with no third-party REGISTER
// under way, or else after those queued so far, which still go to the server stored (TS 24.229 §5.4.1.7).
func (r *Registrar) forgetASs(identityKeys []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range identityKeys {
		for serverName, p := range r.peers[id] {
			if p.sending {
				p.pending = append(p.pending, peerJob{forget: true})
			} else {
				delete(r.peers[id], serverName)
			}
		}

		if len(r.peers[id]) == 0 {
			delete(r.peers, id)
		}
	}
}

// forget is a forget job reached in the queue of p: the next third-party REGISTER, if any, starts anew.
func (p *asPeer) forget() {
	p.server, p.callID, p.cseq, p.termIOI = sip.Server{}, "", 0, ""
}

// dropLocked removes what the S-CSCF keeps of the AS named serverName for the identity id, if p is still it and
// holds nothing.
func (r *Registrar) dropLocked(id, serverName string, p *asPeer) {
	if r.peers[id][serverName] != p || !p.blank() {
		return
	}

	delete(r.peers[id], serverName)

	if len(r.peers[id]) == 0 {
		delete(r.peers, id)
	}
}

func (r *Registrar) peerLocked(id, serverName string) *asPeer {
	peers := r.peers[id]
	if peers == nil {
		peers = make(map[string]*asPeer)
		r.peers[id] = peers
	}

	p := peers[serverName]
	if p == nil {
		p = &asPeer{}
		peers[serverName] = p
	}

	return p
}

func (r *Registrar) trusted(a netip.Addr) bool {
	return r.cfg.Trust != nil && r.cfg.Trust.Trusted(a)
}
