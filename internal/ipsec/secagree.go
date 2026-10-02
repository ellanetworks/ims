package ipsec

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

const Mechanism = "ipsec-3gpp"

// Offer is one usable ipsec-3gpp mechanism of a UE's Security-Client
// (TS 33.203 Annex H). Endpoint has no address: it is the packet's source.
type Offer struct {
	Endpoint   Endpoint
	Integrity  Integrity
	Encryption Encryption
}

// ParseOffers returns the ipsec-3gpp mechanisms this package supports, in the
// UE's order. Mechanisms with an unsupported protocol, mode or algorithm are
// skipped. It returns ErrNoOffer when no ipsec-3gpp mechanism is present.
func ParseOffers(ms []sip.SecurityMechanism) ([]Offer, error) {
	var (
		out   []Offer
		found bool
	)

	for _, m := range ms {
		if !strings.EqualFold(m.Name, Mechanism) {
			continue
		}

		found = true

		o, ok, err := parseOffer(m.Params)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}

		if !ok {
			continue
		}

		if len(out) > 0 && o.Endpoint != out[0].Endpoint {
			return nil, fmt.Errorf("%s: SPIs or ports differ from %s", m, Mechanism)
		}

		out = append(out, o)
	}

	if !found {
		return nil, ErrNoOffer
	}

	return out, nil
}

func parseOffer(ps sip.Params) (Offer, bool, error) {
	var (
		o   Offer
		err error
	)

	if o.Endpoint.SPIC, err = uintParam(ps, "spi-c", 1<<32-1); err != nil {
		return o, false, err
	}

	if o.Endpoint.SPIS, err = uintParam(ps, "spi-s", 1<<32-1); err != nil {
		return o, false, err
	}

	portC, err := uintParam(ps, "port-c", 1<<16-1)
	if err != nil {
		return o, false, err
	}

	portS, err := uintParam(ps, "port-s", 1<<16-1)
	if err != nil {
		return o, false, err
	}

	o.Endpoint.PortC, o.Endpoint.PortS = uint16(portC), uint16(portS)

	alg, ok := ps.Get("alg")
	if !ok {
		return o, false, errors.New("missing alg")
	}

	if v, ok := ps.Get("prot"); ok && !strings.EqualFold(v, "esp") {
		return o, false, nil
	}

	if v, ok := ps.Get("mod"); ok && !strings.EqualFold(v, "trans") {
		return o, false, nil
	}

	o.Integrity = Integrity(strings.ToLower(alg))
	if _, err := integrityAlgo(o.Integrity); err != nil {
		return o, false, nil
	}

	o.Encryption = EncryptionNull
	if v, ok := ps.Get("ealg"); ok {
		o.Encryption = Encryption(strings.ToLower(v))
	}

	if _, err := encryptionAlgo(o.Encryption); err != nil {
		return o, false, nil
	}

	return o, true, nil
}

func uintParam(ps sip.Params, name string, maxValue uint64) (uint32, error) {
	v, ok := ps.Get(name)
	if !ok {
		return 0, fmt.Errorf("missing %s", name)
	}

	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 || n > maxValue {
		return 0, fmt.Errorf("%s %q: not a number from 1 to %d", name, v, maxValue)
	}

	return uint32(n), nil
}

// Policy is the P-CSCF's ordered list of algorithms.
type Policy struct {
	Integrity  []Integrity
	Encryption EncryptionPolicy
}

func DefaultPolicy() Policy {
	return Policy{Integrity: []Integrity{HMACSHA196, HMACMD596}, Encryption: EncryptionOff}
}

func (p Policy) Validate() error {
	if len(p.Integrity) == 0 {
		return errors.New("no integrity algorithm")
	}

	for _, i := range p.Integrity {
		if _, err := integrityAlgo(i); err != nil {
			return err
		}
	}

	switch p.Encryption {
	case EncryptionOff, EncryptionPreferred, EncryptionRequired:
		return nil
	}

	return fmt.Errorf("unknown encryption policy %q", p.Encryption)
}

// Select returns the first combination on the P-CSCF's list that the UE
// offered (TS 33.203 §7.2). Encryption takes precedence over the integrity
// order when the policy asks for it. With encryption off, null is selected
// whatever encryption the UE offered, since every UE supports it (IR.92 §5.3).
func (p Policy) Select(offers []Offer) (Offer, error) {
	find := func(i Integrity, e Encryption) (Offer, bool) {
		for _, o := range offers {
			if o.Integrity == i && (e == "" || o.Encryption == e) {
				return o, true
			}
		}

		return Offer{}, false
	}

	if p.Encryption != EncryptionOff {
		for _, i := range p.Integrity {
			if o, ok := find(i, AESCBC); ok {
				return o, nil
			}
		}

		if p.Encryption == EncryptionRequired {
			return Offer{}, ErrNoAlgorithm
		}
	}

	for _, i := range p.Integrity {
		if o, ok := find(i, ""); ok {
			o.Encryption = EncryptionNull
			return o, nil
		}
	}

	return Offer{}, ErrNoAlgorithm
}

// Server returns the Security-Server mechanism with which Local, the P-CSCF,
// announces the set (TS 33.203 §7.2, Annex H). ealg=null is explicit.
func (s Set) Server() sip.SecurityMechanism {
	return sip.SecurityMechanism{Name: Mechanism, Params: sip.Params{
		{Name: "q", Value: "0.1"},
		{Name: "prot", Value: "esp"},
		{Name: "mod", Value: "trans"},
		{Name: "spi-c", Value: strconv.FormatUint(uint64(s.Local.SPIC), 10)},
		{Name: "spi-s", Value: strconv.FormatUint(uint64(s.Local.SPIS), 10)},
		{Name: "port-c", Value: strconv.FormatUint(uint64(s.Local.PortC), 10)},
		{Name: "port-s", Value: strconv.FormatUint(uint64(s.Local.PortS), 10)},
		{Name: "alg", Value: string(s.Integrity)},
		{Name: "ealg", Value: string(s.Encryption)},
	}}
}

// KeysFromChallenge returns CK and IK from the ck and ik parameters of the
// S-CSCF's WWW-Authenticate challenge (TS 24.229 §5.4.1.2.1).
func KeysFromChallenge(a sip.Auth) (Keys, error) {
	var k Keys

	for _, p := range []struct {
		name string
		dst  *[]byte
	}{{"ck", &k.CK}, {"ik", &k.IK}} {
		v, ok := a.Params.Get(p.name)
		if !ok {
			return Keys{}, fmt.Errorf("missing %s", p.name)
		}

		b, err := hex.DecodeString(sip.Unquote(v))
		if err != nil || len(b) != 16 {
			return Keys{}, fmt.Errorf("%s: %w", p.name, ErrBadKeys)
		}

		*p.dst = b
	}

	return k, nil
}
