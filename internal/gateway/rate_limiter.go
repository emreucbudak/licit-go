package gateway

import (
	"context"
	"crypto/tls"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/licit/licit-go/internal/config"
	redis "github.com/redis/go-redis/v9"
)

const rateLimitScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_interval = tonumber(ARGV[2])
local requested = 1

local time_parts = redis.call("TIME")
local now = (tonumber(time_parts[1]) * 1000) + math.floor(tonumber(time_parts[2]) / 1000)

local bucket = redis.call("HMGET", key, "tokens", "updated_at")
local tokens = tonumber(bucket[1])
local updated_at = tonumber(bucket[2])

if tokens == nil then
	tokens = capacity
	updated_at = now
end

if updated_at == nil then
	updated_at = now
end

if now < updated_at then
	updated_at = now
end

local elapsed = now - updated_at
if elapsed >= refill_interval then
	local refill = math.floor(elapsed / refill_interval)
	tokens = math.min(capacity, tokens + refill)
	updated_at = updated_at + (refill * refill_interval)
	if tokens == capacity then
		updated_at = now
	end
end

local allowed = 0
if tokens >= requested then
	tokens = tokens - requested
	allowed = 1
end

local next_refill = now
if tokens < capacity then
	next_refill = updated_at + refill_interval
	if next_refill < now then
		next_refill = now
	end
end

redis.call("HSET", key, "tokens", tokens, "updated_at", updated_at)
redis.call("PEXPIRE", key, capacity * refill_interval)

return {allowed, capacity, tokens, next_refill, now}
`

var redisTokenBucketScript = redis.NewScript(rateLimitScript)

type rateLimiter struct {
	store          rateLimitStore
	capacity       int
	refillInterval time.Duration
	keyPrefix      string
}

type rateLimitStore interface {
	Allow(ctx context.Context, key string, capacity int, refillInterval time.Duration) (rateLimitDecision, error)
	Close() error
}

type rateLimitDecision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	Reset      time.Time
	RetryAfter time.Duration
}

type redisRateLimitStore struct {
	client *redis.Client
}

func newRateLimiter(cfg config.GatewayRateLimitConfig, redisCfg config.RedisConfig) (*rateLimiter, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	resolved, err := redisCfg.Resolve()
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(resolved.Addr) == "" {
		return nil, errors.New("gateway rate limit requires redis address")
	}

	store := newRedisRateLimitStore(resolved)
	return newRateLimiterWithStore(cfg, store), nil
}

func newRateLimiterWithStore(cfg config.GatewayRateLimitConfig, store rateLimitStore) *rateLimiter {
	return &rateLimiter{
		store:          store,
		capacity:       cfg.Capacity(),
		refillInterval: cfg.RefillIntervalDuration(),
		keyPrefix:      cfg.RedisKeyPrefix(),
	}
}

func newRedisRateLimitStore(cfg config.ResolvedRedisConfig) *redisRateLimitStore {
	var tlsConfig *tls.Config
	if cfg.TLS {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	return &redisRateLimitStore{
		client: redis.NewClient(&redis.Options{
			Addr:      strings.TrimSpace(cfg.Addr),
			Password:  cfg.Password,
			TLSConfig: tlsConfig,
		}),
	}
}

func (s *redisRateLimitStore) Allow(ctx context.Context, key string, capacity int, refillInterval time.Duration) (rateLimitDecision, error) {
	result, err := redisTokenBucketScript.Run(ctx, s.client, []string{key}, capacity, refillInterval.Milliseconds()).Result()
	if err != nil {
		return rateLimitDecision{}, err
	}

	return parseRateLimitResult(result)
}

func (s *redisRateLimitStore) Close() error {
	return s.client.Close()
}

func (l *rateLimiter) Close() error {
	if l == nil || l.store == nil {
		return nil
	}

	return l.store.Close()
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	if l == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		clientID := clientIdentifier(r)
		decision, err := l.store.Allow(r.Context(), l.keyFor(clientID), l.capacity, l.refillInterval)
		if err != nil {
			slog.Error("gateway rate limit check failed", "error", err, "client", clientID)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "rate limiter unavailable"})
			return
		}

		writeRateLimitHeaders(w.Header(), decision)
		if !decision.Allowed {
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(decision.RetryAfter), 10))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (l *rateLimiter) keyFor(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return l.keyPrefix + ":" + hex.EncodeToString(sum[:])
}

func parseRateLimitResult(result any) (rateLimitDecision, error) {
	values, ok := result.([]interface{})
	if !ok {
		return rateLimitDecision{}, fmt.Errorf("unexpected redis rate limit result %T", result)
	}

	if len(values) != 5 {
		return rateLimitDecision{}, fmt.Errorf("unexpected redis rate limit result length %d", len(values))
	}

	allowed, err := int64Value(values[0])
	if err != nil {
		return rateLimitDecision{}, err
	}

	limit, err := int64Value(values[1])
	if err != nil {
		return rateLimitDecision{}, err
	}

	remaining, err := int64Value(values[2])
	if err != nil {
		return rateLimitDecision{}, err
	}

	resetMS, err := int64Value(values[3])
	if err != nil {
		return rateLimitDecision{}, err
	}

	nowMS, err := int64Value(values[4])
	if err != nil {
		return rateLimitDecision{}, err
	}

	retryAfter := time.Duration(resetMS-nowMS) * time.Millisecond
	if retryAfter < 0 {
		retryAfter = 0
	}

	return rateLimitDecision{
		Allowed:    allowed == 1,
		Limit:      int(limit),
		Remaining:  maxInt(int(remaining), 0),
		Reset:      time.UnixMilli(resetMS),
		RetryAfter: retryAfter,
	}, nil
}

func int64Value(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected redis rate limit value %T", value)
	}
}

func writeRateLimitHeaders(header http.Header, decision rateLimitDecision) {
	header.Set("X-RateLimit-Limit", strconv.Itoa(decision.Limit))
	header.Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
	header.Set("X-RateLimit-Reset", strconv.FormatInt(decision.Reset.Unix(), 10))
}

func retryAfterSeconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}

	return int64((duration + time.Second - 1) / time.Second)
}

func clientIdentifier(r *http.Request) string {
	if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		ip := strings.TrimSpace(strings.Split(forwardedFor, ",")[0])
		if ip != "" {
			return ip
		}
	}

	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}

	if strings.TrimSpace(r.RemoteAddr) != "" {
		return r.RemoteAddr
	}

	return "unknown"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}

	return b
}
