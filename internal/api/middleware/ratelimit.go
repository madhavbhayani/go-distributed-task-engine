package middleware

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ════════════════════════════════════════════════════════════════
//  HTTP Rate Limiter Middleware (Token Bucket per client)
//
//  Limits the number of API requests per client per time window.
//  Stateless per-instance — for multi-node, use Redis-backed
//  limiter in future.
//
//  Headers returned:
//    X-RateLimit-Limit     — max requests per window
//    X-RateLimit-Remaining — tokens left
//    X-RateLimit-Reset     — seconds until full refill
// ════════════════════════════════════════════════════════════════

// RateLimitConfig holds rate limiter settings.
type RateLimitConfig struct {
	Enabled         bool          // If false, rate limiting is bypassed
	RequestsPerMin  int           // Max requests per client per minute
	BurstSize       int           // Max burst above steady rate
	CleanupInterval time.Duration // How often to purge expired buckets
}

// rateBucket is a per-client token bucket.
type rateBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

func (b *rateBucket) allow() (bool, float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens--
		return true, b.tokens
	}
	return false, 0
}

// rateLimiter holds all client buckets.
type rateLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*rateBucket
	config  RateLimitConfig
}

func newRateLimiter(cfg RateLimitConfig) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*rateBucket),
		config:  cfg,
	}

	// Background cleanup of stale buckets
	if cfg.CleanupInterval > 0 {
		go rl.cleanupLoop(cfg.CleanupInterval)
	}

	return rl
}

func (rl *rateLimiter) getBucket(clientID string) *rateBucket {
	rl.mu.RLock()
	b, ok := rl.buckets[clientID]
	rl.mu.RUnlock()
	if ok {
		return b
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check
	b, ok = rl.buckets[clientID]
	if ok {
		return b
	}

	burst := rl.config.BurstSize
	if burst < rl.config.RequestsPerMin {
		burst = rl.config.RequestsPerMin
	}

	b = &rateBucket{
		tokens:     float64(burst),
		maxTokens:  float64(burst),
		refillRate: float64(rl.config.RequestsPerMin) / 60.0, // tokens per second
		lastRefill: time.Now(),
	}
	rl.buckets[clientID] = b
	return b
}

func (rl *rateLimiter) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-5 * time.Minute)
		for id, b := range rl.buckets {
			b.mu.Lock()
			if b.lastRefill.Before(cutoff) {
				delete(rl.buckets, id)
			}
			b.mu.Unlock()
		}
		rl.mu.Unlock()
	}
}

// RateLimit returns HTTP middleware that enforces per-client rate limits.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	limiter := newRateLimiter(cfg)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Identify client: X-Client-ID (set by auth) → IP fallback
			clientID := r.Header.Get("X-Client-ID")
			if clientID == "" {
				clientID = r.RemoteAddr
			}

			bucket := limiter.getBucket(clientID)
			allowed, remaining := bucket.allow()

			// Set rate limit headers
			w.Header().Set("X-RateLimit-Limit", formatInt(cfg.RequestsPerMin))
			w.Header().Set("X-RateLimit-Remaining", formatInt(int(remaining)))
			w.Header().Set("X-RateLimit-Reset", formatInt(60)) // full refill in ~60s

			if !allowed {
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(models.APIResponse{
					Success: false,
					Error: &models.APIError{
						Code:    "RATE_LIMIT_EXCEEDED",
						Message: "Too many requests. Please retry after cooling down.",
						Details: map[string]string{
							"limit":     formatInt(cfg.RequestsPerMin) + " req/min",
							"client_id": clientID,
						},
					},
					Timestamp: time.Now().UTC(),
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func formatInt(n int) string {
	return time.Duration(n).String()[:0] + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
