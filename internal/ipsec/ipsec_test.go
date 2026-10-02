package ipsec

import (
	"bytes"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

func mechanisms(t *testing.T, values ...string) []sip.SecurityMechanism {
	t.Helper()

	var h sip.Header
	for _, v := range values {
		h.Add("Security-Client", v)
	}

	ms, err := h.SecurityMechanisms("Security-Client")
	if err != nil {
		t.Fatal(err)
	}

	return ms
}

func TestParseOffers(t *testing.T) {
	ue := Endpoint{PortC: 6301, PortS: 6300, SPIC: 25656, SPIS: 25657}

	for _, tc := range []struct {
		name string
		in   []string
		want []Offer
	}{
		{
			"Samsung",
			[]string{"ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300;alg=hmac-md5-96;ealg=null"},
			[]Offer{{ue, HMACMD596, EncryptionNull}},
		},
		{
			"several, with unsupported algorithms skipped",
			[]string{
				"ipsec-3gpp;alg=hmac-sha2-256-128;ealg=aes-gcm;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300",
				"ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300, " +
					"ipsec-3gpp;alg=hmac-md5-96;ealg=des-ede3-cbc;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300",
				"ipsec-3gpp;alg=hmac-md5-96;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300",
			},
			[]Offer{{ue, HMACSHA196, AESCBC}, {ue, HMACMD596, EncryptionNull}},
		},
		{
			"case, other mechanisms, unsupported protocol and mode",
			[]string{
				"digest;q=0.1, tls;q=0.2, sdes-srtp;mediasec",
				"ipsec-3gpp;prot=ah;alg=hmac-sha-1-96;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300",
				"ipsec-3gpp;mod=UDP-enc-tun;alg=hmac-sha-1-96;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300",
				"IPSEC-3GPP;PROT=ESP;MOD=TRANS;ALG=HMAC-SHA-1-96;EALG=NULL;SPI-C=25656;SPI-S=25657;PORT-C=6301;PORT-S=6300",
			},
			[]Offer{{ue, HMACSHA196, EncryptionNull}},
		},
		{
			"nothing usable",
			[]string{"ipsec-3gpp;alg=hmac-sha2-256-128;spi-c=1;spi-s=2;port-c=3;port-s=4"},
			nil,
		},
	} {
		got, err := ParseOffers(mechanisms(t, tc.in...))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}

		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: ParseOffers = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestParseOffersErrors(t *testing.T) {
	if _, err := ParseOffers(mechanisms(t, "digest, tls")); !errors.Is(err, ErrNoOffer) {
		t.Errorf("no ipsec-3gpp: %v, want ErrNoOffer", err)
	}

	if _, err := ParseOffers(nil); !errors.Is(err, ErrNoOffer) {
		t.Errorf("no Security-Client: %v, want ErrNoOffer", err)
	}
}

func TestParseOffersSkipsBadMechanisms(t *testing.T) {
	good := "ipsec-3gpp;alg=hmac-sha-1-96;spi-c=11;spi-s=12;port-c=6301;port-s=6300"
	other := "ipsec-3gpp;alg=hmac-md5-96;spi-c=21;spi-s=22;port-c=7301;port-s=7300"

	got, err := ParseOffers(mechanisms(t,
		"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=0;spi-s=2;port-c=6301;port-s=6300", good, other))
	if err != nil {
		t.Fatal(err)
	}

	want := []Offer{
		{Endpoint{PortC: 6301, PortS: 6300, SPIC: 11, SPIS: 12}, HMACSHA196, EncryptionNull},
		{Endpoint{PortC: 7301, PortS: 7300, SPIC: 21, SPIS: 22}, HMACMD596, EncryptionNull},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParseOffers = %+v, want %+v", got, want)
	}

	md5 := Policy{Integrity: []Integrity{HMACMD596}, Encryption: EncryptionOff}
	if o, err := md5.Select(got); err != nil || o != want[1] {
		t.Errorf("Select = %+v, %v; want %+v", o, err, want[1])
	}
}

func TestParseOffer(t *testing.T) {
	for _, tc := range []struct {
		in          string
		unsupported bool
	}{
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-s=2;port-c=6301;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=0;spi-s=2;port-c=6301;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=4294967296;spi-s=2;port-c=6301;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=-1;spi-s=2;port-c=6301;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=65536;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=x;port-s=6300", false},
		{"ipsec-3gpp;spi-c=1;spi-s=2;port-c=6301;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=7;spi-s=7;port-c=6301;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=6300;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=5060;port-s=6300", false},
		{"ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=6301;port-s=5061", false},
		{"ipsec-3gpp;alg=hmac-sha2-256-128;spi-c=1;spi-s=2;port-c=6301;port-s=6300", true},
		{"ipsec-3gpp;alg=hmac-sha-1-96;ealg=des-ede3-cbc;spi-c=1;spi-s=2;port-c=6301;port-s=6300", true},
		{"ipsec-3gpp;prot=ah;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=6301;port-s=6300", true},
		{"ipsec-3gpp;mod=tun;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c=6301;port-s=6300", true},
		{"tls;q=0.1", true},
	} {
		m, err := sip.ParseSecurityMechanism(tc.in)
		if err != nil {
			t.Fatal(err)
		}

		o, err := ParseOffer(m)
		if err == nil {
			t.Errorf("ParseOffer(%q) = %+v, want error", tc.in, o)
			continue
		}

		if errors.Is(err, ErrUnsupportedOffer) != tc.unsupported {
			t.Errorf("ParseOffer(%q) = %v, unsupported: want %v", tc.in, err, tc.unsupported)
		}
	}
}

func TestSelect(t *testing.T) {
	ue := Endpoint{PortC: 1, PortS: 2, SPIC: 3, SPIS: 4}
	offer := func(i Integrity, e Encryption) Offer { return Offer{ue, i, e} }

	all := []Offer{
		offer(HMACMD596, AESCBC), offer(HMACMD596, EncryptionNull),
		offer(HMACSHA196, AESCBC), offer(HMACSHA196, EncryptionNull),
	}

	for _, tc := range []struct {
		name   string
		policy EncryptionPolicy
		offers []Offer
		want   Offer
		err    error
	}{
		{"off", EncryptionOff, all, offer(HMACSHA196, EncryptionNull), nil},
		{"off, md5 only", EncryptionOff, []Offer{offer(HMACMD596, EncryptionNull)}, offer(HMACMD596, EncryptionNull), nil},
		{"off, null not offered", EncryptionOff, []Offer{offer(HMACSHA196, AESCBC)}, offer(HMACSHA196, AESCBC), nil},
		{
			"off, null before integrity order", EncryptionOff,
			[]Offer{offer(HMACSHA196, AESCBC), offer(HMACMD596, EncryptionNull)},
			offer(HMACMD596, EncryptionNull), nil,
		},
		{"preferred", EncryptionPreferred, all, offer(HMACSHA196, AESCBC), nil},
		{
			"preferred, encryption before integrity order", EncryptionPreferred,
			[]Offer{offer(HMACSHA196, EncryptionNull), offer(HMACMD596, AESCBC)},
			offer(HMACMD596, AESCBC), nil,
		},
		{"preferred, no encryption", EncryptionPreferred, []Offer{offer(HMACMD596, EncryptionNull)}, offer(HMACMD596, EncryptionNull), nil},
		{"required", EncryptionRequired, all, offer(HMACSHA196, AESCBC), nil},
		{"required, no encryption", EncryptionRequired, []Offer{offer(HMACSHA196, EncryptionNull)}, Offer{}, ErrNoAlgorithm},
		{"nothing offered", EncryptionOff, nil, Offer{}, ErrNoAlgorithm},
	} {
		p := DefaultPolicy()
		p.Encryption = tc.policy

		got, err := p.Select(tc.offers)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: Select = %+v, %v; want %+v, %v", tc.name, got, err, tc.want, tc.err)
		}
	}

	sha1Only := Policy{Integrity: []Integrity{HMACSHA196}, Encryption: EncryptionOff}
	if _, err := sha1Only.Select([]Offer{offer(HMACMD596, EncryptionNull)}); !errors.Is(err, ErrNoAlgorithm) {
		t.Errorf("integrity not on our list: %v, want ErrNoAlgorithm", err)
	}
}

func TestPolicyValidate(t *testing.T) {
	if err := DefaultPolicy().Validate(); err != nil {
		t.Errorf("DefaultPolicy: %v", err)
	}

	for _, p := range []Policy{
		{Encryption: EncryptionOff},
		{Integrity: []Integrity{"hmac-sha2-256-128"}, Encryption: EncryptionOff},
		{Integrity: []Integrity{HMACSHA196}, Encryption: "sometimes"},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate(%+v): no error", p)
		}
	}
}

func TestServer(t *testing.T) {
	s := Set{
		Local:      Endpoint{Addr: netip.MustParseAddr("10.0.0.5"), PortC: 5064, PortS: 5063, SPIC: 70000, SPIS: 70001},
		Remote:     Endpoint{Addr: netip.MustParseAddr("10.45.0.7"), PortC: 6301, PortS: 6300, SPIC: 25656, SPIS: 25657},
		Integrity:  HMACSHA196,
		Encryption: EncryptionNull,
	}

	want := "ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=70000;spi-s=70001;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null"
	if got := s.Server().String(); got != want {
		t.Errorf("Server() = %q, want %q", got, want)
	}

	v, err := sip.ParseSecurityMechanism("ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=70000;spi-s=70001;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null")
	if err != nil || !v.Equal(s.Server()) {
		t.Errorf("Security-Verify does not match: %v", err)
	}

	if r := s.Reverse(); r.Local != s.Remote || r.Remote != s.Local || r.Reverse() != s {
		t.Errorf("Reverse() = %+v", r)
	}
}

func TestKeys(t *testing.T) {
	a, err := sip.ParseAuth(`Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="bm9uY2U=", algorithm=AKAv1-MD5, ` +
		`ck="000102030405060708090a0b0c0d0e0f", ik="101112131415161718191A1B1C1D1E1F"`)
	if err != nil {
		t.Fatal(err)
	}

	k, err := KeysFromChallenge(a)
	if err != nil {
		t.Fatal(err)
	}

	ck := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	ik := []byte{16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}

	if !bytes.Equal(k.CK, ck) || !bytes.Equal(k.IK, ik) {
		t.Fatalf("KeysFromChallenge = %x %x", k.CK, k.IK)
	}

	for _, tc := range []struct {
		i    Integrity
		want []byte
	}{
		{HMACSHA196, append(slices.Clone(ik), 0, 0, 0, 0)},
		{HMACMD596, ik},
	} {
		if got, err := k.integrityKey(tc.i); err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("integrityKey(%s) = %x, %v; want %x", tc.i, got, err, tc.want)
		}
	}

	if got, err := k.encryptionKey(AESCBC); err != nil || !bytes.Equal(got, ck) {
		t.Errorf("encryptionKey(aes-cbc) = %x, %v; want CK", got, err)
	}

	if got, err := k.encryptionKey(EncryptionNull); err != nil || got != nil {
		t.Errorf("encryptionKey(null) = %x, %v", got, err)
	}

	for _, in := range []string{
		`Digest nonce="n", ik="101112131415161718191a1b1c1d1e1f"`,
		`Digest ck="0001", ik="101112131415161718191a1b1c1d1e1f"`,
		`Digest ck="zz0102030405060708090a0b0c0d0e0f", ik="101112131415161718191a1b1c1d1e1f"`,
		`Digest ck="000102030405060708090a0b0c0d0e0f"`,
	} {
		a, err := sip.ParseAuth(in)
		if err != nil {
			t.Fatal(err)
		}

		if k, err := KeysFromChallenge(a); err == nil {
			t.Errorf("KeysFromChallenge(%q) = %x %x, want error", in, k.CK, k.IK)
		}
	}
}

func TestSPIs(t *testing.T) {
	a := NewSPIs()
	seen := make(map[uint32]bool)

	for range 1000 {
		c, s, err := a.Allocate(MinSPI, 1<<32-1)
		if err != nil {
			t.Fatal(err)
		}

		for _, spi := range []uint32{c, s} {
			if spi <= MinSPI || spi > MaxSPI || seen[spi] {
				t.Fatalf("Allocate returned %d", spi)
			}

			seen[spi] = true
		}
	}

	b := NewSPIs()
	b.Reserve(1, 2)
	b.Release(1)

	if !b.used[2] || b.used[1] {
		t.Errorf("Reserve and Release: %v", b.used)
	}
}
