package testue

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

type identities struct {
	impi, impu, domain string
	mcc, mnc           string
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return s != ""
}

func deriveIdentities(c Config) (identities, error) {
	var id identities

	if c.IMSI != "" {
		mncLen := c.MNCLength
		if mncLen == 0 {
			mncLen = 2
		}

		if !isDigits(c.IMSI) || len(c.IMSI) < 6 || len(c.IMSI) > 15 || (mncLen != 2 && mncLen != 3) {
			return id, fmt.Errorf("testue: IMSI %q with an MNC of %d digits", c.IMSI, mncLen)
		}

		id.mcc, id.mnc = c.IMSI[:3], c.IMSI[3:3+mncLen]
		id.domain = "ims.mnc" + strings.Repeat("0", 3-len(id.mnc)) + id.mnc + ".mcc" + id.mcc + ".3gppnetwork.org"
		id.impi = c.IMSI + "@" + id.domain
		id.impu = "sip:" + id.impi
	}

	if c.HomeDomain != "" {
		id.domain = c.HomeDomain
	}

	if c.IMPI != "" {
		id.impi = c.IMPI
	}

	if c.IMPU != "" {
		id.impu = c.IMPU
	}

	if id.impi == "" || id.impu == "" || id.domain == "" {
		return id, errors.New("testue: no IMSI, and no IMPI, IMPU and home domain")
	}

	return id, nil
}

func instanceID(imei string) (string, error) {
	if !isDigits(imei) || (len(imei) != 14 && len(imei) != 15) {
		return "", fmt.Errorf("testue: IMEI %q is not 14 or 15 digits", imei)
	}

	return "urn:gsma:imei:" + imei[:8] + "-" + imei[8:14] + "-0", nil
}

func accessNetworkInfo(mcc, mnc string) string {
	return "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=" + mcc + mnc + "0001" + "0000001"
}

var uuidState struct {
	sync.Mutex
	last  uint64
	clock uint16
	node  [6]byte
}

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
