package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type contextKey string

const requestIDKey contextKey = "requestID"
const maxRequestIDLength = 128
const rateLimiterShardCount = 64
const maxRateLimitClients = 65536

var trustedProxyRanges []*net.IPNet

func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !validRequestID(requestID) {
			requestID = generateRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '.', '-', '_', ':':
			continue
		default:
			return false
		}
	}
	return true
}

func getRequestID(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminRequest := r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/")
		if !adminRequest && !isIngestionPath(r.URL.Path) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
			w.Header().Set(
				"Access-Control-Allow-Headers",
				"Content-Type, X-Request-ID, X-Antistatic-Token, X-Antistatic-Match-Self-Tag, X-Antistatic-Match-Peer-Tag, X-Antistatic-Match-Self-Tag-Token",
			)
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, "+antistaticReportIDHeader)
			w.Header().Set("Access-Control-Max-Age", "3600")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if adminRequest {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		} else {
			w.Header().Set("Content-Security-Policy", "default-src 'none'")
		}

		if r.Method == "OPTIONS" && !adminRequest && !isIngestionPath(r.URL.Path) {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isIngestionPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] == "" {
		return false
	}
	return (parts[1] == "reports" && (parts[2] == "crash" || parts[2] == "feedback")) ||
		(parts[1] == "metrics" && (parts[2] == "gameplay" || parts[2] == "performance"))
}

func withTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type rateLimiter struct {
	shards    [rateLimiterShardCount]rateLimiterShard
	rate      float64
	burst     float64
	interval  time.Duration
	metrics   *serverMetrics
	stop      chan struct{}
	stopOnce  sync.Once
	cleanupWG sync.WaitGroup
}

type rateLimiterShard struct {
	mu      sync.Mutex
	clients map[string]*bucket
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func newRateLimiter(rate, burst int, interval time.Duration) *rateLimiter {
	rl := &rateLimiter{
		rate:     float64(rate),
		burst:    float64(burst),
		interval: interval,
		stop:     make(chan struct{}),
	}
	for i := range rl.shards {
		rl.shards[i].clients = make(map[string]*bucket)
	}
	rl.cleanupWG.Go(func() {
		ticker := time.NewTicker(interval * 10)
		defer ticker.Stop()
		for {
			select {
			case <-rl.stop:
				return
			case <-ticker.C:
				rl.cleanup()
			}
		}
	})
	return rl
}

func (rl *rateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.stop)
	})
	rl.cleanupWG.Wait()
}

func (rl *rateLimiter) cleanup() {
	now := time.Now()
	for i := range rl.shards {
		shard := &rl.shards[i]
		shard.mu.Lock()
		for ip, b := range shard.clients {
			if now.Sub(b.lastSeen) > rl.interval*10 {
				delete(shard.clients, ip)
			}
		}
		shard.mu.Unlock()
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	shard := rl.shard(ip)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := time.Now()
	b, exists := shard.clients[ip]
	if !exists {
		if len(shard.clients) >= (maxRateLimitClients+rateLimiterShardCount-1)/rateLimiterShardCount {
			return false
		}
		b = &bucket{
			tokens:   rl.burst - 1,
			lastSeen: now,
		}
		shard.clients[ip] = b
		return true
	}

	elapsed := now.Sub(b.lastSeen)
	b.tokens += elapsed.Seconds() * rl.rate / rl.interval.Seconds()
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

func (rl *rateLimiter) shard(ip string) *rateLimiterShard {
	var h uint32 = 2166136261
	for i := 0; i < len(ip); i++ {
		h ^= uint32(ip[i])
		h *= 16777619
	}
	return &rl.shards[h%rateLimiterShardCount]
}

func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if ip == "" {
			if rl.metrics != nil {
				rl.metrics.recordHTTPError("Unable to determine client IP", http.StatusBadRequest)
			}
			http.Error(w, "Unable to determine client IP", http.StatusBadRequest)
			return
		}
		if !rl.allow(ip) {
			if rl.metrics != nil {
				rl.metrics.recordHTTPError("Rate limit exceeded", http.StatusTooManyRequests)
			}
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setTrustedProxyCIDRs(value string) error {
	if strings.TrimSpace(value) == "" {
		trustedProxyRanges = nil
		return nil
	}

	parts := strings.Split(value, ",")
	ranges := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("parse trusted proxy CIDR %q: %w", cidr, err)
		}
		ranges = append(ranges, network)
	}

	if len(ranges) == 0 {
		return errors.New("trusted proxy CIDR list was empty")
	}

	trustedProxyRanges = ranges
	return nil
}

func remoteIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		if parsed := net.ParseIP(r.RemoteAddr); parsed != nil {
			return parsed.String()
		}
		return ""
	}
	return ip
}

func isTrustedProxy(ip string) bool {
	if len(trustedProxyRanges) == 0 {
		return false
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}

	for _, network := range trustedProxyRanges {
		if network.Contains(parsed) {
			return true
		}
	}
	return false
}

func getClientIP(r *http.Request) string {
	remote := remoteIP(r)
	if remote == "" {
		return ""
	}

	if trustProxy && isTrustedProxy(remote) {
		forwardedValues := r.Header.Values("X-Forwarded-For")
		realValues := r.Header.Values("X-Real-IP")
		forwarded := forwardedClientIP(forwardedValues)
		real := forwardedClientIP(realValues)
		if (len(forwardedValues) > 0 && forwarded == "") || (len(realValues) > 0 && real == "") {
			return ""
		}
		if forwarded != "" && real != "" && real != forwarded {
			return ""
		}
		if forwarded != "" {
			return forwarded
		}
		if real != "" {
			return real
		}

		// The proxy address is not a client identity. Falling back to it would
		// group unrelated users into the same rate-limit and private-endpoint
		// visibility cohort.
		return ""
	}

	return remote
}

func forwardedClientIP(values []string) string {
	if len(values) != 1 {
		return ""
	}
	value := strings.TrimSpace(values[0])
	if value == "" || strings.Contains(value, ",") {
		return ""
	}
	parsed := net.ParseIP(value)
	if parsed == nil {
		return ""
	}
	return parsed.String()
}

func maxBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
