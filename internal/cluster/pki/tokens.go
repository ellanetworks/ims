package pki

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	tokenVersion    byte = 1
	tokenMaxPayload      = 16 << 10
	tokenTagSize         = sha256.Size
	MinTokenTTL          = 5 * time.Minute
	MaxTokenTTL          = 24 * time.Hour
)

var ErrInvalidToken = errors.New("invalid join token")

type Claims struct {
	ID        string   `json:"id"`
	ClusterID string   `json:"cid"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	Pins      []string `json:"pins"`
	Addresses []string `json:"addrs"`
}

func NewTokenID() string {
	var b [16]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

func MintToken(key []byte, c Claims) (string, error) {
	switch {
	case len(key) < 32:
		return "", errors.New("the join token key is too short")
	case c.ID == "" || c.ClusterID == "":
		return "", errors.New("a join token needs an ID and a cluster ID")
	case c.ExpiresAt <= c.IssuedAt:
		return "", errors.New("a join token must expire after it is issued")
	case len(c.Pins) == 0 || len(c.Addresses) == 0:
		return "", errors.New("a join token needs the members' pins and addresses")
	}

	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode the join token: %w", err)
	}

	if len(payload) > tokenMaxPayload {
		return "", errors.New("the join token is too large")
	}

	raw := append([]byte{tokenVersion}, payload...)

	return base64.RawURLEncoding.EncodeToString(append(raw, tag(key, raw)...)), nil
}

func tag(key, raw []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)

	return mac.Sum(nil)
}

func split(token string) (raw, sum []byte, c Claims, err error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) < 2+tokenTagSize || len(b) > 1+tokenMaxPayload+tokenTagSize {
		return nil, nil, Claims{}, ErrInvalidToken
	}

	if b[0] != tokenVersion {
		return nil, nil, Claims{}, fmt.Errorf("%w: version %d", ErrInvalidToken, b[0])
	}

	raw, sum = b[:len(b)-tokenTagSize], b[len(b)-tokenTagSize:]

	if err := json.Unmarshal(raw[1:], &c); err != nil {
		return nil, nil, Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	return raw, sum, c, nil
}

func ReadToken(token string) (Claims, error) {
	_, _, c, err := split(token)
	if err != nil {
		return Claims{}, err
	}

	if !ValidID(c.ClusterID) || len(c.Pins) == 0 || len(c.Addresses) == 0 {
		return Claims{}, fmt.Errorf("%w: missing claims", ErrInvalidToken)
	}

	return c, nil
}

func VerifyToken(key []byte, token string, now time.Time) (Claims, error) {
	raw, sum, c, err := split(token)
	if err != nil {
		return Claims{}, err
	}

	if !hmac.Equal(sum, tag(key, raw)) {
		return Claims{}, ErrInvalidToken
	}

	switch {
	case now.Add(-clockSkew).Unix() > c.ExpiresAt:
		return Claims{}, fmt.Errorf("%w: expired", ErrInvalidToken)
	case now.Add(clockSkew).Unix() < c.IssuedAt:
		return Claims{}, fmt.Errorf("%w: issued in the future", ErrInvalidToken)
	}

	return c, nil
}
