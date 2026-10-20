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
// 4h), and the Call-ID and CSeq of its third-party REGISTERs (RFC 3261 §10.2).
type asPeer struct {
	server sip.Server
	callID string
	cseq   uint32
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

// forgetASs drops what the S-CSCF keeps of the ASs for public identities once they are deregistered (TS 24.229
// §5.4.1.4, §5.4.1.5) or their unregistered profile is released.
func (r *Registrar) forgetASs(identityKeys []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range identityKeys {
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
