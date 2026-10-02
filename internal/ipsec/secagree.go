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

type Offer struct {
	Endpoint   Endpoint
	Integrity  Integrity
	Encryption Encryption
}

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

		if o, err := ParseOffer(m); err == nil {
			out = append(out, o)
		}
	}

	if !found {
		return nil, ErrNoOffer
	}

	return out, nil
}

func ParseOffer(m sip.SecurityMechanism) (Offer, error) {
	o, err := parseOffer(m)
	if err != nil {
		return Offer{}, fmt.Errorf("%s: %w", m, err)
	}

	return o, nil
}

func parseOffer(m sip.SecurityMechanism) (Offer, error) {
	var (
		o   Offer
		err error
		ps  = m.Params
	)

	if !strings.EqualFold(m.Name, Mechanism) {
		return o, ErrUnsupportedOffer
	}

	if o.Endpoint.SPIC, err = uintParam(ps, "spi-c", 1<<32-1); err != nil {
		return o, err
	}

	if o.Endpoint.SPIS, err = uintParam(ps, "spi-s", 1<<32-1); err != nil {
		return o, err
	}

	portC, err := uintParam(ps, "port-c", 1<<16-1)
	if err != nil {
		return o, err
	}

	portS, err := uintParam(ps, "port-s", 1<<16-1)
	if err != nil {
		return o, err
	}

	o.Endpoint.PortC, o.Endpoint.PortS = uint16(portC), uint16(portS)

	switch {
	case o.Endpoint.SPIC == o.Endpoint.SPIS:
		return o, errors.New("spi-c and spi-s are equal")
	case o.Endpoint.PortC == o.Endpoint.PortS:
		return o, errors.New("port-c and port-s are equal")
	case isSIPPort(o.Endpoint.PortC) || isSIPPort(o.Endpoint.PortS):
		return o, errors.New("protected port is 5060 or 5061")
	}

	alg, ok := ps.Get("alg")
	if !ok {
		return o, errors.New("missing alg")
	}

	if v, ok := ps.Get("prot"); ok && !strings.EqualFold(v, "esp") {
		return o, ErrUnsupportedOffer
	}

	if v, ok := ps.Get("mod"); ok && !strings.EqualFold(v, "trans") {
		return o, ErrUnsupportedOffer
	}

	o.Integrity = Integrity(strings.ToLower(alg))
	if _, err := integrityAlgo(o.Integrity); err != nil {
		return o, ErrUnsupportedOffer
	}

	o.Encryption = EncryptionNull
	if v, ok := ps.Get("ealg"); ok {
		o.Encryption = Encryption(strings.ToLower(v))
	}

	if _, err := encryptionAlgo(o.Encryption); err != nil {
		return o, ErrUnsupportedOffer
	}

	return o, nil
}

func isSIPPort(p uint16) bool {
	return p == 5060 || p == 5061
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

func (p Policy) Select(offers []Offer) (Offer, error) {
	find := func(i Integrity, e Encryption) (Offer, bool) {
		for _, o := range offers {
			if o.Integrity == i && o.Encryption == e {
				return o, true
			}
		}

		return Offer{}, false
	}

	order := []Encryption{EncryptionNull, AESCBC}

	switch p.Encryption {
	case EncryptionPreferred:
		order = []Encryption{AESCBC, EncryptionNull}
	case EncryptionRequired:
		order = []Encryption{AESCBC}
	}

	for _, e := range order {
		for _, i := range p.Integrity {
			if o, ok := find(i, e); ok {
				return o, nil
			}
		}
	}

	return Offer{}, ErrNoAlgorithm
}

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
