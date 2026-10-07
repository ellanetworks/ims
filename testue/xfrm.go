package testue

import "github.com/ellanetworks/ims/internal/ipsec"

type XFRM interface {
	Kernel
	Close() error
}

func OpenXFRM() (XFRM, error) {
	x, err := ipsec.Open()
	if err != nil {
		return nil, err
	}

	return x, nil
}
