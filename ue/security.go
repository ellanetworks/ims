package ue

import (
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/internal/ipsec"
)

// Integrity is an integrity algorithm of the ipsec-3gpp mechanism (TS 33.203 §6.1, Annex H "alg").
type Integrity string

const (
	HMACSHA196 Integrity = "hmac-sha-1-96"
	HMACMD596  Integrity = "hmac-md5-96"
)

// Encryption is an encryption algorithm of the ipsec-3gpp mechanism (TS 33.203 §6.2, Annex H "ealg").
type Encryption string

const (
	EncryptionNull   Encryption = "null"
	EncryptionAESCBC Encryption = "aes-cbc"
)

// Offer is an algorithm pair the UE offers in its Security-Client (TS 33.203 §7.2).
type Offer struct {
	Integrity  Integrity
	Encryption Encryption
}

// DefaultOffers are the offers of a UE configured without: integrity protection only.
func DefaultOffers() []Offer {
	return []Offer{
		{HMACSHA196, EncryptionNull},
		{HMACMD596, EncryptionNull},
	}
}

// Endpoint is one end of an SA set (TS 33.203 §7.1): its address, its protected client and server ports, and the SPIs
// of the SAs it receives on, at its client port and at its server port.
type Endpoint struct {
	Addr         netip.Addr
	PortC, PortS uint16
	SPIC, SPIS   uint32
}

// SASet is the set of four SAs between the UE and the P-CSCF that one authenticated registration sets up (TS 33.203
// §7.1), ESP in transport mode, for UDP and TCP:
//   - from Local.PortC to Remote.PortS, SPI Remote.SPIS, with the responses back on Local.PortC, SPI Local.SPIC;
//   - from Remote.PortC to Local.PortS, SPI Local.SPIS, with the responses back on Remote.PortC, SPI Remote.SPIC.
//
// Over UDP the P-CSCF sends its responses from Remote.PortC to Local.PortS (TS 33.203 §7.1), on the second pair.
type SASet struct {
	Local, Remote Endpoint
	Integrity     Integrity
	Encryption    Encryption
}

func (s SASet) String() string { return toIPsec(s).String() }

// Keys are the keys of an AKA run (TS 33.203 §6.1), from which those of the SAs are derived.
type Keys struct {
	CK []byte
	IK []byte
}

var errBadKeys = errors.New("ue: CK and IK must be 128 bits")

// IntegrityKey is the integrity key of the SAs, IK_ESP (TS 33.203 Annex I): IK, followed by 32 zero bits for
// HMAC-SHA-1-96.
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

// EncryptionKey is the encryption key of the SAs, CK_ESP (TS 33.203 Annex I): CK for AES-CBC, none for null.
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

// IPsec is the IPsec of the platform the UE runs on, in which it sets up the SAs of each authenticated registration
// and removes them (TS 33.203 §7.1, §7.4): the kernel's SAD and SPD of selectors, or transforms on the sockets of the
// protected ports. Its methods may be called from any goroutine.
type IPsec interface {
	// AllocateSPI reserves an SPI of the SAs to the UE's address local, for its Security-Client (TS 33.203 §7.2), the
	// UE's spi_uc or spi_us: unique among the SPIs of SAs to local. ReleaseSPI releases one, once its SAs are removed or
	// never set up.
	AllocateSPI(local netip.Addr) (uint32, error)
	ReleaseSPI(spi uint32)

	// Install sets up the four SAs of s with the keys of k; Remove removes them.
	Install(s SASet, k Keys) error
	Remove(s SASet) error

	// Protect has the SAs of s, Installed, protect socket c, bound to the UE's port port of s, s.Local.PortC or
	// s.Local.PortS: in on that port, out to the P-CSCF's port of its pair (§7.1). The UE calls it for each socket of a
	// protected port, its UDP socket and TCP listener once s is installed, a TCP connection it dials from it before it
	// connects; c is valid during the call only. An IPsec of selectors does nothing. An IPsec of sockets that holds
	// one transform per direction per socket protects port_us with the latest set only.
	Protect(s SASet, port uint16, c syscall.RawConn) error
}

// SAState is the state of an SA set (TS 33.203 §7.4).
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

// SA is an SA set of the UE, with its keys, state and expiry.
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
