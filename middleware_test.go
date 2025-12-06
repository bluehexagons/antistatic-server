package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		remoteAddr string
		headers    map[string]string
		expected   string
	}{
		{"192.168.1.1:12345", map[string]string{}, "192.168.1.1"},
		{"10.0.0.1:12345", map[string]string{"X-Forwarded-For": "203.0.113.1"}, "203.0.113.1"},
		{"10.0.0.1:12345", map[string]string{"X-Forwarded-For": "203.0.113.1, 198.51.100.1"}, "203.0.113.1"},
		{"10.0.0.1:12345", map[string]string{"X-Real-IP": "203.0.113.5"}, "203.0.113.5"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tt.remoteAddr
		for k, v := range tt.headers {
			req.Header.Set(k, v)
		}
		if got := getClientIP(req); got != tt.expected {
			t.Errorf("getClientIP() = %q, want %q", got, tt.expected)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(2, 2, time.Second)

	if !rl.allow("192.168.1.1") || !rl.allow("192.168.1.1") {
		t.Error("First two requests should be allowed")
	}
	if rl.allow("192.168.1.1") {
		t.Error("Third request should be denied")
	}
	if !rl.allow("192.168.1.2") {
		t.Error("Request from different IP should be allowed")
	}

	time.Sleep(time.Second + 100*time.Millisecond)
	if !rl.allow("192.168.1.1") {
		t.Error("Request should be allowed after refill")
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS header not set")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options header not set")
	}
}

func TestSecurityHeadersOptions(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called for OPTIONS")
	}))

	req := httptest.NewRequest("OPTIONS", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS should return 204, got %d", rec.Code)
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if getRequestID(r) == "" {
			t.Error("Request ID should not be empty")
		}
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header should be set")
	}
}

func TestRequestIDMiddlewareWithExisting(t *testing.T) {
	existingID := "test-id-123"
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if getRequestID(r) != existingID {
			t.Errorf("Request ID = %q, want %q", getRequestID(r), existingID)
		}
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", existingID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") != existingID {
		t.Errorf("X-Request-ID = %q, want %q", rec.Header().Get("X-Request-ID"), existingID)
	}
}
