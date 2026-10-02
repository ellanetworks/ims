//go:build !linux || !(amd64 || arm64)

package ipsec

import (
	"errors"
	"net/netip"
)

type XFRM struct{}

func Open() (*XFRM, error) {
	return nil, errors.ErrUnsupported
}

func (x *XFRM) Close() error {
	return nil
}

func (x *XFRM) Install(Set, Keys) error {
	return errors.ErrUnsupported
}

func (x *XFRM) Remove(Set) error {
	return errors.ErrUnsupported
}

func (x *XFRM) Reconcile([]Set) ([]Set, error) {
	return nil, errors.ErrUnsupported
}

func (x *XFRM) Probe(netip.Addr) error {
	return errors.ErrUnsupported
}
