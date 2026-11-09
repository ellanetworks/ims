package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	trustDomain = "cluster.ims.ella"
	certTTL     = 10 * 365 * 24 * time.Hour
	clockSkew   = time.Minute
)

func NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}

func ValidID(id string) bool {
	u, err := uuid.Parse(id)

	return err == nil && u.String() == id
}

func spiffeID(clusterID, nodeID string) *url.URL {
	return &url.URL{Scheme: "spiffe", Host: trustDomain, Path: "/" + clusterID + "/node/" + nodeID}
}

func GenerateCert(clusterID, nodeID string, now time.Time) (tls.Certificate, error) {
	if !ValidID(clusterID) || !ValidID(nodeID) {
		return tls.Certificate{}, fmt.Errorf("cluster %q or node %q is not a canonical UUID", clusterID, nodeID)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate the node key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 159))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate a serial number: %w", err)
	}

	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("encode the node key: %w", err)
	}

	skid := sha256.Sum256(pub)
	start := now.Add(-clockSkew)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ims-node-" + nodeID},
		URIs:                  []*url.URL{spiffeID(clusterID, nodeID)},
		NotBefore:             start,
		NotAfter:              start.Add(certTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		SubjectKeyId:          skid[:20],
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create the node certificate: %w", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse the node certificate: %w", err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)

	return "sha256:" + hex.EncodeToString(sum[:])
}

func Identity(cert *x509.Certificate) (clusterID, nodeID string, err error) {
	if len(cert.URIs) != 1 {
		return "", "", fmt.Errorf("the certificate carries %d URIs, want 1", len(cert.URIs))
	}

	u := cert.URIs[0]
	if u.Scheme != "spiffe" || u.Host != trustDomain {
		return "", "", fmt.Errorf("URI %s is not in the %s trust domain", u, trustDomain)
	}

	cid, nid, ok := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/node/")
	if !ok || !ValidID(cid) || !ValidID(nid) {
		return "", "", fmt.Errorf("URI %s is not /<cluster>/node/<node>", u)
	}

	if cert.Subject.CommonName != "ims-node-"+nid {
		return "", "", fmt.Errorf("common name %q does not match node %s", cert.Subject.CommonName, nid)
	}

	return cid, nid, nil
}

func EncodeCert(cert tls.Certificate) (certPEM, keyPEM []byte, err error) {
	key, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("the node key is not a signer")
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encode the node key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func DecodeCert(certPEM, keyPEM []byte) (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("decode the node certificate: %w", err)
	}

	return cert, nil
}

func ParseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}

	return x509.ParseCertificate(block.Bytes)
}
