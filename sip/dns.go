package sip

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	typeNAPTR         = dnsmessage.Type(35)
	maxUDPPayload     = 1232
	dnsTimeout        = 5 * time.Second
	dnsAttempts       = 2
	defaultNdots      = 1
	maxNdots          = 15
	maxDNSMessage     = 1<<16 - 1
	defaultDNSPort    = 53
	resolvConfLimit   = 64 << 10
	resolvConfRecheck = 5 * time.Second
	maxCacheTTL       = 5 * time.Minute
	maxCacheEntries   = 1024
	maxCNAMEChain     = 8
)

var errNoEDNS = errors.New("sip: DNS server does not support EDNS")

var system = &systemDNS{conf: "/etc/resolv.conf"}

type systemDNS struct {
	conf    string
	servers []netip.AddrPort
	now     func() time.Time

	mu       sync.Mutex
	loaded   resolvConf
	loadedAt time.Time
	stamp    fileStamp
	cache    map[dnsKey]dnsEntry

	rotation atomic.Uint32
}

type fileStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

type dnsKey struct {
	name  string
	qtype dnsmessage.Type
}

type dnsAnswer struct {
	naptr []NAPTR
	srv   []net.SRV
}

type dnsEntry struct {
	dnsAnswer
	notFound bool
	expires  time.Time
}

type dnsResult struct {
	dnsAnswer
	notFound bool
	ttl      time.Duration
}

type resolvConf struct {
	servers  []netip.AddrPort
	search   []string
	ndots    int
	timeout  time.Duration
	attempts int
	rotate   bool
	tcp      bool
}

func readResolvConf(path string) resolvConf {
	c := resolvConf{ndots: defaultNdots, timeout: dnsTimeout, attempts: dnsAttempts}

	searched := false

	f, err := os.Open(path)
	if err == nil {
		defer func() { _ = f.Close() }()

		s := bufio.NewScanner(io.LimitReader(f, resolvConfLimit))
		for s.Scan() {
			searched = c.parse(s.Text()) || searched
		}
	}

	if len(c.servers) == 0 {
		c.servers = []netip.AddrPort{
			netip.AddrPortFrom(netip.IPv6Loopback(), defaultDNSPort),
			netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), defaultDNSPort),
		}
	}

	if !searched {
		if h, err := os.Hostname(); err == nil {
			if _, domain, ok := strings.Cut(h, "."); ok && domain != "" {
				c.search = []string{absolute(domain)}
			}
		}
	}

	return c
}

func (c *resolvConf) parse(line string) bool {
	f := strings.Fields(line)
	if len(f) < 2 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], ";") {
		return false
	}

	switch f[0] {
	case "nameserver":
		if a, err := netip.ParseAddr(f[1]); err == nil {
			c.servers = append(c.servers, netip.AddrPortFrom(a, defaultDNSPort))
		}
	case "domain":
		c.search = []string{absolute(f[1])}
		return true
	case "search":
		c.search = nil

		for _, d := range f[1:] {
			if strings.TrimSuffix(d, ".") != "" {
				c.search = append(c.search, absolute(d))
			}
		}

		return true
	case "options":
		for _, o := range f[1:] {
			c.option(o)
		}
	}

	return false
}

func (c *resolvConf) option(o string) {
	name, value, _ := strings.Cut(o, ":")

	switch name {
	case "rotate":
		c.rotate = true
		return
	case "use-vc", "usevc", "tcp":
		c.tcp = true
		return
	}

	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return
	}

	switch {
	case name == "ndots":
		c.ndots = min(n, maxNdots)
	case name == "timeout" && n > 0:
		c.timeout = time.Duration(min(n, 30)) * time.Second
	case name == "attempts" && n > 0:
		c.attempts = min(n, 5)
	}
}

func (c resolvConf) names(name string) []string {
	if strings.HasSuffix(name, ".") {
		return []string{name}
	}

	dotted := strings.Count(name, ".") >= c.ndots

	var out []string

	if dotted {
		out = append(out, name+".")
	}

	for _, s := range c.search {
		out = append(out, name+"."+s)
	}

	if !dotted {
		out = append(out, name+".")
	}

	return out
}

func (d *systemDNS) clock() time.Time {
	if d.now != nil {
		return d.now()
	}

	return time.Now()
}

func (d *systemDNS) config() resolvConf {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.clock()

	if d.loadedAt.IsZero() || now.Sub(d.loadedAt) >= resolvConfRecheck {
		var stamp fileStamp
		if fi, err := os.Stat(d.conf); err == nil {
			stamp = fileStamp{mod: fi.ModTime(), size: fi.Size(), ok: true}
		}

		if d.loadedAt.IsZero() || stamp != d.stamp {
			d.loaded = readResolvConf(d.conf)
			d.stamp = stamp
			d.cache = nil
		}

		d.loadedAt = now
	}

	c := d.loaded
	if d.servers != nil {
		c.servers = d.servers
	}

	return c
}

func (d *systemDNS) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

func (d *systemDNS) LookupNAPTR(ctx context.Context, name string) ([]NAPTR, error) {
	a, err := d.lookup(ctx, name, typeNAPTR)
	if err != nil {
		return nil, err
	}

	return a.naptr, nil
}

func (d *systemDNS) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	if service != "" || proto != "" {
		name = "_" + service + "._" + proto + "." + name
	}

	a, err := d.lookup(ctx, name, dnsmessage.TypeSRV)
	if err != nil {
		return "", nil, err
	}

	out := make([]*net.SRV, len(a.srv))
	for i := range a.srv {
		rec := a.srv[i]
		out[i] = &rec
	}

	return name, out, nil
}

func (d *systemDNS) lookup(ctx context.Context, name string, qtype dnsmessage.Type) (dnsAnswer, error) {
	conf := d.config()

	var failed error

	for _, fqdn := range conf.names(name) {
		res, err := d.cached(ctx, conf, fqdn, qtype)

		switch {
		case ctx.Err() != nil:
			return dnsAnswer{}, ctx.Err()
		case err != nil:
			if failed == nil {
				failed = err
			}
		case !res.notFound:
			return res.dnsAnswer, nil
		}
	}

	if failed != nil {
		return dnsAnswer{}, failed
	}

	return dnsAnswer{}, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *systemDNS) cached(ctx context.Context, conf resolvConf, fqdn string, qtype dnsmessage.Type) (dnsResult, error) {
	key := dnsKey{name: strings.ToLower(fqdn), qtype: qtype}

	if e, ok := d.get(key); ok {
		return dnsResult{dnsAnswer: e.dnsAnswer, notFound: e.notFound}, nil
	}

	res, err := d.query(ctx, conf, fqdn, qtype)
	if err != nil {
		return dnsResult{}, err
	}

	d.put(key, res)

	return res, nil
}

func (d *systemDNS) get(key dnsKey) (dnsEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	e, ok := d.cache[key]
	if !ok {
		return dnsEntry{}, false
	}

	if !d.clock().Before(e.expires) {
		delete(d.cache, key)
		return dnsEntry{}, false
	}

	return e, true
}

func (d *systemDNS) put(key dnsKey, res dnsResult) {
	ttl := min(res.ttl, maxCacheTTL)
	if ttl <= 0 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.clock()

	if d.cache == nil {
		d.cache = map[dnsKey]dnsEntry{}
	}

	if _, ok := d.cache[key]; !ok && len(d.cache) >= maxCacheEntries {
		for k, e := range d.cache {
			if !now.Before(e.expires) {
				delete(d.cache, k)
			}
		}

		for k := range d.cache {
			if len(d.cache) < maxCacheEntries {
				break
			}

			delete(d.cache, k)
		}
	}

	d.cache[key] = dnsEntry{dnsAnswer: res.dnsAnswer, notFound: res.notFound, expires: now.Add(ttl)}
}

func (d *systemDNS) query(ctx context.Context, conf resolvConf, fqdn string, qtype dnsmessage.Type) (dnsResult, error) {
	name, err := dnsmessage.NewName(fqdn)
	if err != nil {
		return dnsResult{}, &net.DNSError{Err: err.Error(), Name: fqdn}
	}

	q := dnsmessage.Question{Name: name, Type: qtype, Class: dnsmessage.ClassINET}
	last := &net.DNSError{Err: "no DNS server answered", Name: fqdn, IsTemporary: true}

	first := 0
	if conf.rotate && len(conf.servers) > 0 {
		first = int((d.rotation.Add(1) - 1) % uint32(len(conf.servers)))
	}

	for range conf.attempts {
		for i := range conf.servers {
			server := conf.servers[(first+i)%len(conf.servers)]

			res, err := exchange(ctx, server, q, conf)
			if err == nil {
				return res, nil
			}

			if ctx.Err() != nil {
				return dnsResult{}, ctx.Err()
			}

			last = &net.DNSError{Err: err.Error(), Name: fqdn, Server: server.String(), IsTemporary: true}
		}
	}

	return dnsResult{}, last
}

// RFC 6891 section 7
func exchange(ctx context.Context, server netip.AddrPort, q dnsmessage.Question, conf resolvConf) (dnsResult, error) {
	ctx, cancel := context.WithTimeout(ctx, conf.timeout)
	defer cancel()

	res, err := exchangeOnce(ctx, server, q, conf.tcp, true)
	if errors.Is(err, errNoEDNS) {
		return exchangeOnce(ctx, server, q, conf.tcp, false)
	}

	return res, err
}

func exchangeOnce(ctx context.Context, server netip.AddrPort, q dnsmessage.Question, tcp, edns bool) (dnsResult, error) {
	id := uint16(rand.Uint32())

	query, err := dnsQuery(id, q, edns)
	if err != nil {
		return dnsResult{}, err
	}

	if !tcp {
		answer, err := exchangeUDP(ctx, server, query, func(msg []byte) bool { return matches(msg, id, q) })
		if err != nil {
			return dnsResult{}, err
		}

		res, truncated, err := parseAnswer(answer, q, edns)
		if !truncated {
			return res, err
		}
	}

	answer, err := exchangeTCP(ctx, server, query)
	if err != nil {
		return dnsResult{}, err
	}

	if !matches(answer, id, q) {
		return dnsResult{}, errors.New("sip: DNS answer to another query")
	}

	res, truncated, err := parseAnswer(answer, q, edns)
	if truncated {
		return dnsResult{}, errors.New("sip: truncated DNS answer over TCP")
	}

	return res, err
}

func dnsQuery(id uint16, q dnsmessage.Question, edns bool) ([]byte, error) {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: id, RecursionDesired: true})
	b.EnableCompression()

	if err := b.StartQuestions(); err != nil {
		return nil, err
	}

	if err := b.Question(q); err != nil {
		return nil, err
	}

	if !edns {
		return b.Finish()
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

// RFC 5452 section 9.1
func matches(msg []byte, id uint16, q dnsmessage.Question) bool {
	var p dnsmessage.Parser

	h, err := p.Start(msg)
	if err != nil || h.ID != id || !h.Response {
		return false
	}

	questions, err := p.AllQuestions()
	if err != nil || len(questions) != 1 {
		return false
	}

	got := questions[0]

	return got.Type == q.Type && got.Class == q.Class && sameName(got.Name, q.Name)
}

// RFC 1035 section 2.3.3
func sameName(a, b dnsmessage.Name) bool {
	return strings.EqualFold(a.String(), b.String())
}

func exchangeUDP(ctx context.Context, server netip.AddrPort, query []byte, valid func([]byte) bool) ([]byte, error) {
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

		if valid(buf[:n]) {
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

type dnsRecord struct {
	owner dnsmessage.Name
	ttl   uint32
	naptr NAPTR
	srv   net.SRV
}

func parseAnswer(msg []byte, q dnsmessage.Question, edns bool) (dnsResult, bool, error) {
	var p dnsmessage.Parser

	h, err := p.Start(msg)
	if err != nil {
		return dnsResult{}, false, err
	}

	if h.Truncated {
		return dnsResult{}, true, nil
	}

	if err := p.SkipAllQuestions(); err != nil {
		return dnsResult{}, false, err
	}

	switch h.RCode {
	case dnsmessage.RCodeSuccess, dnsmessage.RCodeNameError:
	case dnsmessage.RCodeFormatError:
		if edns && !hasOPT(&p) {
			return dnsResult{}, false, errNoEDNS
		}

		return dnsResult{}, false, fmt.Errorf("sip: DNS %s", h.RCode)
	default:
		return dnsResult{}, false, fmt.Errorf("sip: DNS %s", h.RCode)
	}

	var (
		records []dnsRecord
		aliases = map[string]dnsRecord{}
	)

	for h.RCode == dnsmessage.RCodeSuccess {
		rh, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}

		if err != nil {
			return dnsResult{}, false, err
		}

		switch {
		case rh.Class != dnsmessage.ClassINET:
			err = p.SkipAnswer()
		case rh.Type == dnsmessage.TypeCNAME:
			var r dnsmessage.CNAMEResource

			r, err = p.CNAMEResource()
			if err == nil {
				aliases[strings.ToLower(rh.Name.String())] = dnsRecord{owner: r.CNAME, ttl: rh.TTL}
			}
		case rh.Type == q.Type:
			var (
				rec dnsRecord
				ok  bool
			)

			rec, ok, err = record(&p, q.Type)
			if ok {
				rec.owner, rec.ttl = rh.Name, rh.TTL
				records = append(records, rec)
			}
		default:
			err = p.SkipAnswer()
		}

		if err != nil {
			return dnsResult{}, false, err
		}
	}

	owner, ttl := canonical(q.Name, aliases)

	var res dnsResult

	for _, rec := range records {
		if !sameName(rec.owner, owner) {
			continue
		}

		if q.Type == dnsmessage.TypeSRV {
			res.srv = append(res.srv, rec.srv)
		} else {
			res.naptr = append(res.naptr, rec.naptr)
		}

		ttl = min(ttl, rec.ttl)
	}

	if len(res.naptr)+len(res.srv) > 0 {
		res.ttl = time.Duration(ttl) * time.Second
		return res, false, nil
	}

	return dnsResult{notFound: true, ttl: negativeTTL(&p)}, false, nil
}

// RFC 1035 section 3.3.1
func canonical(name dnsmessage.Name, aliases map[string]dnsRecord) (dnsmessage.Name, uint32) {
	ttl := uint32(1<<32 - 1)

	for range maxCNAMEChain {
		alias, ok := aliases[strings.ToLower(name.String())]
		if !ok || sameName(alias.owner, name) {
			break
		}

		name, ttl = alias.owner, min(ttl, alias.ttl)
	}

	return name, ttl
}

func hasOPT(p *dnsmessage.Parser) bool {
	if p.SkipAllAnswers() != nil || p.SkipAllAuthorities() != nil {
		return false
	}

	for {
		rh, err := p.AdditionalHeader()
		if err != nil {
			return false
		}

		if rh.Type == dnsmessage.TypeOPT {
			return true
		}

		if p.SkipAdditional() != nil {
			return false
		}
	}
}

func record(p *dnsmessage.Parser, qtype dnsmessage.Type) (dnsRecord, bool, error) {
	if qtype == dnsmessage.TypeSRV {
		r, err := p.SRVResource()
		if err != nil {
			return dnsRecord{}, false, err
		}

		return dnsRecord{srv: net.SRV{Target: r.Target.String(), Port: r.Port, Priority: r.Priority, Weight: r.Weight}}, true, nil
	}

	r, err := p.UnknownResource()
	if err != nil {
		return dnsRecord{}, false, err
	}

	rec, ok := decodeNAPTR(r.Data)

	return dnsRecord{naptr: rec}, ok, nil
}

func negativeTTL(p *dnsmessage.Parser) time.Duration {
	if err := p.SkipAllAnswers(); err != nil {
		return 0
	}

	for {
		rh, err := p.AuthorityHeader()
		if err != nil {
			return 0
		}

		if rh.Type != dnsmessage.TypeSOA {
			if err := p.SkipAuthority(); err != nil {
				return 0
			}

			continue
		}

		soa, err := p.SOAResource()
		if err != nil {
			return 0
		}

		return time.Duration(min(rh.TTL, soa.MinTTL)) * time.Second
	}
}

// RFC 2915 section 8
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
