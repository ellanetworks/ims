package cluster

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
)

const (
	certFile        = "node.crt"
	keyFile         = "node.key"
	pendingCertFile = "join.crt"
	pendingKeyFile  = "join.key"
	pinsFile        = "pins.json"
)

type certs struct {
	dir       string
	nodeID    string
	cert      atomic.Pointer[tls.Certificate]
	pins      atomic.Pointer[map[string]string]
	bootstrap atomic.Pointer[map[string]string]
	removed   atomic.Pointer[map[string]bool]

	provisionalOnce sync.Once
	provisional     *tls.Certificate
}

func loadCerts(dir, nodeID string) (*certs, error) {
	c := &certs{dir: dir, nodeID: nodeID}

	certPEM, err := os.ReadFile(filepath.Join(dir, certFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read the node certificate: %w", err)
	default:
		keyPEM, err := os.ReadFile(filepath.Join(dir, keyFile))
		if err != nil {
			return nil, fmt.Errorf("read the node key: %w", err)
		}

		cert, err := pki.DecodeCert(certPEM, keyPEM)
		if err != nil {
			return nil, err
		}

		if _, id, err := pki.Identity(cert.Leaf); err != nil || id != nodeID {
			return nil, fmt.Errorf("the node certificate in %s is not this node's", dir)
		}

		c.cert.Store(&cert)
	}

	raw, err := os.ReadFile(filepath.Join(dir, pinsFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read the bootstrap pins: %w", err)
	default:
		var pins map[string]string
		if err := json.Unmarshal(raw, &pins); err != nil {
			return nil, fmt.Errorf("read the bootstrap pins: %w", err)
		}

		c.bootstrap.Store(&pins)
	}

	return c, nil
}

func (c *certs) presented() *tls.Certificate {
	if cert := c.cert.Load(); cert != nil {
		return cert
	}

	c.provisionalOnce.Do(func() {
		cert, err := pki.GenerateCert(pki.ProvisionalCluster, c.nodeID, time.Now())
		if err == nil {
			c.provisional = &cert
		}
	})

	return c.provisional
}

func (c *certs) fingerprint() string {
	if cert := c.cert.Load(); cert != nil {
		return pki.Fingerprint(cert.Leaf)
	}

	return ""
}

func (c *certs) clusterID() string {
	cert := c.cert.Load()
	if cert == nil {
		return ""
	}

	id, _, _ := pki.Identity(cert.Leaf)

	return id
}

func (c *certs) pin(fp string) (string, bool) {
	if fp == c.fingerprint() && fp != "" {
		return c.nodeID, true
	}

	if m := c.pins.Load(); m != nil {
		if id, ok := (*m)[fp]; ok {
			return id, true
		}
	}

	if m := c.bootstrap.Load(); m != nil {
		id, ok := (*m)[fp]
		return id, ok
	}

	return "", false
}

func (c *certs) fenced(fp string) bool {
	m := c.removed.Load()

	return m != nil && (*m)[fp]
}

func (c *certs) refresh(ctx context.Context, d *db.DB) error {
	members, err := d.Members(ctx)
	if err != nil {
		return err
	}

	tombstones, err := d.Tombstones(ctx)
	if err != nil {
		return err
	}

	removed := make(map[string]bool, len(tombstones))

	for _, t := range tombstones {
		if t.Fingerprint != "" {
			removed[t.Fingerprint] = true
		}
	}

	c.removed.Store(&removed)

	pins := make(map[string]string, len(members))

	for _, m := range members {
		if m.Fingerprint != "" {
			pins[m.Fingerprint] = m.NodeID
		}
	}

	c.pins.Store(&pins)

	if own := c.fingerprint(); own != "" && c.bootstrap.Load() != nil && pins[own] == c.nodeID {
		c.bootstrap.Store(nil)

		if err := os.Remove(filepath.Join(c.dir, pinsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the bootstrap pins: %w", err)
		}
	}

	return nil
}

func (c *certs) install(cert tls.Certificate, bootstrap map[string]string) error {
	certPEM, keyPEM, err := pki.EncodeCert(cert)
	if err != nil {
		return err
	}

	if bootstrap != nil {
		raw, err := json.Marshal(bootstrap)
		if err != nil {
			return err
		}

		if err := writeFile(filepath.Join(c.dir, pinsFile), raw); err != nil {
			return fmt.Errorf("write the bootstrap pins: %w", err)
		}

		c.bootstrap.Store(&bootstrap)
	}

	if err := writeFile(filepath.Join(c.dir, keyFile), keyPEM); err != nil {
		return fmt.Errorf("write the node key: %w", err)
	}

	if err := writeFile(filepath.Join(c.dir, certFile), certPEM); err != nil {
		return fmt.Errorf("write the node certificate: %w", err)
	}

	c.cert.Store(&cert)

	return nil
}

func (c *certs) member(address, version, label string, generated bool) db.Member {
	m := db.Member{
		NodeID: c.nodeID, Address: address, SchemaVersion: db.SchemaVersion(), Version: version, DiameterLabel: label,
		LabelGenerated: generated,
	}

	if cert := c.cert.Load(); cert != nil {
		m.Fingerprint = pki.Fingerprint(cert.Leaf)
	}

	return m
}

func (c *certs) pending(clusterID string, now time.Time) (tls.Certificate, error) {
	certPEM, certErr := os.ReadFile(filepath.Join(c.dir, pendingCertFile))
	keyPEM, keyErr := os.ReadFile(filepath.Join(c.dir, pendingKeyFile))

	if certErr == nil && keyErr == nil {
		cert, err := pki.DecodeCert(certPEM, keyPEM)
		if err == nil {
			if cid, id, err := pki.Identity(cert.Leaf); err == nil && cid == clusterID && id == c.nodeID {
				return cert, nil
			}
		}
	}

	cert, err := pki.GenerateCert(clusterID, c.nodeID, now)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM, keyPEM, err = pki.EncodeCert(cert)
	if err != nil {
		return tls.Certificate{}, err
	}

	if err := writeFile(filepath.Join(c.dir, pendingKeyFile), keyPEM); err != nil {
		return tls.Certificate{}, err
	}

	if err := writeFile(filepath.Join(c.dir, pendingCertFile), certPEM); err != nil {
		return tls.Certificate{}, err
	}

	return cert, nil
}

func (c *certs) dropPending() {
	_ = os.Remove(filepath.Join(c.dir, pendingCertFile))
	_ = os.Remove(filepath.Join(c.dir, pendingKeyFile))
}
