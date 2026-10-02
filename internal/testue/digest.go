package testue

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ellanetworks/ims/internal/milenage"
	"github.com/ellanetworks/ims/sip"
)

const (
	algorithmAKAv1 = "AKAv1-MD5"
	qopAuth        = "auth"
	firstNC        = "00000001"
)

var ErrNoChallenge = errors.New("testue: no AKAv1-MD5 challenge in the 401")

type challenge struct {
	realm string
	nonce string
	qop   bool
	rand  []byte
	autn  []byte
}

// parseChallenge takes the first WWW-Authenticate with algorithm=AKAv1-MD5,
// whose nonce is base64(RAND ‖ AUTN ‖ server data) (RFC 3310 §3.2).
func parseChallenge(res *sip.Response) (challenge, error) {
	for _, v := range res.Header.Values("WWW-Authenticate") {
		a, err := sip.ParseAuth(v)
		if err != nil || !strings.EqualFold(a.Scheme, "Digest") {
			continue
		}

		get := func(name string) string {
			v, _ := a.Params.Get(name)
			return sip.Unquote(v)
		}

		if !strings.EqualFold(get("algorithm"), algorithmAKAv1) {
			continue
		}

		ch := challenge{realm: get("realm"), nonce: get("nonce")}

		for _, q := range strings.Split(get("qop"), ",") {
			ch.qop = ch.qop || strings.EqualFold(strings.TrimSpace(q), qopAuth)
		}

		b, err := base64.StdEncoding.DecodeString(ch.nonce)
		if err != nil || len(b) < milenage.RANDLen+milenage.AUTNLen {
			return challenge{}, fmt.Errorf("testue: nonce %q is not base64(RAND ‖ AUTN)", ch.nonce)
		}

		ch.rand, ch.autn = b[:milenage.RANDLen], b[milenage.RANDLen:milenage.RANDLen+milenage.AUTNLen]

		return ch, nil
	}

	return challenge{}, ErrNoChallenge
}

func md5Hex(parts ...[]byte) string {
	h := md5.New()
	for _, p := range parts {
		h.Write(p)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func cnonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// emptyAuthorization is the Authorization of a REGISTER without a challenge
// to answer (TS 24.229 §5.1.1.2.1).
func emptyAuthorization(impi, domain string) string {
	return sip.Auth{Scheme: "Digest", Params: sip.Params{
		{Name: "username", Value: sip.Quote(impi)},
		{Name: "realm", Value: sip.Quote(domain)},
		{Name: "uri", Value: sip.Quote("sip:" + domain)},
		{Name: "nonce", Value: ""},
		{Name: "response", Value: ""},
		{Name: "algorithm", Value: algorithmAKAv1},
	}}.String()
}

// answer is the Authorization answering ch with the password, the RES as raw
// octets (RFC 3310 §3.4), or an empty one for a resynchronisation. auts is
// added when it is set; an empty response with neither marks a network
// authentication failure (TS 24.229 §5.1.1.5.3).
func answer(impi, domain string, ch challenge, password []byte, auts []byte, emptyResponse bool) string {
	uri := "sip:" + domain
	ps := sip.Params{
		{Name: "username", Value: sip.Quote(impi)},
		{Name: "realm", Value: sip.Quote(ch.realm)},
		{Name: "uri", Value: sip.Quote(uri)},
		{Name: "nonce", Value: sip.Quote(ch.nonce)},
		{Name: "algorithm", Value: algorithmAKAv1},
	}

	response := ""

	if !emptyResponse {
		ha1 := md5Hex([]byte(impi+":"+ch.realm+":"), password)
		ha2 := md5Hex([]byte("REGISTER:" + uri))

		if ch.qop {
			cn := cnonce()
			response = md5Hex([]byte(ha1 + ":" + ch.nonce + ":" + firstNC + ":" + cn + ":" + qopAuth + ":" + ha2))
			ps = append(ps,
				sip.Param{Name: "qop", Value: qopAuth},
				sip.Param{Name: "nc", Value: firstNC},
				sip.Param{Name: "cnonce", Value: sip.Quote(cn)})
		} else {
			response = md5Hex([]byte(ha1 + ":" + ch.nonce + ":" + ha2))
		}
	}

	ps = append(ps, sip.Param{Name: "response", Value: quoteOrEmpty(response)})

	if auts != nil {
		ps = append(ps, sip.Param{Name: "auts", Value: sip.Quote(base64.StdEncoding.EncodeToString(auts))})
	}

	return sip.Auth{Scheme: "Digest", Params: ps}.String()
}

func quoteOrEmpty(s string) string {
	if s == "" {
		return ""
	}

	return sip.Quote(s)
}
