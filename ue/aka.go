package ue

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

// Authenticator runs the AKA challenges of the network (TS 33.203 §6.1): the ISIM, or the USIM without one (§8), of
// the UICC; the USIM checks the network's AUTN and its SQN itself (TS 33.102 §6.3.3).
type Authenticator interface {
	// Authenticate runs the challenge RAND, AUTN: RES, CK and IK; or AUTS alone, on a synchronisation failure
	// (TS 33.102 §6.3.5); or ErrMACFailure when the AUTN is not the network's.
	Authenticate(rand, autn []byte) (AKAResult, error)
}

// AKAResult is the outcome of an AKA run (TS 31.102 §7.1.2.1, TS 31.103 §7.1.2.1).
type AKAResult struct {
	RES, CK, IK []byte

	AUTS []byte
}

// ErrMACFailure is the error of an Authenticator that found the AUTN of a challenge not to be the network's.
var ErrMACFailure = errors.New("ue: MAC failure")

const (
	algorithmAKAv1 = "AKAv1-MD5"
	qopAuth        = "auth"
	firstNC        = "00000001"

	randLen = 16
	autnLen = 16
)

var ErrNoChallenge = errors.New("ue: no AKAv1-MD5 challenge in the 401")

// challenge is the AKAv1-MD5 challenge of a 401 (RFC 3310 §3.2): its nonce is RAND ‖ AUTN.
type challenge struct {
	realm string
	nonce string
	qop   bool
	rand  []byte
	autn  []byte
}

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
		if err != nil || len(b) < randLen+autnLen {
			return challenge{}, fmt.Errorf("ue: nonce %q is not base64(RAND ‖ AUTN)", ch.nonce)
		}

		ch.rand, ch.autn = b[:randLen], b[randLen:randLen+autnLen]

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

// emptyAuthorization is the Authorization of an initial REGISTER (TS 24.229 §5.1.1.2.2).
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

// answer is the Authorization answering a challenge (RFC 3310 §3.3, §3.4; TS 24.229 §5.1.1.5.1): the digest with RES as
// the password, or with AUTS on a synchronisation failure, or an empty response on a MAC failure.
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
