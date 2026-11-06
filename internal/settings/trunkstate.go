package settings

import (
	"errors"
	"time"
)

type TrunkState string

const (
	TrunkUnknown  TrunkState = "unknown"
	TrunkUp       TrunkState = "up"
	TrunkDown     TrunkState = "down"
	TrunkUnusable TrunkState = "unusable"
)

var ErrSharedServer = errors.New("TLS server of another SIP trunk")

type TrunkStatus struct {
	State TrunkState
	Since time.Time
}

type TrunkStates interface {
	TrunkStatus(id string) TrunkStatus
}

func (s TrunkStatus) Usable() bool {
	return s.State != TrunkDown && s.State != TrunkUnusable
}

func StatusOf(src TrunkStates, id string) TrunkStatus {
	if src == nil {
		return TrunkStatus{State: TrunkUnknown}
	}

	st := src.TrunkStatus(id)
	if st.State == "" {
		return TrunkStatus{State: TrunkUnknown}
	}

	return st
}
