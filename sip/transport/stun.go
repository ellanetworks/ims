package transport

import (
	"encoding/binary"
	"net/netip"
)

const (
	stunHeaderSize       = 20
	stunMagicCookie      = 0x2112A442
	stunBindingRequest   = 0x0001
	stunBindingResponse  = 0x0101
	stunXORMappedAddress = 0x0020
)

func isSTUN(data []byte) bool {
	return len(data) > 0 && data[0] <= 1
}

func stunBindingReply(req []byte, src netip.AddrPort) []byte {
	if len(req) < stunHeaderSize || len(req)%4 != 0 ||
		binary.BigEndian.Uint16(req[0:2]) != stunBindingRequest ||
		int(binary.BigEndian.Uint16(req[2:4])) != len(req)-stunHeaderSize ||
		binary.BigEndian.Uint32(req[4:8]) != stunMagicCookie {
		return nil
	}

	addr := src.Addr().Unmap()

	family, size := byte(0x01), 4
	if addr.Is6() {
		family, size = 0x02, 16
	}

	res := make([]byte, stunHeaderSize+4+4+size)
	binary.BigEndian.PutUint16(res[0:2], stunBindingResponse)
	binary.BigEndian.PutUint16(res[2:4], uint16(4+4+size))
	copy(res[4:20], req[4:20])

	attr := res[stunHeaderSize:]
	binary.BigEndian.PutUint16(attr[0:2], stunXORMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], uint16(4+size))
	attr[5] = family
	binary.BigEndian.PutUint16(attr[6:8], src.Port()^uint16(stunMagicCookie>>16))

	raw := addr.AsSlice()
	for i := range raw {
		attr[8+i] = raw[i] ^ req[4+i]
	}

	return res
}
