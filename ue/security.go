package ue

import (
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
)

// TS 33.203 clause 6.1
// TS 33.203 annex H
type Integrity string

const (
	HMACSHA196 Integrity = "hmac-sha-1-96"
	HMACMD596  Integrity = "hmac-md5-96"
)

// TS 33.203 clause 6.2
// TS 33.203 annex H
type Encryption string

const (
	EncryptionNull   Encryption = "null"
	EncryptionAESCBC Encryption = "aes-cbc"
)

// TS 33.203 clause 7.2
type Offer struct {
	Integrity  Integrity
	Encryption Encryption
}

func DefaultOffers() []Offer {
	return []Offer{
		{HMACSHA196, EncryptionNull},
		{HMACMD596, EncryptionNull},
	}
}

// TS 33.203 clause 7.1
type Endpoint struct {
	Addr         netip.Addr
	PortC, PortS uint16
	SPIC, SPIS   uint32
}

// TS 33.203 clause 7.1
type SASet struct {
	Local, Remote Endpoint
	Integrity     Integrity
	Encryption    Encryption
}

func (s SASet) String() string { return toIPsec(s).String() }

// TS 33.203 clause 6.1
type Keys struct {
	CK []byte
	IK []byte
}

var errBadKeys = errors.New("ue: CK and IK must be 128 bits")

// TS 33.203 annex I
func (k Keys) IntegrityKey(i Integrity) ([]byte, error) {
	if len(k.IK) != 16 {
		return nil, errBadKeys
	}

	switch i {
	case HMACSHA196:
		return append(append([]byte(nil), k.IK...), 0, 0, 0, 0), nil
	case HMACMD596:
		return append([]byte(nil), k.IK...), nil
	}

	return nil, fmt.Errorf("ue: unsupported integrity algorithm %q", i)
}

// TS 33.203 annex I
func (k Keys) EncryptionKey(e Encryption) ([]byte, error) {
	switch e {
	case EncryptionNull:
		return nil, nil
	case EncryptionAESCBC:
		if len(k.CK) != 16 {
			return nil, errBadKeys
		}

		return append([]byte(nil), k.CK...), nil
	}

	return nil, fmt.Errorf("ue: unsupported encryption algorithm %q", e)
}

// TS 33.203 clause 7.1
// TS 33.203 clause 7.4
type IPsec interface {
	AllocateSPI(local netip.Addr) (uint32, error)
	ReleaseSPI(spi uint32)

	Install(s SASet, k Keys) error
	Remove(s SASet) error

	Protect(s SASet, port uint16, c syscall.RawConn) error
}

// TS 33.203 clause 7.4
type SAState int

const (
	Temporary SAState = iota
	Established
	Old
)

func (s SAState) String() string {
	switch s {
	case Temporary:
		return "temporary"
	case Established:
		return "established"
	case Old:
		return "old"
	}

	return fmt.Sprintf("SAState(%d)", int(s))
}

type SA struct {
	Set     SASet
	Keys    Keys
	State   SAState
	Expires time.Time
}

func toIPsec(s SASet) ipsec.Set {
	ep := func(e Endpoint) ipsec.Endpoint {
		return ipsec.Endpoint{Addr: e.Addr, PortC: e.PortC, PortS: e.PortS, SPIC: e.SPIC, SPIS: e.SPIS}
	}

	return ipsec.Set{
		Local:      ep(s.Local),
		Remote:     ep(s.Remote),
		Integrity:  ipsec.Integrity(s.Integrity),
		Encryption: ipsec.Encryption(s.Encryption),
	}
}

func endpoint(e ipsec.Endpoint) Endpoint {
	return Endpoint{Addr: e.Addr, PortC: e.PortC, PortS: e.PortS, SPIC: e.SPIC, SPIS: e.SPIS}
}
