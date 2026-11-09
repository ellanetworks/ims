package pki

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestCertificateIdentity(t *testing.T) {
	cluster, node := NewID(), NewID()

	cert, err := GenerateCert(cluster, node, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	gotCluster, gotNode, err := Identity(cert.Leaf)
	if err != nil || gotCluster != cluster || gotNode != node {
		t.Fatalf("identity = %s, %s, %v", gotCluster, gotNode, err)
	}

	certPEM, keyPEM, err := EncodeCert(cert)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeCert(certPEM, keyPEM)
	if err != nil || Fingerprint(decoded.Leaf) != Fingerprint(cert.Leaf) {
		t.Fatalf("decoded = %v, %v", decoded.Leaf, err)
	}

	if _, err := GenerateCert("not-a-uuid", node, time.Now()); err == nil {
		t.Fatal("a certificate for a malformed cluster ID")
	}
}

func TestJoinToken(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	now := time.Unix(1_800_000_000, 0)
	claims := Claims{
		ID: NewTokenID(), ClusterID: NewID(), IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
		Pins: []string{"sha256:00"}, Addresses: []string{"10.0.0.1:7000"},
	}

	token, err := MintToken(key, claims)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := ReadToken(token); err != nil || got.ID != claims.ID || got.Addresses[0] != "10.0.0.1:7000" {
		t.Fatalf("read = %+v, %v", got, err)
	}

	if got, err := VerifyToken(key, token, now.Add(time.Minute)); err != nil || got.ClusterID != claims.ClusterID {
		t.Fatalf("verify = %+v, %v", got, err)
	}

	tampered := []byte(token)
	tampered[10] ^= 1

	cases := map[string]struct {
		key   []byte
		token string
		at    time.Time
	}{
		"another key": {bytes.Repeat([]byte{8}, 32), token, now},
		"tampered":    {key, string(tampered), now},
		"expired":     {key, token, now.Add(2 * time.Hour)},
		"future":      {key, token, now.Add(-time.Hour)},
		"garbage":     {key, "not a token", now},
	}

	for name, c := range cases {
		if _, err := VerifyToken(c.key, c.token, c.at); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v, want %v", name, err, ErrInvalidToken)
		}
	}
}
