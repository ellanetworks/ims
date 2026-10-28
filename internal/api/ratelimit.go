package api

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// maxRateKeys bounds the clients that a rate limiter tracks. Once it tracks that many in their windows, it refuses
// new ones, rather than grow without bound.
const maxRateKeys = 10_000

// rateLimiter allows each key at most limit events per fixed window. It forgets the windows that are over once per
// window, as it is called, so that it needs no goroutine.
type rateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]*rateWindow
	swept   time.Time
}

type rateWindow struct {
	start time.Time
	count int
}

func newRateLimiter(limit int, window time.Duration, now func() time.Time) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, now: now, windows: map[string]*rateWindow{}}
}

// allow counts an event of key, and reports whether it is within the limit.
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.current(key)
	if w == nil {
		return false
	}

	w.count++

	return w.count <= l.limit
}

// exceeded reports whether key has reached its limit, without counting an event.
func (l *rateLimiter) exceeded(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.current(key)

	return w == nil || w.count >= l.limit
}

// reset forgets the events of key.
func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.windows, key)
}

// current returns the window of key, a new one if its last is over, or nil when it tracks too many keys to add it.
func (l *rateLimiter) current(key string) *rateWindow {
	now := l.now()

	if now.Sub(l.swept) >= l.window {
		for k, w := range l.windows {
			if now.Sub(w.start) >= l.window {
				delete(l.windows, k)
			}
		}

		l.swept = now
	}

	if w, ok := l.windows[key]; ok && now.Sub(w.start) < l.window {
		return w
	}

	if _, ok := l.windows[key]; !ok && len(l.windows) >= maxRateKeys {
		return nil
	}

	w := &rateWindow{start: now}
	l.windows[key] = w

	return w
}

// clientIP is the address of the direct peer of a request. X-Forwarded-For is not trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// clientKey is the key of the client of a request for rate limiting: its IPv4 address, or the /64 of its IPv6
// address, since a single host commonly has a whole /64.
func clientKey(r *http.Request) string {
	addr, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return clientIP(r)
	}

	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}

	return netip.PrefixFrom(addr, 64).Masked().String()
}
