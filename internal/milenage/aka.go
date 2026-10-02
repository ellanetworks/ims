package milenage

import (
	"crypto/subtle"
	"encoding/binary"
	"fmt"
)

type Vector struct {
	RAND []byte
	XRES []byte
	CK   []byte
	IK   []byte
	AUTN []byte
}

type Response struct {
	SQN uint64
	AMF []byte
	RES []byte
	CK  []byte
	IK  []byte
}

func sqnBytes(sqn uint64) []byte {
	var b [8]byte

	binary.BigEndian.PutUint64(b[:], sqn)

	return b[8-SQNLen:]
}

func sqnValue(b []byte) uint64 {
	var full [8]byte

	copy(full[8-SQNLen:], b)

	return binary.BigEndian.Uint64(full[:])
}

func checkSQN(sqn uint64) error {
	if sqn > MaxSQN {
		return ErrSQN
	}

	return nil
}

func GenerateVector(k, opc, rand []byte, sqn uint64, amf []byte) (Vector, error) {
	c, err := New(k, opc)
	if err != nil {
		return Vector{}, err
	}

	if err := checkSQN(sqn); err != nil {
		return Vector{}, err
	}

	s := sqnBytes(sqn)

	mac, err := c.F1(rand, s, amf)
	if err != nil {
		return Vector{}, err
	}

	res, ck, ik, ak, err := c.F2345(rand)
	if err != nil {
		return Vector{}, err
	}

	autn := make([]byte, 0, AUTNLen)
	autn = append(autn, s...)
	subtle.XORBytes(autn[:SQNLen], autn[:SQNLen], ak)
	autn = append(autn, amf...)
	autn = append(autn, mac...)

	return Vector{RAND: clone(rand), XRES: res, CK: ck, IK: ik, AUTN: autn}, nil
}

func Respond(k, opc, rand, autn []byte) (Response, error) {
	c, err := New(k, opc)
	if err != nil {
		return Response{}, err
	}

	if len(autn) != AUTNLen {
		return Response{}, fmt.Errorf("%w: AUTN must be 128 bits", ErrLength)
	}

	res, ck, ik, ak, err := c.F2345(rand)
	if err != nil {
		return Response{}, err
	}

	s := make([]byte, SQNLen)
	subtle.XORBytes(s, autn[:SQNLen], ak)
	amf := autn[SQNLen : SQNLen+AMFLen]

	xmac, err := c.F1(rand, s, amf)
	if err != nil {
		return Response{}, err
	}

	if subtle.ConstantTimeCompare(xmac, autn[SQNLen+AMFLen:]) != 1 {
		return Response{}, ErrMACFailure
	}

	return Response{SQN: sqnValue(s), AMF: clone(amf), RES: res, CK: ck, IK: ik}, nil
}

var resyncAMF = [AMFLen]byte{}

func AUTS(k, opc, rand []byte, sqnMS uint64) ([]byte, error) {
	c, err := New(k, opc)
	if err != nil {
		return nil, err
	}

	if err := checkSQN(sqnMS); err != nil {
		return nil, err
	}

	s := sqnBytes(sqnMS)

	macS, err := c.F1Star(rand, s, resyncAMF[:])
	if err != nil {
		return nil, err
	}

	ak, err := c.F5Star(rand)
	if err != nil {
		return nil, err
	}

	auts := make([]byte, 0, AUTSLen)
	auts = append(auts, s...)
	subtle.XORBytes(auts, auts, ak)

	return append(auts, macS...), nil
}

func Resync(k, opc, rand, auts []byte) (uint64, error) {
	c, err := New(k, opc)
	if err != nil {
		return 0, err
	}

	if len(auts) != AUTSLen {
		return 0, fmt.Errorf("%w: AUTS must be 112 bits", ErrLength)
	}

	ak, err := c.F5Star(rand)
	if err != nil {
		return 0, err
	}

	s := make([]byte, SQNLen)
	subtle.XORBytes(s, auts[:SQNLen], ak)

	xmac, err := c.F1Star(rand, s, resyncAMF[:])
	if err != nil {
		return 0, err
	}

	if subtle.ConstantTimeCompare(xmac, auts[SQNLen:]) != 1 {
		return 0, ErrMACFailure
	}

	return sqnValue(s), nil
}

func clone(b []byte) []byte {
	return append([]byte(nil), b...)
}
