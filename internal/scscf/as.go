package scscf

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/ellanetworks/ims/sip"
)

// TS 24.229 clause 5.4.0
// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
// RFC 3261 section 10.2
type asPeer struct {
	server  sip.Server
	callID  string
	cseq    uint32
	termIOI string

	pending []peerJob
	sending bool
}

type peerJob struct {
	tp     *thirdParty
	forget bool
}

func (p *asPeer) blank() bool {
	return !p.server.Addr.IsValid() && p.callID == "" && p.termIOI == "" && !p.sending && len(p.pending) == 0
}

var errUntrustedAS = errors.New("the AS is outside the trust domain")

// RFC 3263 section 4
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

// RFC 3261 section 10.2
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

// TS 24.229 clause 5.4.1.7
func (r *Registrar) storeTermIOI(impu, serverName, callID, ioi string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p := r.peers[identityKeyOf(impu)][serverName]; p != nil && p.callID == callID {
		p.termIOI = ioi
	}
}

// TS 24.229 clause 5.4.1.4
// TS 24.229 clause 5.4.1.5
// TS 24.229 clause 5.4.1.7
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

func (p *asPeer) forget() {
	p.server, p.callID, p.cseq, p.termIOI = sip.Server{}, "", 0, ""
}

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
