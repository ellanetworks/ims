//go:build linux && (amd64 || arm64)

package ipsec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const owner = 0x454c4c41

const (
	policyPriority = 1024
	replayWindow   = 64
	recvTimeout    = 5 * time.Second
)

type xfrmAddress [4]uint32

type xfrmSelector struct {
	daddr      xfrmAddress
	saddr      xfrmAddress
	dport      uint16
	dportMask  uint16
	sport      uint16
	sportMask  uint16
	family     uint16
	prefixlenD uint8
	prefixlenS uint8
	proto      uint8
	ifindex    int32
	user       uint32
}

type xfrmID struct {
	daddr xfrmAddress
	spi   uint32
	proto uint8
}

type xfrmLifetimeCfg struct {
	softByteLimit, hardByteLimit     uint64
	softPacketLimit, hardPacketLimit uint64
	_, _                             uint64
	_, _                             uint64
}

type xfrmLifetimeCur struct {
	_       uint64
	packets uint64
	_, _    uint64
}

type xfrmUsersaInfo struct {
	sel          xfrmSelector
	id           xfrmID
	saddr        xfrmAddress
	lft          xfrmLifetimeCfg
	curlft       xfrmLifetimeCur
	_            [3]uint32
	_            uint32
	reqid        uint32
	family       uint16
	mode         uint8
	replayWindow uint8
	_            uint8
}

type xfrmUsersaID struct {
	daddr  xfrmAddress
	spi    uint32
	family uint16
	proto  uint8
}

type xfrmUserpolicyInfo struct {
	sel      xfrmSelector
	lft      xfrmLifetimeCfg
	_        xfrmLifetimeCur
	priority uint32
	_        uint32
	dir      uint8
	_        uint8
	_        uint8
	_        uint8
}

type xfrmUserpolicyID struct {
	sel xfrmSelector
	_   uint32
	dir uint8
}

type xfrmUserTmpl struct {
	id     xfrmID
	family uint16
	saddr  xfrmAddress
	reqid  uint32
	mode   uint8
	_      uint8
	_      uint8
	aalgos uint32
	ealgos uint32
	calgos uint32
}

var (
	_ [unsafe.Sizeof(xfrmSelector{}) - 56]byte
	_ [56 - unsafe.Sizeof(xfrmSelector{})]byte
	_ [unsafe.Sizeof(xfrmUsersaInfo{}) - 224]byte
	_ [224 - unsafe.Sizeof(xfrmUsersaInfo{})]byte
	_ [unsafe.Sizeof(xfrmUsersaID{}) - 24]byte
	_ [24 - unsafe.Sizeof(xfrmUsersaID{})]byte
	_ [unsafe.Sizeof(xfrmUserpolicyInfo{}) - 168]byte
	_ [168 - unsafe.Sizeof(xfrmUserpolicyInfo{})]byte
	_ [unsafe.Sizeof(xfrmUserpolicyID{}) - 64]byte
	_ [64 - unsafe.Sizeof(xfrmUserpolicyID{})]byte
	_ [unsafe.Sizeof(xfrmUserTmpl{}) - 64]byte
	_ [64 - unsafe.Sizeof(xfrmUserTmpl{})]byte
)

const (
	xfrmMsgNewSA      = 0x10
	xfrmMsgDelSA      = 0x11
	xfrmMsgGetSA      = 0x12
	xfrmMsgNewPolicy  = 0x13
	xfrmMsgDelPolicy  = 0x14
	xfrmMsgGetPolicy  = 0x15
	xfrmaAlgCrypt     = 2
	xfrmaTmpl         = 5
	xfrmaAlgAuthTrunc = 20
	xfrmModeTransport = 0
	xfrmPolicyIn      = 0
	xfrmPolicyOut     = 1
	xfrmInf           = ^uint64(0)
	nlmsgerrAttrMsg   = 1
)

func bytesOf[T any](v *T) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(v)), unsafe.Sizeof(*v))
}

func decode[T any](b []byte) (T, bool) {
	var v T

	if len(b) < int(unsafe.Sizeof(v)) {
		return v, false
	}

	copy(bytesOf(&v), b)

	return v, true
}

func be16(v uint16) uint16 {
	var b [2]byte

	binary.BigEndian.PutUint16(b[:], v)

	return binary.NativeEndian.Uint16(b[:])
}

func be32(v uint32) uint32 {
	var b [4]byte

	binary.BigEndian.PutUint32(b[:], v)

	return binary.NativeEndian.Uint32(b[:])
}

func address(a netip.Addr) xfrmAddress {
	var x xfrmAddress

	b := (*[16]byte)(unsafe.Pointer(&x))

	if a.Is4() {
		v := a.As4()
		copy(b[:], v[:])
	} else {
		*b = a.As16()
	}

	return x
}

func family(a netip.Addr) uint16 {
	if a.Is4() {
		return unix.AF_INET
	}

	return unix.AF_INET6
}

func infinite() xfrmLifetimeCfg {
	return xfrmLifetimeCfg{
		softByteLimit: xfrmInf, hardByteLimit: xfrmInf,
		softPacketLimit: xfrmInf, hardPacketLimit: xfrmInf,
	}
}

type sa struct {
	src, dst     netip.Addr
	sport, dport uint16
	spi          uint32
	dir          uint8
}

func (s Set) sas() [4]sa {
	l, r := s.Local, s.Remote

	return [4]sa{
		{r.Addr, l.Addr, r.PortC, l.PortS, l.SPIS, xfrmPolicyIn},
		{r.Addr, l.Addr, r.PortS, l.PortC, l.SPIC, xfrmPolicyIn},
		{l.Addr, r.Addr, l.PortC, r.PortS, r.SPIS, xfrmPolicyOut},
		{l.Addr, r.Addr, l.PortS, r.PortC, r.SPIC, xfrmPolicyOut},
	}
}

func (a sa) selector() xfrmSelector {
	bits := uint8(128)
	if a.src.Is4() {
		bits = 32
	}

	return xfrmSelector{
		daddr: address(a.dst), saddr: address(a.src),
		dport: be16(a.dport), dportMask: 0xffff,
		sport: be16(a.sport), sportMask: 0xffff,
		family:     family(a.src),
		prefixlenD: bits, prefixlenS: bits,
		proto:   0,
		ifindex: 0,
		user:    owner,
	}
}

func (a sa) id() saKey {
	return saKey{family: family(a.dst), daddr: address(a.dst), spi: a.spi}
}

func (a sa) policy() policyKey {
	return policyKey{sel: a.selector(), dir: a.dir}
}

type saKey struct {
	family uint16
	daddr  xfrmAddress
	spi    uint32
}

type policyKey struct {
	sel xfrmSelector
	dir uint8
}

type NetlinkError struct {
	Errno   unix.Errno
	Message string
}

func (e *NetlinkError) Error() string {
	if e.Message == "" {
		return e.Errno.Error()
	}

	return e.Errno.Error() + ": " + e.Message
}

func (e *NetlinkError) Unwrap() error {
	return e.Errno
}

type XFRM struct {
	mu  sync.Mutex
	fd  int
	seq uint32
	buf []byte
}

func Open() (*XFRM, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_XFRM)
	if err != nil {
		return nil, fmt.Errorf("open XFRM netlink socket (is xfrm_user loaded?): %w", err)
	}

	tv := unix.NsecToTimeval(recvTimeout.Nanoseconds())

	_ = unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_EXT_ACK, 1)
	_ = unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_CAP_ACK, 1)

	for _, o := range []func() error{
		func() error { return unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv) },
		func() error { return unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}) },
	} {
		if err := o(); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("set up XFRM netlink socket: %w", err)
		}
	}

	return &XFRM{fd: fd, buf: make([]byte, 1<<16)}, nil
}

func (x *XFRM) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if x.fd < 0 {
		return nil
	}

	err := unix.Close(x.fd)
	x.fd = -1

	return err
}

func (x *XFRM) Install(s Set, k Keys) error {
	if err := s.validate(); err != nil {
		return fmt.Errorf("install %s: %w", s, err)
	}

	auth, crypt, err := k.algos(s)
	if err != nil {
		return fmt.Errorf("install %s: %w", s, err)
	}

	var (
		sas      []sa
		policies []sa
	)

	undo := func(err error) error {
		for _, a := range policies {
			_ = x.deletePolicy(a)
		}

		for _, a := range sas {
			_ = x.deleteSA(a)
		}

		return fmt.Errorf("install %s: %w", s, err)
	}

	for _, a := range s.sas() {
		if err := x.newSA(a, auth, crypt); err != nil {
			return undo(fmt.Errorf("SA %s:%d -> %s:%d spi %d: %w", a.src, a.sport, a.dst, a.dport, a.spi, err))
		}

		sas = append(sas, a)
	}

	for _, a := range s.sas() {
		if err := x.newPolicy(a); err != nil {
			return undo(fmt.Errorf("policy %s:%d -> %s:%d: %w", a.src, a.sport, a.dst, a.dport, err))
		}

		policies = append(policies, a)
	}

	return nil
}

func (x *XFRM) Remove(s Set) error {
	var errs []error

	for _, a := range s.sas() {
		if err := x.deletePolicy(a); err != nil && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, fmt.Errorf("policy %s:%d -> %s:%d: %w", a.src, a.sport, a.dst, a.dport, err))
		}
	}

	for _, a := range s.sas() {
		if err := x.deleteSA(a); err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, fmt.Errorf("SA %s spi %d: %w", a.dst, a.spi, err))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("remove %s: %w", s, err)
	}

	return nil
}

func (x *XFRM) Reconcile(keep []Set) ([]Set, error) {
	sas, err := x.dump(xfrmMsgGetSA)
	if err != nil {
		return nil, fmt.Errorf("list SAs: %w", err)
	}

	policies, err := x.dump(xfrmMsgGetPolicy)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}

	installedSAs := make(map[saKey]bool)

	for _, b := range sas {
		if v, ok := decode[xfrmUsersaInfo](b); ok && v.sel.user == owner {
			installedSAs[saKey{family: v.family, daddr: v.id.daddr, spi: be32(v.id.spi)}] = true
		}
	}

	installedPolicies := make(map[policyKey]bool)

	for _, b := range policies {
		if v, ok := decode[xfrmUserpolicyInfo](b); ok && v.sel.user == owner {
			installedPolicies[policyKey{sel: v.sel, dir: v.dir}] = true
		}
	}

	var missing []Set

	wantSAs := make(map[saKey]bool)
	wantPolicies := make(map[policyKey]bool)

	for _, s := range keep {
		complete := true

		for _, a := range s.sas() {
			wantSAs[a.id()] = true
			wantPolicies[a.policy()] = true
			complete = complete && installedSAs[a.id()] && installedPolicies[a.policy()]
		}

		if !complete {
			missing = append(missing, s)
		}
	}

	var errs []error

	for p := range installedPolicies {
		if !wantPolicies[p] {
			if err := x.request(xfrmMsgDelPolicy, 0, bytesOf(&xfrmUserpolicyID{sel: p.sel, dir: p.dir})); err != nil && !errors.Is(err, unix.ENOENT) {
				errs = append(errs, fmt.Errorf("delete stale policy: %w", err))
			}
		}
	}

	for k := range installedSAs {
		if !wantSAs[k] {
			id := xfrmUsersaID{daddr: k.daddr, spi: be32(k.spi), family: k.family, proto: unix.IPPROTO_ESP}
			if err := x.request(xfrmMsgDelSA, 0, bytesOf(&id)); err != nil && !errors.Is(err, unix.ESRCH) {
				errs = append(errs, fmt.Errorf("delete stale SA spi %d: %w", k.spi, err))
			}
		}
	}

	return missing, errors.Join(errs...)
}

func (x *XFRM) Probe(local netip.Addr) error {
	remote := netip.MustParseAddr("192.0.2.1")
	if !local.Is4() {
		remote = netip.MustParseAddr("2001:db8::1")
	}

	k := Keys{CK: make([]byte, 16), IK: make([]byte, 16)}

	for _, alg := range []struct {
		i Integrity
		e Encryption
	}{{HMACSHA196, AESCBC}, {HMACMD596, EncryptionNull}} {
		s := Set{
			Local:      Endpoint{Addr: local, PortC: 1, PortS: 2, SPIC: MinSPI - 4, SPIS: MinSPI - 3},
			Remote:     Endpoint{Addr: remote, PortC: 1, PortS: 2, SPIC: MinSPI - 2, SPIS: MinSPI - 1},
			Integrity:  alg.i,
			Encryption: alg.e,
		}

		_ = x.Remove(s)

		if err := x.Install(s, k); err != nil {
			switch {
			case errors.Is(err, unix.EPERM):
				return fmt.Errorf("IPsec needs CAP_NET_ADMIN: %w", err)
			case errors.Is(err, unix.EEXIST):
				return fmt.Errorf("IPsec probe SPIs held by another program: %w", err)
			}

			return fmt.Errorf("IPsec %s/%s on %s (are esp4, esp6 and authenc available?): %w", alg.i, alg.e, local, err)
		}

		if err := x.Remove(s); err != nil {
			return err
		}
	}

	return nil
}

func (x *XFRM) newSA(a sa, auth, crypt algo) error {
	info := xfrmUsersaInfo{
		sel:          a.selector(),
		id:           xfrmID{daddr: address(a.dst), spi: be32(a.spi), proto: unix.IPPROTO_ESP},
		saddr:        address(a.src),
		lft:          infinite(),
		reqid:        a.spi,
		family:       family(a.src),
		mode:         xfrmModeTransport,
		replayWindow: replayWindow,
	}

	return x.request(xfrmMsgNewSA, unix.NLM_F_CREATE|unix.NLM_F_EXCL, bytesOf(&info),
		attr(xfrmaAlgAuthTrunc, algoAuth(auth)),
		attr(xfrmaAlgCrypt, algoCrypt(crypt)))
}

func (x *XFRM) newPolicy(a sa) error {
	info := xfrmUserpolicyInfo{
		sel:      a.selector(),
		lft:      infinite(),
		priority: policyPriority,
		dir:      a.dir,
	}

	tmpl := xfrmUserTmpl{
		id:     xfrmID{daddr: address(a.dst), proto: unix.IPPROTO_ESP},
		family: family(a.src),
		saddr:  address(a.src),
		reqid:  a.spi,
		mode:   xfrmModeTransport,
		aalgos: ^uint32(0),
		ealgos: ^uint32(0),
		calgos: ^uint32(0),
	}

	return x.request(xfrmMsgNewPolicy, unix.NLM_F_CREATE|unix.NLM_F_EXCL, bytesOf(&info), attr(xfrmaTmpl, bytesOf(&tmpl)))
}

func (x *XFRM) deleteSA(a sa) error {
	id := xfrmUsersaID{daddr: address(a.dst), spi: be32(a.spi), family: family(a.dst), proto: unix.IPPROTO_ESP}

	b, err := x.get(xfrmMsgGetSA, bytesOf(&id))
	if err != nil {
		return err
	}

	if v, ok := decode[xfrmUsersaInfo](b); !ok || v.sel.user != owner {
		return unix.ESRCH
	}

	return x.request(xfrmMsgDelSA, 0, bytesOf(&id))
}

func (x *XFRM) deletePolicy(a sa) error {
	id := xfrmUserpolicyID{sel: a.selector(), dir: a.dir}

	return x.request(xfrmMsgDelPolicy, 0, bytesOf(&id))
}

func algoAuth(a algo) []byte {
	b := make([]byte, 72+len(a.key))
	copy(b, a.name)
	binary.NativeEndian.PutUint32(b[64:], uint32(len(a.key)*8))
	binary.NativeEndian.PutUint32(b[68:], uint32(a.trunc))
	copy(b[72:], a.key)

	return b
}

func algoCrypt(a algo) []byte {
	b := make([]byte, 68+len(a.key))
	copy(b, a.name)
	binary.NativeEndian.PutUint32(b[64:], uint32(len(a.key)*8))
	copy(b[68:], a.key)

	return b
}

func attr(typ uint16, data []byte) []byte {
	b := make([]byte, nlaAlign(unix.SizeofNlAttr+len(data)))
	binary.NativeEndian.PutUint16(b, uint16(unix.SizeofNlAttr+len(data)))
	binary.NativeEndian.PutUint16(b[2:], typ)
	copy(b[unix.SizeofNlAttr:], data)

	return b
}

func nlaAlign(n int) int {
	return (n + unix.NLA_ALIGNTO - 1) &^ (unix.NLA_ALIGNTO - 1)
}

func nlmAlign(n int) int {
	return (n + unix.NLMSG_ALIGNTO - 1) &^ (unix.NLMSG_ALIGNTO - 1)
}

func (x *XFRM) request(typ uint16, flags uint16, payload []byte, attrs ...[]byte) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	seq, err := x.send(typ, flags|unix.NLM_F_REQUEST|unix.NLM_F_ACK, payload, attrs...)
	if err != nil {
		return err
	}

	for {
		msgs, err := x.recv()
		if err != nil {
			return err
		}

		for _, m := range msgs {
			if m.Header.Seq != seq || m.Header.Type != unix.NLMSG_ERROR {
				continue
			}

			return ackError(m)
		}
	}
}

func (x *XFRM) get(typ uint16, payload []byte) ([]byte, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	seq, err := x.send(typ, unix.NLM_F_REQUEST, payload)
	if err != nil {
		return nil, err
	}

	for {
		msgs, err := x.recv()
		if err != nil {
			return nil, err
		}

		for _, m := range msgs {
			if m.Header.Seq != seq {
				continue
			}

			if m.Header.Type == unix.NLMSG_ERROR {
				if err := ackError(m); err != nil {
					return nil, err
				}

				return nil, errors.New("no reply")
			}

			return append([]byte(nil), m.Data...), nil
		}
	}
}

func (x *XFRM) dump(typ uint16) ([][]byte, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	seq, err := x.send(typ, unix.NLM_F_REQUEST|unix.NLM_F_DUMP, nil)
	if err != nil {
		return nil, err
	}

	var out [][]byte

	for {
		msgs, err := x.recv()
		if err != nil {
			return nil, err
		}

		for _, m := range msgs {
			if m.Header.Seq != seq {
				continue
			}

			switch m.Header.Type {
			case unix.NLMSG_DONE, unix.NLMSG_ERROR:
				if err := ackError(m); err != nil {
					return nil, err
				}

				return out, nil
			default:
				out = append(out, append([]byte(nil), m.Data...))
			}
		}
	}
}

func (x *XFRM) send(typ, flags uint16, payload []byte, attrs ...[]byte) (uint32, error) {
	if x.fd < 0 {
		return 0, errors.New("XFRM closed")
	}

	x.seq++

	n := unix.NLMSG_HDRLEN + nlmAlign(len(payload))
	for _, a := range attrs {
		n += len(a)
	}

	b := make([]byte, n)
	h := unix.NlMsghdr{Len: uint32(n), Type: typ, Flags: flags, Seq: x.seq}
	copy(b, bytesOf(&h))
	copy(b[unix.NLMSG_HDRLEN:], payload)

	off := unix.NLMSG_HDRLEN + nlmAlign(len(payload))
	for _, a := range attrs {
		off += copy(b[off:], a)
	}

	if err := unix.Sendto(x.fd, b, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, err
	}

	return x.seq, nil
}

func (x *XFRM) recv() ([]netlinkMessage, error) {
	for {
		n, from, err := unix.Recvfrom(x.fd, x.buf, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("no answer from the kernel in %s: %w", recvTimeout, err)
		}

		if err != nil {
			return nil, err
		}

		if sa, ok := from.(*unix.SockaddrNetlink); !ok || sa.Pid != 0 {
			continue
		}

		return parseMessages(x.buf[:n])
	}
}

type netlinkMessage struct {
	Header unix.NlMsghdr
	Data   []byte
}

func parseMessages(b []byte) ([]netlinkMessage, error) {
	var out []netlinkMessage

	for len(b) >= unix.NLMSG_HDRLEN {
		h, _ := decode[unix.NlMsghdr](b)
		if h.Len < unix.NLMSG_HDRLEN || int(h.Len) > len(b) {
			return nil, errors.New("truncated netlink message")
		}

		out = append(out, netlinkMessage{Header: h, Data: b[unix.NLMSG_HDRLEN:h.Len]})

		b = b[min(nlmAlign(int(h.Len)), len(b)):]
	}

	return out, nil
}

func ackError(m netlinkMessage) error {
	if len(m.Data) < 4 {
		return errors.New("truncated netlink acknowledgement")
	}

	errno := -int32(binary.NativeEndian.Uint32(m.Data))
	if errno == 0 {
		return nil
	}

	e := &NetlinkError{Errno: unix.Errno(errno)}

	if m.Header.Flags&unix.NLM_F_ACK_TLVS == 0 {
		return e
	}

	var attrs []byte

	switch {
	case m.Header.Type == unix.NLMSG_DONE:
		attrs = m.Data[4:]
	case len(m.Data) < 4+unix.NLMSG_HDRLEN:
		return e
	case m.Header.Flags&unix.NLM_F_CAPPED != 0:
		attrs = m.Data[4+unix.NLMSG_HDRLEN:]
	default:
		inner, _ := decode[unix.NlMsghdr](m.Data[4:])
		attrs = m.Data[min(4+nlmAlign(int(inner.Len)), len(m.Data)):]
	}

	for len(attrs) >= unix.SizeofNlAttr {
		l := int(binary.NativeEndian.Uint16(attrs))
		t := binary.NativeEndian.Uint16(attrs[2:])

		if l < unix.SizeofNlAttr || l > len(attrs) {
			break
		}

		if t == nlmsgerrAttrMsg {
			e.Message = string(trimNUL(attrs[unix.SizeofNlAttr:l]))
		}

		attrs = attrs[min(nlaAlign(l), len(attrs)):]
	}

	return e
}

func trimNUL(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}

	return b
}
