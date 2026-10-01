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
	algorithmAKAv1 = "AKAv1-MD5"
	qopAuth        = "auth"
)

// credentials are the parameters of an Authorization: Digest header.
type credentials struct {
	username           string
	realm              string
	nonce              string
	uri                string
	response           string
	algorithm          string
	qop                string
	nc                 string
	cnonce             string
	auts               string
	integrityProtected string
}

// protected reports whether the P-CSCF received the REGISTER over the SAs
// (TS 24.229 §5.2.2.1).
func (c *credentials) protected() bool {
	return c != nil && c.integrityProtected == "yes"
}

// parseAuthorization returns the Digest credentials for the home realm. It
// returns nil when the request has none.
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

// parseCredentials parses one Authorization value. It returns nil for a
// scheme other than Digest.
func parseCredentials(v string) (*credentials, error) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
	if !strings.EqualFold(scheme, "Digest") {
		return nil, nil
	}

	c := &credentials{}

	for _, p := range sip.SplitList(rest) {
		name, value, ok := strings.Cut(p, "=")
		if !ok {
			return nil, errors.New("authorization parameter without a value")
		}

		value = sip.Unquote(strings.TrimSpace(value))

		switch strings.ToLower(strings.TrimSpace(name)) {
		case "username":
			c.username = value
		case "realm":
			c.realm = value
		case "nonce":
			c.nonce = value
		case "uri":
			c.uri = value
		case "response":
			c.response = value
		case "algorithm":
			c.algorithm = value
		case "qop":
			c.qop = value
		case "nc":
			c.nc = value
		case "cnonce":
			c.cnonce = value
		case "auts":
			c.auts = value
		case "integrity-protected":
			c.integrityProtected = value
		}
	}

	if c.username == "" {
		return nil, errors.New("authorization without a username")
	}

	return c, nil
}

// wwwAuthenticate is the AKA challenge (TS 24.229 §5.4.1.2.1). ck and ik are
// for the P-CSCF, which removes them.
func wwwAuthenticate(realm, nonce string, v authVector) string {
	return "Digest realm=" + sip.Quote(realm) +
		", nonce=" + sip.Quote(nonce) +
		", algorithm=" + algorithmAKAv1 +
		", qop=" + sip.Quote(qopAuth) +
		`, ck="` + hex.EncodeToString(v.ck) + `"` +
		`, ik="` + hex.EncodeToString(v.ik) + `"`
}

func akaNonce(v authVector) string {
	return base64.StdEncoding.EncodeToString(append(append([]byte(nil), v.rand...), v.autn...))
}

// verify checks the digest response, with XRES as the password (RFC 3310
// §3.4) and qop=auth (RFC 2617 §3.2.2).
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
