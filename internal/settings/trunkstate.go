package settings

import "time"

type TrunkState string

const (
	TrunkUnknown  TrunkState = "unknown"
	TrunkUp       TrunkState = "up"
	TrunkDown     TrunkState = "down"
	TrunkUnusable TrunkState = "unusable"
)

type TrunkStatus struct {
	State TrunkState
	Since time.Time
}

type TrunkStates interface {
	TrunkStatus(id string) TrunkStatus
}

func (s TrunkStatus) Usable() bool {
	return s.State != TrunkDown
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
