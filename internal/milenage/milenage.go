package milenage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"errors"
	"fmt"
)

const (
	KeyLen   = 16
	RANDLen  = 16
	SQNLen   = 6
	AMFLen   = 2
	MACLen   = 8
	RESLen   = 8
	CKLen    = 16
	IKLen    = 16
	AKLen    = 6
	AUTNLen  = SQNLen + AMFLen + MACLen
	AUTSLen  = SQNLen + MACLen
	MaxSQN   = 1<<48 - 1
	blockLen = 16
)

var (
	ErrLength     = errors.New("milenage: wrong length")
	ErrSQN        = errors.New("milenage: SQN above 48 bits")
	ErrMACFailure = errors.New("milenage: MAC failure")
)

type Cipher struct {
	block cipher.Block
	opc   [blockLen]byte
}

func New(k, opc []byte) (*Cipher, error) {
	if len(k) != KeyLen || len(opc) != blockLen {
		return nil, fmt.Errorf("%w: K and OPc must be 128 bits", ErrLength)
	}

	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}

	c := &Cipher{block: b}
	copy(c.opc[:], opc)

	return c, nil
}

func OPc(k, op []byte) ([]byte, error) {
	if len(k) != KeyLen || len(op) != blockLen {
		return nil, fmt.Errorf("%w: K and OP must be 128 bits", ErrLength)
	}

	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}

	out := make([]byte, blockLen)
	b.Encrypt(out, op)
	subtle.XORBytes(out, out, op)

	return out, nil
}

func (c *Cipher) temp(rand []byte) [blockLen]byte {
	var t [blockLen]byte

	subtle.XORBytes(t[:], rand, c.opc[:])
	c.block.Encrypt(t[:], t[:])

	return t
}

func (c *Cipher) out(in [blockLen]byte, r int, cn byte) [blockLen]byte {
	var x [blockLen]byte

	for i := range x {
		x[i] = in[(i+r/8)%blockLen]
	}

	x[blockLen-1] ^= cn

	c.block.Encrypt(x[:], x[:])
	subtle.XORBytes(x[:], x[:], c.opc[:])

	return x
}

func (c *Cipher) out1(rand, sqn, amf []byte) [blockLen]byte {
	var in1 [blockLen]byte

	copy(in1[0:], sqn)
	copy(in1[6:], amf)
	copy(in1[8:], sqn)
	copy(in1[14:], amf)

	subtle.XORBytes(in1[:], in1[:], c.opc[:])

	t := c.temp(rand)

	var x [blockLen]byte

	for i := range x {
		x[i] = t[i] ^ in1[(i+8)%blockLen]
	}

	return c.out(x, 0, 0)
}

func (c *Cipher) outN(rand []byte, r int, cn byte) [blockLen]byte {
	t := c.temp(rand)
	subtle.XORBytes(t[:], t[:], c.opc[:])

	return c.out(t, r, cn)
}

func checkF1(rand, sqn, amf []byte) error {
	if len(rand) != RANDLen || len(sqn) != SQNLen || len(amf) != AMFLen {
		return fmt.Errorf("%w: RAND, SQN and AMF must be 128, 48 and 16 bits", ErrLength)
	}

	return nil
}

func checkRAND(rand []byte) error {
	if len(rand) != RANDLen {
		return fmt.Errorf("%w: RAND must be 128 bits", ErrLength)
	}

	return nil
}

func (c *Cipher) F1(rand, sqn, amf []byte) ([]byte, error) {
	if err := checkF1(rand, sqn, amf); err != nil {
		return nil, err
	}

	o := c.out1(rand, sqn, amf)

	return o[:MACLen:MACLen], nil
}

func (c *Cipher) F1Star(rand, sqn, amf []byte) ([]byte, error) {
	if err := checkF1(rand, sqn, amf); err != nil {
		return nil, err
	}

	o := c.out1(rand, sqn, amf)

	return o[MACLen:], nil
}

func (c *Cipher) F2345(rand []byte) (res, ck, ik, ak []byte, err error) {
	if err := checkRAND(rand); err != nil {
		return nil, nil, nil, nil, err
	}

	o2 := c.outN(rand, 0, 1)
	o3 := c.outN(rand, 32, 2)
	o4 := c.outN(rand, 64, 4)

	return o2[8:], o3[:], o4[:], o2[:AKLen:AKLen], nil
}

func (c *Cipher) F5Star(rand []byte) ([]byte, error) {
	if err := checkRAND(rand); err != nil {
		return nil, err
	}

	o5 := c.outN(rand, 96, 8)

	return o5[:AKLen:AKLen], nil
}
