// Package middleware provides IP-based rate limiting and brute-force protection.
package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ── bucket: sliding-window counter per IP ────────────────────────────────────

type bucket struct {
	mu        sync.Mutex
	count     int
	windowEnd time.Time
}

func (b *bucket) allow(limit int, window time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.After(b.windowEnd) {
		b.count = 0
		b.windowEnd = now.Add(window)
	}
	if limit <= 0 { // 0 = disabled
		return true
	}
	if b.count >= limit {
		return false
	}
	b.count++
	return true
}

// ── generic store ─────────────────────────────────────────────────────────────

type store struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
}

func newStore() *store {
	s := &store{buckets: make(map[string]*bucket)}
	go s.cleanup()
	return s
}

func (s *store) get(key string) *bucket {
	s.mu.RLock()
	b, ok := s.buckets[key]
	s.mu.RUnlock()
	if ok {
		return b
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// double-check
	if b, ok = s.buckets[key]; ok {
		return b
	}
	b = &bucket{}
	s.buckets[key] = b
	return b
}

// cleanup removes stale buckets every minute to prevent unbounded memory growth.
func (s *store) cleanup() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for k, b := range s.buckets {
			b.mu.Lock()
			stale := now.After(b.windowEnd)
			b.mu.Unlock()
			if stale {
				delete(s.buckets, k)
			}
		}
		s.mu.Unlock()
	}
}

// ── exported limiters ─────────────────────────────────────────────────────────

var (
	randomStore = newStore()
	authStore   = newStore()
)

// getIP extracts the real client IP, respecting X-Forwarded-For.
func getIP(c *gin.Context) string {
	if ip := c.GetHeader("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := c.GetHeader("X-Forwarded-For"); ip != "" {
		// take the first entry
		for i, ch := range ip {
			if ch == ',' {
				return ip[:i]
			}
		}
		return ip
	}
	return c.ClientIP()
}

// RandomRateLimit returns middleware that limits random-image requests per IP per minute.
// limitFn is called each request so changes to config are picked up dynamically.
func RandomRateLimit(limitFn func() int) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := getIP(c)
		limit := limitFn()
		if !randomStore.get(ip).allow(limit, time.Minute) {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded, try again later",
			})
			return
		}
		c.Next()
	}
}

// AuthBruteForce returns a gate that silently rate-limits auth attempts.
// On rate-limit it still returns 401 (not 429) so the client cannot detect the cooldown.
func AuthBruteForce(limitFn func() int) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := getIP(c)
		limit := limitFn()
		if limit > 0 && !authStore.get(ip).allow(limit, time.Minute) {
			// Return generic 401 — do NOT reveal it's a rate-limit
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
	}
}

// RecordAuthSuccess resets the failure counter for an IP after a successful login.
func RecordAuthSuccess(ip string) {
	b := authStore.get(ip)
	b.mu.Lock()
	b.count = 0
	b.mu.Unlock()
}

// ClientIP is exported so handlers can call RecordAuthSuccess with the same IP logic.
func ClientIP(c *gin.Context) string {
	return getIP(c)
}
