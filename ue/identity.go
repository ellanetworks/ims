package ue

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Identities are the identities of the UE in the IMS: its private user identity, the public user identity it
// registers, and its home network domain name (TS 23.003 §13; TS 24.229 §5.1.1.1A): those of the ISIM (TS 31.103
// §4.2.2–§4.2.4), or derived from the IMSI without one (IdentitiesFromIMSI).
type Identities struct {
	IMPI       string
	IMPU       string
	HomeDomain string
}

func (id Identities) check() error {
	if id.IMPI == "" || id.IMPU == "" || id.HomeDomain == "" {
		return errors.New("ue: no IMPI, IMPU or home domain")
	}

	return nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return s != ""
}

// IdentitiesFromIMSI are the identities of a UE without an ISIM (TS 23.003 §13.2, §13.3, §13.4B): the home network
// domain ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org, the IMPI <IMSI>@<domain> and the temporary IMPU sip:<IMPI>. mncLength
// is the length of the MNC in the IMSI, 2 or 3, which the USIM gives in EF_AD.
func IdentitiesFromIMSI(imsi string, mncLength int) (Identities, error) {
	if !isDigits(imsi) || len(imsi) < 6 || len(imsi) > 15 || (mncLength != 2 && mncLength != 3) {
		return Identities{}, fmt.Errorf("ue: IMSI %q with an MNC of %d digits", imsi, mncLength)
	}

	mcc, mnc := imsi[:3], imsi[3:3+mncLength]
	domain := "ims.mnc" + strings.Repeat("0", 3-len(mnc)) + mnc + ".mcc" + mcc + ".3gppnetwork.org"
	impi := imsi + "@" + domain

	return Identities{IMPI: impi, IMPU: "sip:" + impi, HomeDomain: domain}, nil
}

// IMEIInstanceID is the instance ID of a UE with an IMEI (TS 24.229 §5.1.1.2.1, TS 23.003 §13.8, RFC 7254): its TAC and
// SNR, and an SVN of 0.
func IMEIInstanceID(imei string) (string, error) {
	if !isDigits(imei) || (len(imei) != 14 && len(imei) != 15) {
		return "", fmt.Errorf("ue: IMEI %q is not 14 or 15 digits", imei)
	}

	return "urn:gsma:imei:" + imei[:8] + "-" + imei[8:14] + "-0", nil
}

var uuidState struct {
	sync.Mutex
	last  uint64
	clock uint16
	node  [6]byte
}

// timeUUID is a version 1 UUID with a random node (RFC 9562 §5.1): unique and increasing.
func timeUUID() string {
	s := &uuidState

	s.Lock()

	if s.last == 0 {
		var b [8]byte

		_, _ = rand.Read(b[:])
		s.clock = binary.BigEndian.Uint16(b[:2]) & 0x3fff
		copy(s.node[:], b[2:])
		s.node[0] |= 0x01
	}

	ts := uint64(time.Now().UnixNano()/100) + 0x01b21dd213814000
	if ts <= s.last {
		ts = s.last + 1
	}

	s.last = ts
	clock, node := s.clock, s.node

	s.Unlock()

	var u [16]byte

	binary.BigEndian.PutUint32(u[0:], uint32(ts))
	binary.BigEndian.PutUint16(u[4:], uint16(ts>>32))
	binary.BigEndian.PutUint16(u[6:], uint16(ts>>48)&0x0fff|0x1000)
	binary.BigEndian.PutUint16(u[8:], clock|0x8000)
	copy(u[10:], node[:])

	h := hex.EncodeToString(u[:])

	return strings.Join([]string{h[:8], h[8:12], h[12:16], h[16:20], h[20:]}, "-")
}
