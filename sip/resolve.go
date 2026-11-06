package sip

import (
	"bufio"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// RFC 3263 section 4
type Server struct {
	Transport Transport
	Addr      netip.AddrPort
	Domain    string
}

type NAPTR struct {
	Order       uint16
	Preference  uint16
	Flags       string
	Service     string
	Regexp      string
	Replacement string
}

type DNS interface {
	LookupNAPTR(ctx context.Context, name string) ([]NAPTR, error)
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// RFC 3263 section 4
// RFC 3263 section 4.1
type Resolver struct {
	DNS        DNS
	Transports []Transport
	Rand       func(n uint64) uint64
}

// RFC 3263 section 4
func (r *Resolver) Resolve(ctx context.Context, u URI) ([]Server, error) {
	servers, err := r.resolve(ctx, u)
	if err != nil {
		return nil, err
	}

	if _, ok := HostAddr(target(u)); !ok {
		for i := range servers {
			servers[i].Domain = strings.TrimSuffix(target(u), ".")
		}
	}

	return servers, nil
}

// RFC 3263 section 4.1
// RFC 3263 section 4.2
// RFC 3263 section 4.3
func (r *Resolver) resolve(ctx context.Context, u URI) ([]Server, error) {
	if !u.IsSIP() || target(u) == "" {
		return nil, fmt.Errorf("sip: no destination in %s", u)
	}

	secure := u.IsSIPS()

	tr, explicit, err := uriTransport(u, secure)
	if err != nil {
		return nil, err
	}

	if !explicit && !r.supports(tr, secure) {
		tr = r.preferred(tr)
	}

	if a, ok := HostAddr(target(u)); ok {
		port := u.Port
		if port == 0 {
			port = DefaultPortFor(tr)
		}

		return []Server{{Transport: tr, Addr: netip.AddrPortFrom(a.Unmap(), port)}}, nil
	}

	host := strings.TrimSuffix(target(u), ".")

	if u.Port != 0 {
		return r.addresses(ctx, host, tr, u.Port)
	}

	if explicit {
		service, proto := srvService(tr)
		servers, found, err := r.srv(ctx, service, proto, host, tr)

		switch {
		case err != nil:
			return nil, err
		case len(servers) > 0:
			return servers, nil
		case found:
			return nil, fmt.Errorf("sip: no address for the SRV targets of %s", host)
		}

		return r.addresses(ctx, host, tr, DefaultPortFor(tr))
	}

	servers, err := r.naptr(ctx, host, secure)
	if err != nil || len(servers) > 0 {
		return servers, err
	}

	var found bool

	for _, t := range r.fallback(secure) {
		service, proto := srvService(t)

		s, ok, err := r.srv(ctx, service, proto, host, t)
		if err != nil {
			return nil, err
		}

		servers = append(servers, s...)
		found = found || ok
	}

	switch {
	case len(servers) > 0:
		return servers, nil
	case found:
		return nil, fmt.Errorf("sip: no address for the SRV targets of %s", host)
	}

	return r.addresses(ctx, host, tr, DefaultPortFor(tr))
}

// RFC 3263 section 4.1
func uriTransport(u URI, secure bool) (Transport, bool, error) {
	t, ok := u.Params.Get("transport")
	if !ok {
		if secure {
			return TLS, false, nil
		}

		return UDP, false, nil
	}

	tr := Transport(strings.ToUpper(t))

	switch {
	case secure && (tr == TCP || tr == TLS):
		return TLS, true, nil
	case !secure && (tr == UDP || tr == TCP || tr == TLS):
		return tr, true, nil
	}

	return "", false, fmt.Errorf("sip: destination transport %s", t)
}

func (r *Resolver) supports(tr Transport, secure bool) bool {
	if secure {
		return tr == TLS
	}

	if r.Transports == nil {
		return tr == UDP || tr == TCP
	}

	return slices.Contains(r.Transports, tr)
}

// RFC 3263 section 4.1
func (r *Resolver) preferred(tr Transport) Transport {
	if i := slices.IndexFunc(r.Transports, func(t Transport) bool { return t == TCP || t == TLS }); i >= 0 {
		return r.Transports[i]
	}

	return tr
}

// RFC 3263 section 4.1
// RFC 3263 section 4.2
func (r *Resolver) fallback(secure bool) []Transport {
	if secure {
		return []Transport{TLS}
	}

	var out []Transport

	for _, tr := range []Transport{TLS, UDP, TCP} {
		if r.supports(tr, false) {
			out = append(out, tr)
		}
	}

	return out
}

// RFC 3263 section 4.1
func naptrTransport(service string) (Transport, bool) {
	switch strings.ToUpper(service) {
	case "SIP+D2U":
		return UDP, true
	case "SIP+D2T":
		return TCP, true
	case "SIPS+D2T":
		return TLS, true
	}

	return "", false
}

// RFC 3263 section 4.1
func (r *Resolver) naptr(ctx context.Context, host string, secure bool) ([]Server, error) {
	records, err := r.dns().LookupNAPTR(ctx, host)
	if err != nil {
		return nil, ctx.Err()
	}

	type usable struct {
		NAPTR
		tr Transport
	}

	var candidates []usable

	for _, rec := range records {
		tr, ok := naptrTransport(rec.Service)
		if !ok || !r.supports(tr, secure) || !strings.EqualFold(rec.Flags, "s") || rec.Regexp != "" {
			continue
		}

		if name := strings.TrimSuffix(rec.Replacement, "."); name != "" {
			rec.Replacement = name
			candidates = append(candidates, usable{rec, tr})
		}
	}

	slices.SortStableFunc(candidates, func(a, b usable) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.Preference, b.Preference))
	})

	for _, c := range candidates {
		servers, _, err := r.srv(ctx, "", "", c.Replacement, c.tr)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if len(servers) > 0 {
			return servers, nil
		}
	}

	return nil, nil
}

// RFC 3263 section 4.2
func srvService(tr Transport) (string, string) {
	if tr == TLS {
		return "sips", "tcp"
	}

	return "sip", strings.ToLower(string(tr))
}

// RFC 2782
// RFC 3263 section 4.2
func (r *Resolver) srv(ctx context.Context, service, proto, name string, tr Transport) ([]Server, bool, error) {
	_, records, err := r.dns().LookupSRV(ctx, service, proto, name)

	var dnsErr *net.DNSError

	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return nil, false, nil
	case err != nil && len(records) == 0:
		return nil, false, fmt.Errorf("sip: SRV of %s: %w", name, err)
	case len(records) == 0:
		return nil, false, nil
	}

	var out []Server

	for _, rec := range r.order(records) {
		target := strings.TrimSuffix(rec.Target, ".")
		if target == "" {
			continue
		}

		servers, err := r.addresses(ctx, target, tr, rec.Port)
		if err != nil {
			continue
		}

		out = append(out, servers...)
	}

	return out, true, nil
}

// RFC 2782
func (r *Resolver) order(records []*net.SRV) []*net.SRV {
	sorted := slices.Clone(records)
	slices.SortStableFunc(sorted, func(a, b *net.SRV) int { return cmp.Compare(a.Priority, b.Priority) })

	out := make([]*net.SRV, 0, len(sorted))

	for start := 0; start < len(sorted); {
		end := start
		for end < len(sorted) && sorted[end].Priority == sorted[start].Priority {
			end++
		}

		group := slices.Clone(sorted[start:end])
		slices.SortStableFunc(group, func(a, b *net.SRV) int { return cmp.Compare(min(a.Weight, 1), min(b.Weight, 1)) })

		for len(group) > 0 {
			var sum uint64
			for _, rec := range group {
				sum += uint64(rec.Weight)
			}

			pick, running := r.random(sum+1), uint64(0)

			i := slices.IndexFunc(group, func(rec *net.SRV) bool {
				running += uint64(rec.Weight)
				return running >= pick
			})

			out = append(out, group[i])
			group = slices.Delete(group, i, i+1)
		}

		start = end
	}

	return out
}

func (r *Resolver) random(n uint64) uint64 {
	if r.Rand != nil {
		return r.Rand(n)
	}

	return rand.Uint64N(n)
}

func (r *Resolver) addresses(ctx context.Context, host string, tr Transport, port uint16) ([]Server, error) {
	addrs, err := r.dns().LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("sip: addresses of %s: %w", host, err)
	}

	out := make([]Server, 0, len(addrs))

	for _, a := range addrs {
		out = append(out, Server{Transport: tr, Addr: netip.AddrPortFrom(a.Unmap(), port)})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("sip: no address for %s", host)
	}

	return out, nil
}

func (r *Resolver) dns() DNS {
	if r == nil || r.DNS == nil {
		return systemDNS{Resolver: net.DefaultResolver, conf: "/etc/resolv.conf"}
	}

	return r.DNS
}

type systemDNS struct {
	*net.Resolver

	conf    string
	servers []netip.AddrPort
}

const (
	typeNAPTR       = dnsmessage.Type(35)
	maxUDPPayload   = 1232
	dnsTimeout      = 5 * time.Second
	dnsAttempts     = 2
	maxDNSMessage   = 1<<16 - 1
	defaultDNSPort  = 53
	resolvConfLimit = 64 << 10
)

type resolvConf struct {
	servers  []netip.AddrPort
	timeout  time.Duration
	attempts int
}

func readResolvConf(path string) resolvConf {
	c := resolvConf{timeout: dnsTimeout, attempts: dnsAttempts}

	f, err := os.Open(path)
	if err == nil {
		defer func() { _ = f.Close() }()

		s := bufio.NewScanner(io.LimitReader(f, resolvConfLimit))
		for s.Scan() {
			c.parse(s.Text())
		}
	}

	if len(c.servers) == 0 {
		c.servers = []netip.AddrPort{
			netip.AddrPortFrom(netip.IPv6Loopback(), defaultDNSPort),
			netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), defaultDNSPort),
		}
	}

	return c
}

func (c *resolvConf) parse(line string) {
	f := strings.Fields(line)
	if len(f) < 2 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], ";") {
		return
	}

	switch f[0] {
	case "nameserver":
		if a, err := netip.ParseAddr(f[1]); err == nil {
			c.servers = append(c.servers, netip.AddrPortFrom(a, defaultDNSPort))
		}
	case "options":
		for _, o := range f[1:] {
			name, value, _ := strings.Cut(o, ":")

			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				continue
			}

			switch name {
			case "timeout":
				c.timeout = time.Duration(min(n, 30)) * time.Second
			case "attempts":
				c.attempts = min(n, 5)
			}
		}
	}
}

func (d systemDNS) LookupNAPTR(ctx context.Context, name string) ([]NAPTR, error) {
	conf := readResolvConf(d.conf)
	if d.servers != nil {
		conf.servers = d.servers
	}

	q, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, &net.DNSError{Err: err.Error(), Name: name}
	}

	last := &net.DNSError{Err: "no DNS server answered", Name: name, IsTemporary: true}

	for range conf.attempts {
		for _, server := range conf.servers {
			records, err := d.exchange(ctx, server, q, conf.timeout)
			if err == nil {
				return records, nil
			}

			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				return nil, err
			}

			last = &net.DNSError{Err: err.Error(), Name: name, Server: server.String(), IsTemporary: true}
		}
	}

	return nil, last
}

func (d systemDNS) exchange(ctx context.Context, server netip.AddrPort, q dnsmessage.Name, timeout time.Duration) ([]NAPTR, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id := uint16(rand.Uint32())

	query, err := naptrQuery(id, q)
	if err != nil {
		return nil, err
	}

	answer, err := exchangeUDP(ctx, server, query, id)
	if err != nil {
		return nil, err
	}

	records, truncated, err := parseNAPTR(answer, id, q)
	if !truncated {
		return records, err
	}

	answer, err = exchangeTCP(ctx, server, query)
	if err != nil {
		return nil, err
	}

	records, _, err = parseNAPTR(answer, id, q)

	return records, err
}

func naptrQuery(id uint16, q dnsmessage.Name) ([]byte, error) {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: id, RecursionDesired: true})
	b.EnableCompression()

	if err := b.StartQuestions(); err != nil {
		return nil, err
	}

	if err := b.Question(dnsmessage.Question{Name: q, Type: typeNAPTR, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}

	if err := b.StartAdditionals(); err != nil {
		return nil, err
	}

	var opt dnsmessage.ResourceHeader
	if err := opt.SetEDNS0(maxUDPPayload, dnsmessage.RCodeSuccess, false); err != nil {
		return nil, err
	}

	if err := b.OPTResource(opt, dnsmessage.OPTResource{}); err != nil {
		return nil, err
	}

	return b.Finish()
}

func exchangeUDP(ctx context.Context, server netip.AddrPort, query []byte, id uint16) ([]byte, error) {
	var dialer net.Dialer

	c, err := dialer.DialContext(ctx, "udp", server.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}

	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	if _, err := c.Write(query); err != nil {
		return nil, err
	}

	buf := make([]byte, maxDNSMessage)

	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}

		if n >= 2 && binary.BigEndian.Uint16(buf) == id {
			return buf[:n], nil
		}
	}
}

func exchangeTCP(ctx context.Context, server netip.AddrPort, query []byte) ([]byte, error) {
	var dialer net.Dialer

	c, err := dialer.DialContext(ctx, "tcp", server.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}

	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	msg := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(query)), uint16(len(query)))
	if _, err := c.Write(append(msg, query...)); err != nil {
		return nil, err
	}

	var size [2]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return nil, err
	}

	answer := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(c, answer); err != nil {
		return nil, err
	}

	return answer, nil
}

func parseNAPTR(msg []byte, id uint16, q dnsmessage.Name) ([]NAPTR, bool, error) {
	var p dnsmessage.Parser

	h, err := p.Start(msg)
	if err != nil {
		return nil, false, err
	}

	if h.ID != id || !h.Response {
		return nil, false, errors.New("sip: unexpected DNS message")
	}

	if h.Truncated {
		return nil, true, nil
	}

	questions, err := p.AllQuestions()
	if err != nil {
		return nil, false, err
	}

	if len(questions) != 1 || !strings.EqualFold(questions[0].Name.String(), q.String()) || questions[0].Type != typeNAPTR {
		return nil, false, errors.New("sip: DNS answer to another question")
	}

	switch h.RCode {
	case dnsmessage.RCodeSuccess:
	case dnsmessage.RCodeNameError:
		return nil, false, &net.DNSError{Err: "no such host", Name: q.String(), IsNotFound: true}
	default:
		return nil, false, fmt.Errorf("sip: DNS %s", h.RCode)
	}

	var out []NAPTR

	for {
		rh, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}

		if err != nil {
			return nil, false, err
		}

		if rh.Type != typeNAPTR || rh.Class != dnsmessage.ClassINET {
			if err := p.SkipAnswer(); err != nil {
				return nil, false, err
			}

			continue
		}

		res, err := p.UnknownResource()
		if err != nil {
			return nil, false, err
		}

		if rec, ok := decodeNAPTR(res.Data); ok {
			out = append(out, rec)
		}
	}

	if len(out) == 0 {
		return nil, false, &net.DNSError{Err: "no such host", Name: q.String(), IsNotFound: true}
	}

	return out, false, nil
}

func decodeNAPTR(b []byte) (NAPTR, bool) {
	if len(b) < 4 {
		return NAPTR{}, false
	}

	rec := NAPTR{Order: binary.BigEndian.Uint16(b), Preference: binary.BigEndian.Uint16(b[2:])}
	b = b[4:]

	for _, field := range []*string{&rec.Flags, &rec.Service, &rec.Regexp} {
		if len(b) < 1 || len(b) < 1+int(b[0]) {
			return NAPTR{}, false
		}

		*field, b = string(b[1:1+int(b[0])]), b[1+int(b[0]):]
	}

	var labels []string

	for {
		if len(b) < 1 {
			return NAPTR{}, false
		}

		n := int(b[0])
		if n == 0 {
			break
		}

		if n > 63 || len(b) < 1+n {
			return NAPTR{}, false
		}

		labels, b = append(labels, string(b[1:1+n])), b[1+n:]
	}

	if len(b) != 1 {
		return NAPTR{}, false
	}

	rec.Replacement = strings.Join(labels, ".") + "."

	return rec, true
}
