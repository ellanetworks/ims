package ipsectest

import (
	"errors"
	"slices"
	"sync"

	"github.com/ellanetworks/ims/internal/ipsec"
)

var ErrExists = errors.New("ipsectest: security associations exist")

type Kernel struct {
	mu        sync.Mutex
	installed map[ipsec.Set]ipsec.Keys
	removed   []ipsec.Set
	failNext  error
}

func NewKernel() *Kernel {
	return &Kernel{installed: make(map[ipsec.Set]ipsec.Keys)}
}

func (k *Kernel) Install(s ipsec.Set, keys ipsec.Keys) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if err := k.failNext; err != nil {
		k.failNext = nil
		return err
	}

	if _, ok := k.installed[s]; ok {
		return ErrExists
	}

	k.installed[s] = keys

	return nil
}

func (k *Kernel) Remove(s ipsec.Set) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if _, ok := k.installed[s]; ok {
		delete(k.installed, s)
		k.removed = append(k.removed, s)
	}

	return nil
}

func (k *Kernel) Reconcile(keep []ipsec.Set) ([]ipsec.Set, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	var missing []ipsec.Set

	for _, s := range keep {
		if _, ok := k.installed[s]; !ok {
			missing = append(missing, s)
		}
	}

	for s := range k.installed {
		if !slices.Contains(keep, s) {
			delete(k.installed, s)
			k.removed = append(k.removed, s)
		}
	}

	return missing, nil
}

func (k *Kernel) FailNext(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.failNext = err
}

func (k *Kernel) Installed() map[ipsec.Set]ipsec.Keys {
	k.mu.Lock()
	defer k.mu.Unlock()

	out := make(map[ipsec.Set]ipsec.Keys, len(k.installed))
	for s, keys := range k.installed {
		out[s] = keys
	}

	return out
}

func (k *Kernel) Removed() []ipsec.Set {
	k.mu.Lock()
	defer k.mu.Unlock()

	return slices.Clone(k.removed)
}

func (k *Kernel) Lose(s ipsec.Set) {
	k.mu.Lock()
	defer k.mu.Unlock()

	delete(k.installed, s)
}
