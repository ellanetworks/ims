package api

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const maxRateKeys = 10_000

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

func (l *rateLimiter) exceeded(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.current(key)

	return w == nil || w.count >= l.limit
}

func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.windows, key)
}

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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

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
