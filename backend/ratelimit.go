package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a per-client token bucket with an escalating lockout.
//
// The bucket alone only slows a scanner down; the lockout is what actually
// stops PIN enumeration. A client that racks up failLimit consecutive misses
// (a wrong or expired PIN) is refused entirely for lockout, and any success
// resets the count, so a legitimate user who fat-fingers a PIN is unaffected.
type rateLimiter struct {
	mu      sync.Mutex
	clients map[string]*clientState

	rate      float64 // tokens refilled per second
	burst     float64 // bucket capacity
	failLimit int     // consecutive failures tolerated before lockout
	lockout   time.Duration
}

type clientState struct {
	tokens       float64
	last         time.Time
	fails        int
	blockedUntil time.Time
}

func newRateLimiter(perMinute int, failLimit int, lockout time.Duration) *rateLimiter {
	rl := &rateLimiter{
		clients:   make(map[string]*clientState),
		rate:      float64(perMinute) / 60.0,
		burst:     float64(perMinute),
		failLimit: failLimit,
		lockout:   lockout,
	}
	go rl.collectGarbage()
	return rl
}

// allow reports whether the client may proceed, and if not, how long to wait.
func (rl *rateLimiter) allow(key string) (bool, time.Duration) {
	if rl == nil {
		return true, 0
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	c, ok := rl.clients[key]
	if !ok {
		c = &clientState{tokens: rl.burst, last: now}
		rl.clients[key] = c
	}

	if now.Before(c.blockedUntil) {
		return false, time.Until(c.blockedUntil).Round(time.Second)
	}

	c.tokens += now.Sub(c.last).Seconds() * rl.rate
	if c.tokens > rl.burst {
		c.tokens = rl.burst
	}
	c.last = now

	if c.tokens < 1 {
		wait := time.Duration((1-c.tokens)/rl.rate*float64(time.Second)) + time.Second
		return false, wait.Round(time.Second)
	}
	c.tokens--
	return true, 0
}

// recordFailure counts a miss and trips the lockout once too many pile up.
func (rl *rateLimiter) recordFailure(key string) {
	if rl == nil || rl.failLimit <= 0 {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	c, ok := rl.clients[key]
	if !ok {
		c = &clientState{tokens: rl.burst, last: time.Now()}
		rl.clients[key] = c
	}
	c.fails++
	if c.fails >= rl.failLimit {
		c.blockedUntil = time.Now().Add(rl.lockout)
		c.fails = 0
	}
}

func (rl *rateLimiter) recordSuccess(key string) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if c, ok := rl.clients[key]; ok {
		c.fails = 0
	}
}

// collectGarbage drops clients that are idle and not serving a lockout, so the
// map cannot grow without bound under a spray of spoofed or rotating addresses.
func (rl *rateLimiter) collectGarbage() {
	for range time.Tick(5 * time.Minute) {
		cutoff := time.Now().Add(-10 * time.Minute)
		rl.mu.Lock()
		for key, c := range rl.clients {
			if c.last.Before(cutoff) && time.Now().After(c.blockedUntil) {
				delete(rl.clients, key)
			}
		}
		rl.mu.Unlock()
	}
}

// middleware refuses over-limit requests before they reach the handler.
func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl == nil {
			next.ServeHTTP(w, r)
			return
		}
		if ok, retryAfter := rl.allow(clientIP(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			writeError(w, http.StatusTooManyRequests,
				"too many requests; retry in "+retryAfter.String())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP identifies the caller for rate-limiting purposes.
//
// X-Forwarded-For is only honoured when TRUSTED_PROXY is set, because any
// client can forge that header: trusting it unconditionally would let a scanner
// dodge the limiter by inventing a new address on every request.
func clientIP(r *http.Request) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, found := strings.Cut(xff, ","); found {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
		if real := r.Header.Get("X-Real-IP"); real != "" {
			return strings.TrimSpace(real)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
