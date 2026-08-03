package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetClientIP(t *testing.T) {
	trustProxy = true
	if err := setTrustedProxyCIDRs("10.0.0.0/8,127.0.0.1/32"); err != nil {
		t.Fatalf("setTrustedProxyCIDRs() error = %v", err)
	}
	defer func() {
		trustProxy = false
		trustedProxyRanges = nil
	}()

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

func TestGetClientIPIgnoresForwardedHeadersFromUntrustedProxy(t *testing.T) {
	trustProxy = true
	if err := setTrustedProxyCIDRs("10.0.0.0/8"); err != nil {
		t.Fatalf("setTrustedProxyCIDRs() error = %v", err)
	}
	defer func() {
		trustProxy = false
		trustedProxyRanges = nil
	}()

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")

	if got := getClientIP(req); got != "198.51.100.10" {
		t.Fatalf("getClientIP() = %q, want %q", got, "198.51.100.10")
	}
}

func TestGetClientIPFallsBackWhenTrustedProxyHasNoForwardedHeaders(t *testing.T) {
	trustProxy = true
	if err := setTrustedProxyCIDRs("10.0.0.0/8"); err != nil {
		t.Fatalf("setTrustedProxyCIDRs() error = %v", err)
	}
	defer func() {
		trustProxy = false
		trustedProxyRanges = nil
	}()

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:12345"

	if got := getClientIP(req); got != "10.1.2.3" {
		t.Fatalf("getClientIP() = %q, want %q", got, "10.1.2.3")
	}
}

func TestSetTrustedProxyCIDRsRejectsInvalidCIDR(t *testing.T) {
	defer func() { trustedProxyRanges = nil }()

	if err := setTrustedProxyCIDRs("not-a-cidr"); err == nil {
		t.Fatal("setTrustedProxyCIDRs() error = nil, want invalid CIDR error")
	}
}

func TestGetClientIPRejectsMalformedRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "not-an-ip"

	if got := getClientIP(req); got != "" {
		t.Fatalf("getClientIP() = %q, want empty string", got)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(2, 2, time.Second)
	defer rl.Stop()

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
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, antistaticTokenHeader) {
		t.Errorf("Access-Control-Allow-Headers = %q, want %s", got, antistaticTokenHeader)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Access-Control-Allow-Methods = %q, want POST", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, antistaticReportIDHeader) {
		t.Errorf("Access-Control-Expose-Headers = %q, want %s", got, antistaticReportIDHeader)
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

func TestSecurityHeadersOmitCORSForIngestionRoutes(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{
		"/1.0/reports/crash",
		"/1.0/reports/feedback",
		"/1.0/metrics/gameplay",
		"/1.0/metrics/performance",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Header().Get("Access-Control-Allow-Origin") != "" || recorder.Header().Get("Access-Control-Expose-Headers") != "" {
			t.Fatalf("ingestion route %s exposed CORS headers: %#v", path, recorder.Header())
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/1.0/lobby/ABC/1234", nil))
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("lobby CORS origin = %q, want wildcard preserved", recorder.Header().Get("Access-Control-Allow-Origin"))
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

func TestRequestIDMiddlewareRejectsUnsafeExistingID(t *testing.T) {
	existingID := "bad\nid"
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if getRequestID(r) == existingID {
			t.Error("unsafe request ID should be replaced")
		}
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", existingID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") == existingID {
		t.Error("unsafe X-Request-ID header should be replaced")
	}
}
