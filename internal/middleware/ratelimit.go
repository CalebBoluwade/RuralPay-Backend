package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/ruralpay/backend/internal/utils"
)

// RateLimitConfig defines the limit and window for a rate limiter tier.
type RateLimitConfig struct {
	// Max number of requests allowed within Window.
	Limit int
	// Window is the rolling time window for the limit.
	Window time.Duration
}

var (
	// AuthStrict is used for high-risk auth endpoints (login, password reset).
	AuthStrict = RateLimitConfig{Limit: 5, Window: 15 * time.Minute}
	// AuthGeneral is used for lower-risk auth endpoints (register, refresh, OTP).
	AuthGeneral = RateLimitConfig{Limit: 10, Window: 10 * time.Minute}
	// GlobalLimit is the default limit applied to all other routes.
	GlobalLimit = RateLimitConfig{Limit: 60, Window: time.Minute}
)

// RateLimiter returns a middleware that enforces the given config per client IP.
// It uses Redis INCR + EXPIRE for a fixed-window counter that works across
// multiple server instances.
func RateLimiter(rdb *redis.Client, cfg RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := realIP(r)
			key := fmt.Sprintf("rl:%s:%s", r.URL.Path, ip)

			count, err := increment(r.Context(), rdb, key, cfg.Window)
			if err != nil {
				// Redis unavailable — fail open to avoid blocking legitimate traffic,
				// but log so ops can act.
				slog.Error("rate_limiter.redis_error", "path", r.URL.Path, "ip", ip, "error", err)
				next.ServeHTTP(w, r)
				return
			}

			remaining := cfg.Limit - count
			if remaining < 0 {
				remaining = 0
			}

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.Limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("X-RateLimit-Window", cfg.Window.String())

			if count > cfg.Limit {
				slog.Warn("rate_limiter.exceeded", "path", r.URL.Path, "ip", ip, "count", count, "limit", cfg.Limit)
				w.Header().Set("Retry-After", strconv.Itoa(int(cfg.Window.Seconds())))
				utils.SendErrorResponse(w, "Too many requests, please try again later", http.StatusTooManyRequests, nil)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// increment atomically increments the counter for key and sets the TTL on
// first access. Returns the new count.
func increment(ctx context.Context, rdb *redis.Client, key string, window time.Duration) (int, error) {
	pipe := rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return int(incr.Val()), nil
}

// realIP extracts the client IP, respecting the X-Real-IP header set by
// chi's RealIP middleware upstream.
func realIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
