package api

import (
	"container/list"
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
	windows map[string]*list.Element
	order   *list.List
}

type rateWindow struct {
	key   string
	start time.Time
	count int
}

func newRateLimiter(limit int, window time.Duration, now func() time.Time) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, now: now, windows: map[string]*list.Element{}, order: list.New()}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	e, ok := l.windows[key]
	if !ok {
		if l.order.Len() >= maxRateKeys {
			l.remove(l.order.Front())
		}

		e = l.order.PushBack(&rateWindow{key: key, start: now})
		l.windows[key] = e
	}

	w := e.Value.(*rateWindow)
	w.count++

	return w.count <= l.limit
}

func (l *rateLimiter) exceeded(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.windows[key]
	if !ok {
		return false
	}

	w := e.Value.(*rateWindow)

	return l.now().Sub(w.start) < l.window && w.count >= l.limit
}

func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.windows[key]; ok {
		l.remove(e)
	}
}

func (l *rateLimiter) sweep(now time.Time) {
	for e := l.order.Front(); e != nil && now.Sub(e.Value.(*rateWindow).start) >= l.window; e = l.order.Front() {
		l.remove(e)
	}
}

func (l *rateLimiter) remove(e *list.Element) {
	delete(l.windows, e.Value.(*rateWindow).key)
	l.order.Remove(e)
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
