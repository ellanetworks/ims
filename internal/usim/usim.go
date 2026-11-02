package usim

import (
	"errors"
	"sync"

	"github.com/ellanetworks/ims/internal/milenage"
)

var ErrMACFailure = errors.New("usim: MAC failure")

type USIM struct {
	k, opc []byte

	mu  sync.Mutex
	sqn uint64
}

func New(k, opc []byte, sqn uint64) *USIM {
	return &USIM{k: k, opc: opc, sqn: sqn}
}

// TS 33.102 clause 6.3.3
// TS 33.102 clause 6.3.5
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

func (u *USIM) SQN() uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.sqn
}
