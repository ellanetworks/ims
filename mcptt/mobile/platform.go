package mobile

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"syscall"

	"github.com/ellanetworks/ims/ue"
)

type Platform interface {
	BindSocket(fd int) error

	Authenticate(rand, autn []byte) ([]byte, error)

	AllocateSPI(local string) (int64, error)
	ReleaseSPI(spi int64)
	InstallSA(set string) error
	ProtectSocket(id string, fd int, port int) error
	RemoveSA(id string) error

	Wake(id int64, ms int64)
	CancelWake(id int64)
	KeepAwake(ms int64)

	Event(event string)
	Log(level int, line string)
}

// TS 33.203 annex I
type saEndpointJSON struct {
	Addr  string `json:"addr"`
	PortC uint16 `json:"portC"`
	PortS uint16 `json:"portS"`
	SPIC  uint32 `json:"spiC"`
	SPIS  uint32 `json:"spiS"`
}

type saSetJSON struct {
	ID         string         `json:"id"`
	Local      saEndpointJSON `json:"local"`
	Remote     saEndpointJSON `json:"remote"`
	Integrity  string         `json:"integrity"`
	Encryption string         `json:"encryption"`
	IK         string         `json:"ik"`
	CK         string         `json:"ck"`
}

func setID(s ue.SASet) string {
	return strconv.FormatUint(uint64(s.Local.SPIC), 10) + "-" + strconv.FormatUint(uint64(s.Local.SPIS), 10)
}

func endpoint(e ue.Endpoint) saEndpointJSON {
	return saEndpointJSON{Addr: e.Addr.String(), PortC: e.PortC, PortS: e.PortS, SPIC: e.SPIC, SPIS: e.SPIS}
}

func setJSON(s ue.SASet, keys ue.Keys) (string, error) {
	ik, err := keys.IntegrityKey(s.Integrity)
	if err != nil {
		return "", err
	}

	ck, err := keys.EncryptionKey(s.Encryption)
	if err != nil {
		return "", err
	}

	b, err := json.Marshal(saSetJSON{
		ID: setID(s), Local: endpoint(s.Local), Remote: endpoint(s.Remote),
		Integrity: string(s.Integrity), Encryption: string(s.Encryption),
		IK: hex.EncodeToString(ik), CK: hex.EncodeToString(ck),
	})

	return string(b), err
}

type ipsec struct{ p Platform }

func (i ipsec) AllocateSPI(local netip.Addr) (uint32, error) {
	spi, err := i.p.AllocateSPI(local.String())
	if err != nil {
		return 0, err
	}

	if spi <= 0 || spi > 1<<32-1 {
		return 0, fmt.Errorf("mobile: SPI %d", spi)
	}

	return uint32(spi), nil
}

func (i ipsec) ReleaseSPI(spi uint32) { i.p.ReleaseSPI(int64(spi)) }

func (i ipsec) Install(s ue.SASet, keys ue.Keys) error {
	j, err := setJSON(s, keys)
	if err != nil {
		return err
	}

	return i.p.InstallSA(j)
}

func (i ipsec) Remove(s ue.SASet) error { return i.p.RemoveSA(setID(s)) }

func (i ipsec) Protect(s ue.SASet, port uint16, c syscall.RawConn) error {
	var err error

	if cerr := c.Control(func(fd uintptr) { err = i.p.ProtectSocket(setID(s), int(fd), int(port)) }); cerr != nil {
		return cerr
	}

	return err
}

type uicc struct{ p Platform }

var (
	errAKAResponse = errors.New("mobile: malformed AUTHENTICATE response")
	errAKAStatus   = errors.New("mobile: AUTHENTICATE failed")
)

// TS 31.102 clause 7.1.2.1
var swMACFailure = []byte{0x98, 0x62}

// TS 31.102 clause 7.1.2.1
// TS 31.103 clause 7.1.2.1
func (u uicc) Authenticate(rand, autn []byte) (ue.AKAResult, error) {
	b, err := u.p.Authenticate(rand, autn)
	if err != nil {
		return ue.AKAResult{}, fmt.Errorf("mobile: AUTHENTICATE: %w", err)
	}

	if bytes.Equal(b, swMACFailure) {
		return ue.AKAResult{}, ue.ErrMACFailure
	}

	if len(b) == 2 {
		return ue.AKAResult{}, fmt.Errorf("%w: status word % x", errAKAStatus, b)
	}

	fields := func(b []byte, n int) ([][]byte, bool) {
		var out [][]byte

		for len(out) < n {
			if len(b) == 0 || len(b) < 1+int(b[0]) {
				return nil, false
			}

			out, b = append(out, b[1:1+int(b[0])]), b[1+int(b[0]):]
		}

		return out, true
	}

	switch {
	case len(b) > 1 && b[0] == 0xdb:
		f, ok := fields(b[1:], 3)
		if !ok || len(f[0]) < 4 || len(f[0]) > 16 || len(f[1]) != 16 || len(f[2]) != 16 {
			return ue.AKAResult{}, errAKAResponse
		}

		return ue.AKAResult{RES: f[0], CK: f[1], IK: f[2]}, nil
	case len(b) > 1 && b[0] == 0xdc:
		f, ok := fields(b[1:], 1)
		if !ok || len(f[0]) != 14 {
			return ue.AKAResult{}, errAKAResponse
		}

		return ue.AKAResult{AUTS: f[0]}, nil
	}

	return ue.AKAResult{}, errAKAResponse
}

func bindControl(p Platform) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var err error

		if cerr := c.Control(func(fd uintptr) { err = p.BindSocket(int(fd)) }); cerr != nil {
			return cerr
		}

		return err
	}
}
