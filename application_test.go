package main

import (
	"net/http"
	"net/http/httptest"
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
	recorder := httptest.NewRecorder()
	application.ServeHTTP(recorder, postJSON("/1.2.3/reports/crash", `{"event_id":"random-event-id-5001","app_version":"1.2.2","platform":"linux","arch":"amd64","reason_code":"segfault","symbols":[]}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("explicit report route reached fallback: status %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	application.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/1.2.3/reports/crash", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want 405", recorder.Code)
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
	if _, err := applicationConfigFromEnv(); err == nil {
		t.Fatal("one-sided environment admin configuration was accepted")
	}
	t.Setenv("ANTISTATIC_ADMIN_PASSWORD", "password")
	config, err := applicationConfigFromEnv()
	if err != nil || config.AdminUsername != "operator" || config.AdminPassword != "password" || config.Store != nil {
		t.Fatalf("environment config = %#v, %v", config, err)
	}
}
