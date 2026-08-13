package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func adminRequest(method, target, username, password string, secure bool) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.RemoteAddr = "198.51.100.10:1234"
	if secure {
		request.TLS = &tls.ConnectionState{}
	}
	if username != "" || password != "" {
		request.SetBasicAuth(username, password)
	}
	return request
}

func TestAdminRequiresTLSAndConstantTimeCredentials(t *testing.T) {
	admin := securityHeaders(newAdminHandler(nil, "operator", "correct horse battery staple", "Antistatic", DefaultConfig().Features))
	recorder := httptest.NewRecorder()
	admin.ServeHTTP(recorder, adminRequest(http.MethodGet, "/admin/", "operator", "correct horse battery staple", false))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("insecure admin status = %d, want 400", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	admin.ServeHTTP(recorder, adminRequest(http.MethodGet, "/admin/", "operator", "wrong", true))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("bad auth response = %d, challenge %q", recorder.Code, recorder.Header().Get("WWW-Authenticate"))
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("admin privacy headers = cache %q referrer %q", recorder.Header().Get("Cache-Control"), recorder.Header().Get("Referrer-Policy"))
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("admin response has wildcard CORS: %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "style-src 'self'") {
		t.Fatalf("admin CSP = %q", recorder.Header().Get("Content-Security-Policy"))
	}

	recorder = httptest.NewRecorder()
	admin.ServeHTTP(recorder, adminRequest(http.MethodGet, "/admin/", "operator", "correct horse battery staple", true))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "storage is unavailable") {
		t.Fatalf("authenticated unavailable admin = %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	admin.ServeHTTP(recorder, adminRequest(http.MethodGet, "/admin/style.css", "", "", true))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stylesheet status = %d, want 401", recorder.Code)
	}
}

func TestAdminRegistersOnlyEnabledReportSections(t *testing.T) {
	features := FeatureConfig{CrashReports: true}
	store, err := newReportStoreForFeatures(t.TempDir(), features)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	admin := newAdminHandler(store, "operator", "password", "Another Game", features)

	overview := httptest.NewRecorder()
	admin.ServeHTTP(overview, adminRequest(http.MethodGet, "/admin/", "operator", "password", true))
	if overview.Code != http.StatusOK {
		t.Fatalf("overview status = %d: %s", overview.Code, overview.Body.String())
	}
	body := overview.Body.String()
	if !strings.Contains(body, "/admin/crash") || strings.Contains(body, "/admin/feedback") || strings.Contains(body, "/admin/gameplay") {
		t.Fatalf("overview did not match enabled report sections: %s", body)
	}

	disabled := httptest.NewRecorder()
	admin.ServeHTTP(disabled, adminRequest(http.MethodGet, "/admin/feedback", "operator", "password", true))
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled admin section status = %d, want 404", disabled.Code)
	}
}

func TestAdminEscapesStoredFeedback(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := store.appendFeedback(feedbackRequest{
		EventID: "random-event-id-4001", Category: "feedback", Subject: `<script>alert("subject")</script>`, Body: `<img src=x onerror=alert("body")>`,
	}, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	admin := newAdminHandler(store, "operator", "password", "Antistatic", DefaultConfig().Features)
	recorder := httptest.NewRecorder()
	admin.ServeHTTP(recorder, adminRequest(http.MethodGet, "/admin/feedback/"+id, "operator", "password", true))
	if recorder.Code != http.StatusOK {
		t.Fatalf("feedback detail status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "<img src=x") {
		t.Fatalf("admin rendered unescaped feedback: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "&lt;img") {
		t.Fatalf("admin did not render escaped feedback: %s", body)
	}
}

func TestAdminAcceptsForwardedHTTPSOnlyFromTrustedProxy(t *testing.T) {
	previousTrustProxy := trustProxy
	previousRanges := trustedProxyRanges
	defer func() {
		trustProxy = previousTrustProxy
		trustedProxyRanges = previousRanges
	}()
	trustProxy = true
	if err := setTrustedProxyCIDRs("10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	admin := newAdminHandler(nil, "operator", "password", "Antistatic", DefaultConfig().Features)
	for _, test := range []struct {
		remote string
		want   int
	}{
		{"10.1.2.3:1234", http.StatusOK},
		{"198.51.100.10:1234", http.StatusBadRequest},
	} {
		request := adminRequest(http.MethodGet, "/admin/", "operator", "password", false)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-Proto", "https")
		recorder := httptest.NewRecorder()
		admin.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("remote %s status = %d, want %d", test.remote, recorder.Code, test.want)
		}
	}
}

func TestAdminCategoryReadsAndStorageErrors(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	crashID, _, err := store.appendCrash(validCrashRequest("random-admin-crash-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.collectionPath(feedbackCollection)); err != nil {
		t.Fatal(err)
	}
	admin := newAdminHandler(store, "operator", "password", "Antistatic", DefaultConfig().Features)
	for _, target := range []string{"/admin/crash", "/admin/crash/" + crashID} {
		recorder := httptest.NewRecorder()
		admin.ServeHTTP(recorder, adminRequest(http.MethodGet, target, "operator", "password", true))
		if recorder.Code != http.StatusOK {
			t.Fatalf("category-local read %s status = %d: %s", target, recorder.Code, recorder.Body.String())
		}
	}
	for _, target := range []string{"/admin/", "/admin/feedback"} {
		recorder := httptest.NewRecorder()
		admin.ServeHTTP(recorder, adminRequest(http.MethodGet, target, "operator", "password", true))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "storage unavailable") {
			t.Fatalf("storage error %s response = %d: %s", target, recorder.Code, recorder.Body.String())
		}
	}
}

func TestAdminBoundsRenderedRows(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstID := ""
	lastID := ""
	for i := range maxAdminRows + 1 {
		id, _, err := store.appendCrash(validCrashRequest(fmt.Sprintf("random-admin-event-%04d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstID = id
		}
		lastID = id
	}
	admin := newAdminHandler(store, "operator", "password", "Antistatic", DefaultConfig().Features)
	recorder := httptest.NewRecorder()
	admin.ServeHTTP(recorder, adminRequest(http.MethodGet, "/admin/crash", "operator", "password", true))
	if recorder.Code != http.StatusOK {
		t.Fatalf("crash list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, firstID) || !strings.Contains(body, lastID) {
		t.Fatalf("bounded crash list did not retain latest rows: first=%q last=%q", firstID, lastID)
	}
	if rows := strings.Count(body, "<tr>") - 1; rows != maxAdminRows {
		t.Fatalf("rendered crash rows = %d, want %d", rows, maxAdminRows)
	}
	overview := httptest.NewRecorder()
	admin.ServeHTTP(overview, adminRequest(http.MethodGet, "/admin/", "operator", "password", true))
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), "Crash reports</strong><br>501") {
		t.Fatalf("overview did not report full streaming count: %d %s", overview.Code, overview.Body.String())
	}
}

func TestAdminRendersGameplayPerformanceAndNetplayRecords(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	online, completed := true, true
	if _, err := store.appendGameplay(gameplayRequest{
		EventID: "random-admin-gameplay-0001", Mode: "versus", Stage: "ruins",
		Character: "carbon", OpponentCharacter: "silicon", Online: &online,
		Completed: &completed, DurationFrames: 3600, LocalPlayers: 1, Result: "win",
	}, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendPerformance(performanceRequest{
		EventID: "random-admin-performance-01", Platform: "linux", Arch: "x86_64",
		RendererFamily: "vulkan", GPUVendor: "amd", MemoryGiBBucket: "16-31",
		CPUCoresBucket: "5-8", ResolutionBucket: "1440p", SampleFrames: 600,
		FrameMsAvg: 8.2, FrameMsP95: 11.4,
	}, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := store.appendNetplay(netplayRecord{ID: "nr-0123456789abcdef", AppVersion: "1.2.3", Event: "match_connected"}); err != nil {
		t.Fatal(err)
	}

	admin := newAdminHandler(store, "operator", "password", "Antistatic", DefaultConfig().Features)
	for _, test := range []struct {
		target string
		want   string
	}{
		{target: "/admin/gameplay", want: "carbon / silicon"},
		{target: "/admin/performance", want: "vulkan / amd"},
		{target: "/admin/netplay", want: "match_connected"},
	} {
		recorder := httptest.NewRecorder()
		admin.ServeHTTP(recorder, adminRequest(http.MethodGet, test.target, "operator", "password", true))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), test.want) {
			t.Fatalf("%s response = %d, missing %q: %s", test.target, recorder.Code, test.want, recorder.Body.String())
		}
	}
}
