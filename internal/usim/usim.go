// Package usim is a USIM in software, for test UEs: the AKA of TS 33.102 §6.3 with Milenage, the SQN of a challenge
// accepted only above the highest one accepted so far (§6.3.3; Annex C without the array of IND values).
package usim

import (
	"errors"
	"sync"

	"github.com/ellanetworks/ims/internal/milenage"
)

// ErrMACFailure is the error of a challenge whose AUTN is not the network's.
var ErrMACFailure = errors.New("usim: MAC failure")

type USIM struct {
	k, opc []byte

	mu  sync.Mutex
	sqn uint64
}

// New is a USIM of key K and OPc that accepted SQN last.
func New(k, opc []byte, sqn uint64) *USIM {
	return &USIM{k: k, opc: opc, sqn: sqn}
}

// Authenticate runs the challenge RAND, AUTN (TS 33.102 §6.3.3): RES, CK and IK; or AUTS alone, made from the USIM's
// SQN, on a synchronisation failure (§6.3.5); or ErrMACFailure.
func (u *USIM) Authenticate(rand, autn []byte) (res, ck, ik, auts []byte, err error) {
	r, err := milenage.Respond(u.k, u.opc, rand, autn)
	if errors.Is(err, milenage.ErrMACFailure) {
		return nil, nil, nil, nil, ErrMACFailure
	}

	if err != nil {
		return nil, nil, nil, nil, err
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	if r.SQN <= u.sqn {
		auts, err := milenage.AUTS(u.k, u.opc, rand, u.sqn)

		return nil, nil, nil, auts, err
	}

	u.sqn = r.SQN

	return r.RES, r.CK, r.IK, nil, nil
}

// SQN is the highest SQN the USIM accepted.
func (u *USIM) SQN() uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.sqn
}
