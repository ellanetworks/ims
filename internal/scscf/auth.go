package scscf

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

const (
	algorithmAKAv1        = "AKAv1-MD5"
	qopAuth               = "auth"
	integrityProtectedYes = "yes"
)

type credentials struct {
	username  string
	realm     string
	nonce     string
	uri       string
	response  string
	algorithm string
	qop       string
	nc        string
	cnonce    string
	auts      string

	integrityProtected string
}

func (c *credentials) answers(ch *challenge) bool {
	return c != nil && ch != nil && c.nonce != "" && c.nonce == ch.nonce
}

func parseAuthorization(h sip.Header, realm string) (*credentials, error) {
	var first *credentials

	for _, v := range h.Values("Authorization") {
		c, err := parseCredentials(v)
		if err != nil {
			return nil, err
		}

		if c == nil {
			continue
		}

		if strings.EqualFold(c.realm, realm) {
			return c, nil
		}

		if first == nil {
			first = c
		}
	}

	return first, nil
}

func parseCredentials(v string) (*credentials, error) {
	a, err := sip.ParseAuth(v)
	if err != nil {
		return nil, err
	}

	if !strings.EqualFold(a.Scheme, "Digest") {
		return nil, nil
	}

	get := func(name string) string {
		v, _ := a.Params.Get(name)
		return sip.Unquote(v)
	}

	c := &credentials{
		username:  get("username"),
		realm:     get("realm"),
		nonce:     get("nonce"),
		uri:       get("uri"),
		response:  get("response"),
		algorithm: get("algorithm"),
		qop:       get("qop"),
		nc:        get("nc"),
		cnonce:    get("cnonce"),
		auts:      get("auts"),

		integrityProtected: get("integrity-protected"),
	}

	if c.username == "" {
		return nil, errors.New("authorization without a username")
	}

	return c, nil
}

func wwwAuthenticate(realm, nonce string, v authVector) string {
	return sip.Auth{Scheme: "Digest", Params: sip.Params{
		{Name: "realm", Value: sip.Quote(realm)},
		{Name: "nonce", Value: sip.Quote(nonce)},
		{Name: "algorithm", Value: algorithmAKAv1},
		{Name: "qop", Value: sip.Quote(qopAuth)},
		{Name: "ck", Value: sip.Quote(hex.EncodeToString(v.ck))},
		{Name: "ik", Value: sip.Quote(hex.EncodeToString(v.ik))},
	}}.String()
}

func akaNonce(v authVector) string {
	return base64.StdEncoding.EncodeToString(append(append([]byte(nil), v.rand...), v.autn...))
}

func verify(c *credentials, method, nonce string, xres []byte) bool {
	if c.qop != qopAuth || c.nc == "" || c.cnonce == "" || c.response == "" {
		return false
	}

	ha1 := md5Hex([]byte(c.username+":"+c.realm+":"), xres)
	ha2 := md5Hex([]byte(method + ":" + c.uri))
	want := md5Hex([]byte(ha1 + ":" + nonce + ":" + c.nc + ":" + c.cnonce + ":" + c.qop + ":" + ha2))

	return subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(c.response))) == 1
}

func md5Hex(parts ...[]byte) string {
	h := md5.New()
	for _, p := range parts {
		h.Write(p)
	}

	return hex.EncodeToString(h.Sum(nil))
}
