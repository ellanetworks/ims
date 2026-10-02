// Package ipsec negotiates and installs the IPsec ESP security associations
// between a UE and the P-CSCF (TS 33.203 §6-7, Annexes H and I). The kernel
// does ESP; this package selects the algorithms, derives the keys and manages
// the SAs and policies through netlink XFRM.
package ipsec

import (
	"errors"
	"fmt"
	"net/netip"
)

type Integrity string

const (
	HMACSHA196 Integrity = "hmac-sha-1-96"
	HMACMD596  Integrity = "hmac-md5-96"
)

type Encryption string

const (
	EncryptionNull Encryption = "null"
	AESCBC         Encryption = "aes-cbc"
)

// EncryptionPolicy is the P-CSCF's policy on confidentiality (TS 33.203 §7.2).
type EncryptionPolicy string

const (
	// EncryptionOff selects null encryption.
	EncryptionOff EncryptionPolicy = "off"
	// EncryptionPreferred encrypts when the UE offers an encryption algorithm.
	EncryptionPreferred EncryptionPolicy = "preferred"
	// EncryptionRequired rejects a UE that offers no encryption algorithm.
	EncryptionRequired EncryptionPolicy = "required"
)

var (
	ErrNoOffer       = errors.New("no ipsec-3gpp mechanism offered")
	ErrNoAlgorithm   = errors.New("no acceptable algorithm offered")
	ErrBadKeys       = errors.New("ck and ik must be 128 bits")
	ErrSPIsExhausted = errors.New("no free SPI")
)

// Endpoint is one side of a set of SAs: its address, its protected client and
// server ports, and the SPIs of its inbound SAs at those ports.
type Endpoint struct {
	Addr  netip.Addr
	PortC uint16
	PortS uint16
	SPIC  uint32
	SPIS  uint32
}

// Set is the two pairs of SAs between Local and Remote (TS 33.203 §7.1).
type Set struct {
	Local      Endpoint
	Remote     Endpoint
	Integrity  Integrity
	Encryption Encryption
}

// Reverse returns the same set seen from Remote.
func (s Set) Reverse() Set {
	s.Local, s.Remote = s.Remote, s.Local
	return s
}

func (s Set) validate() error {
	l, r := s.Local, s.Remote

	switch {
	case !l.Addr.IsValid() || !r.Addr.IsValid():
		return errors.New("missing address")
	case l.Addr.Is4In6() || r.Addr.Is4In6():
		return errors.New("IPv4-mapped IPv6 address")
	case l.Addr.Is4() != r.Addr.Is4():
		return errors.New("mixed IPv4 and IPv6 addresses")
	case l.PortC == 0 || l.PortS == 0 || r.PortC == 0 || r.PortS == 0:
		return errors.New("missing port")
	case l.SPIC == 0 || l.SPIS == 0 || r.SPIC == 0 || r.SPIS == 0:
		return errors.New("missing SPI")
	}

	if _, err := integrityAlgo(s.Integrity); err != nil {
		return err
	}

	_, err := encryptionAlgo(s.Encryption)

	return err
}

func (s Set) String() string {
	return fmt.Sprintf("%s [c %d spi %d, s %d spi %d] <-> %s [c %d spi %d, s %d spi %d] %s/%s",
		s.Local.Addr, s.Local.PortC, s.Local.SPIC, s.Local.PortS, s.Local.SPIS,
		s.Remote.Addr, s.Remote.PortC, s.Remote.SPIC, s.Remote.PortS, s.Remote.SPIS,
		s.Integrity, s.Encryption)
}

// Keys are the IMS-AKA keys CK and IK of the challenge (TS 33.203 §6.1).
type Keys struct {
	CK []byte
	IK []byte
}

type algo struct {
	name  string
	trunc int
	key   []byte
}

// integrityKey and encryptionKey expand IK and CK for ESP (TS 33.203
// Annex I). hmac-md5-96 is no longer in Annex H but phones still offer it;
// its key is IK.
func (k Keys) integrityKey(i Integrity) ([]byte, error) {
	if len(k.IK) != 16 {
		return nil, ErrBadKeys
	}

	switch i {
	case HMACSHA196:
		return append(append([]byte(nil), k.IK...), 0, 0, 0, 0), nil
	case HMACMD596:
		return append([]byte(nil), k.IK...), nil
	}

	return nil, fmt.Errorf("unsupported integrity algorithm %q", i)
}

func (k Keys) encryptionKey(e Encryption) ([]byte, error) {
	switch e {
	case EncryptionNull:
		return nil, nil
	case AESCBC:
		if len(k.CK) != 16 {
			return nil, ErrBadKeys
		}

		return append([]byte(nil), k.CK...), nil
	}

	return nil, fmt.Errorf("unsupported encryption algorithm %q", e)
}

func integrityAlgo(i Integrity) (algo, error) {
	switch i {
	case HMACSHA196:
		return algo{name: "hmac(sha1)", trunc: 96}, nil
	case HMACMD596:
		return algo{name: "hmac(md5)", trunc: 96}, nil
	}

	return algo{}, fmt.Errorf("unsupported integrity algorithm %q", i)
}

func encryptionAlgo(e Encryption) (algo, error) {
	switch e {
	case EncryptionNull:
		return algo{name: "ecb(cipher_null)"}, nil
	case AESCBC:
		return algo{name: "cbc(aes)"}, nil
	}

	return algo{}, fmt.Errorf("unsupported encryption algorithm %q", e)
}

func (k Keys) algos(s Set) (auth, crypt algo, err error) {
	if auth, err = integrityAlgo(s.Integrity); err != nil {
		return
	}

	if crypt, err = encryptionAlgo(s.Encryption); err != nil {
		return
	}

	if auth.key, err = k.integrityKey(s.Integrity); err != nil {
		return
	}

	crypt.key, err = k.encryptionKey(s.Encryption)

	return
}
