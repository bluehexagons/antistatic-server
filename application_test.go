package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplicationRouteAssembly(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lobby := newTestLobbyHandler()
	application, err := newApplicationHandler(applicationConfig{Store: store}, lobby)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)
	recorder := httptest.NewRecorder()
	application.ServeHTTP(recorder, postJSON("/1.2.3/reports/crash", `{"event_id":"random-event-id-5001","app_version":"1.2.2","platform":"linux","arch":"amd64","reason_code":"segfault","symbols":[]}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("explicit report route reached fallback: status %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	application.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, apiPrefix+"/reports/crash", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want 405", recorder.Code)
	}
}

func TestApplicationFeatureSwitchesAndServiceIdentity(t *testing.T) {
	config := DefaultConfig()
	config.Service.Name = "Another Game"
	config.Features.Events = false
	config.Features.CrashReports = false
	application, err := newApplicationHandler(applicationConfig{Game: config}, newTestLobbyHandler())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)

	for _, path := range []string{"/events", apiPrefix + "/reports/crash"} {
		recorder := httptest.NewRecorder()
		application.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("disabled route %s status = %d, want 404", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	application.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health.html", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Another Game server health") {
		t.Fatalf("configured health identity missing: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestApplicationCloseStopsReportLimiter(t *testing.T) {
	application, err := newApplicationHandler(applicationConfig{}, newTestLobbyHandler())
	if err != nil {
		t.Fatal(err)
	}
	application.Close()
	application.Close()

	select {
	case <-application.reportLimiter.stop:
	default:
		t.Fatal("application close did not stop the report limiter")
	}
}

func TestListenAddressSupportsIPv6(t *testing.T) {
	if got := listenAddress("::1", 8443); got != "[::1]:8443" {
		t.Fatalf("listenAddress() = %q, want %q", got, "[::1]:8443")
	}
	if got := listenAddress("", 8080); got != ":8080" {
		t.Fatalf("listenAddress() = %q, want %q", got, ":8080")
	}
}

func TestValidateServerPorts(t *testing.T) {
	if err := validateServerPorts(80, 443, 3478); err != nil {
		t.Fatalf("validateServerPorts() = %v for valid ports", err)
	}
	for _, ports := range [][3]int{{-1, 443, 3478}, {80, 65536, 3478}, {80, 443, -1}} {
		if err := validateServerPorts(ports[0], ports[1], ports[2]); err == nil {
			t.Fatalf("validateServerPorts(%v) = nil, want validation error", ports)
		}
	}
}

func TestApplicationAdminConfiguration(t *testing.T) {
	if _, err := newApplicationHandler(applicationConfig{AdminUsername: "operator"}, newTestLobbyHandler()); err == nil {
		t.Fatal("one-sided admin configuration was accepted")
	}
	application, err := newApplicationHandler(applicationConfig{}, newTestLobbyHandler())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)
	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	request.RemoteAddr = "198.51.100.10:1234"
	recorder := httptest.NewRecorder()
	application.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unconfigured admin status = %d, want 404", recorder.Code)
	}
}

func TestApplicationConfigFromEnvironment(t *testing.T) {
	t.Setenv("ANTISTATIC_DATA_DIR", "")
	t.Setenv("ANTISTATIC_ADMIN_USERNAME", "operator")
	t.Setenv("ANTISTATIC_ADMIN_PASSWORD", "")
	if _, err := applicationConfigFromEnv(DefaultConfig()); err == nil {
		t.Fatal("one-sided environment admin configuration was accepted")
	}
	t.Setenv("ANTISTATIC_ADMIN_PASSWORD", "password")
	config, err := applicationConfigFromEnv(DefaultConfig())
	if err != nil || config.AdminUsername != "operator" || config.AdminPassword != "password" || config.Store != nil {
		t.Fatalf("environment config = %#v, %v", config, err)
	}
}
