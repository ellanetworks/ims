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

// Platform is what the app gives a session: the network, the UICC, IPsec, the alarms and wake locks of the device,
// and where the session's events and log go. Its methods may be called from any goroutine, and must return promptly.
type Platform interface {
	// BindSocket binds socket fd, valid during the call only, to the MCPTT network: each SIP and media socket of the
	// session, before it is bound or connects.
	BindSocket(fd int) error

	// Authenticate runs an AKA challenge on the ISIM, the USIM without one (TS 31.103, TS 31.102 §7.1.2.1): the data of
	// the AUTHENTICATE response, DB… or DC…; the status word alone when the UICC answered with no data, 98 62 when it
	// found the MAC wrong; an error when the platform could not run the command at all.
	Authenticate(rand, autn []byte) ([]byte, error)

	// AllocateSPI reserves an SPI of SAs to the device's address local, for the session's Security-Client (TS 33.203
	// §7.2); ReleaseSPI releases it once its SAs are removed or never installed.
	AllocateSPI(local string) (int64, error)
	ReleaseSPI(spi int64)
	// InstallSA builds the four SAs of a set (TS 33.203 §7.1), of JSON saSetJSON with its keys; ProtectSocket applies
	// those of the set id to socket fd, valid during the call only, bound to the device's port port of the set: its
	// inbound SA, the one to that port, and its outbound one, from it; RemoveSA removes the SAs of the set id.
	InstallSA(set string) error
	ProtectSocket(id string, fd int, port int) error
	RemoveSA(id string) error

	// Wake has the device call Session.Woken with id in ms milliseconds, even asleep; CancelWake cancels it.
	// KeepAwake keeps the CPU awake for ms milliseconds, a wake lock with a timeout: the session's work after Woken.
	Wake(id int64, ms int64)
	CancelWake(id int64)
	KeepAwake(ms int64)

	// Event receives the events of the session, in JSON (Event of the package doc), one at a time, in order, on a
	// goroutine of their own: it may call the session.
	Event(event string)
	// Log receives a line of the session's log, of its level: -4 debug, 0 info, 4 warning, 8 error (log/slog).
	Log(level int, line string)
}

// saEndpointJSON and saSetJSON are the SA set of InstallSA, its keys those of the SAs (TS 33.203 Annex I): IK,
// extended for HMAC-SHA-1-96, and CK for AES-CBC, in hex.
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

// setID is the ID of an SA set: its local SPIs, unique among the device's sets.
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

// ipsec is the platform as the UE's IPsec: an IPsec of sockets (IpSecManager).
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

// uicc is the platform's ISIM as the UE's Authenticator.
type uicc struct{ p Platform }

var (
	errAKAResponse = errors.New("mobile: malformed AUTHENTICATE response")
	errAKAStatus   = errors.New("mobile: AUTHENTICATE failed")
)

// swMACFailure is the status word of an AUTHENTICATE whose MAC the UICC found wrong (TS 31.102 §7.1.2.1: "the USIM
// abandons the function and returns the status word '9862'").
var swMACFailure = []byte{0x98, 0x62}

// Authenticate parses the response of the AUTHENTICATE command (TS 31.102 §7.1.2.1, TS 31.103 §7.1.2.1): tag DB, then
// RES of 4 to 16 octets, CK and IK of 16, each with its length, Kc perhaps after; tag DC, then AUTS of 14. The status
// word 98 62 is a MAC failure; another, or the platform failing to run the command, an error of the UE, which does not
// answer the challenge.
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

// bindControl binds each socket of the session to the MCPTT network through the platform.
func bindControl(p Platform) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var err error

		if cerr := c.Control(func(fd uintptr) { err = p.BindSocket(int(fd)) }); cerr != nil {
			return cerr
		}

		return err
	}
}
