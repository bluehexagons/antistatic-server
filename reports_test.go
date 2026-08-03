package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func reportTestHandler(t *testing.T, store *reportStore) http.Handler {
	t.Helper()
	lobby := newTestLobbyHandler()
	application, err := newApplicationHandler(applicationConfig{Store: store}, lobby)
	if err != nil {
		t.Fatal(err)
	}
	return maxBytes(10 * 1024)(application)
}

func postJSON(target, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.10:1234"
	return request
}

func TestCrashAndMetricEndpointsPersistAndDedupe(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := reportTestHandler(t, store)
	crashBody := `{"event_id":"random-event-id-1001","app_version":"1.1.9","platform":"linux","arch":"amd64","reason_code":"segfault","symbols":["game::update"]}`
	firstRequest := postJSON("/1.2.3/reports/crash", crashBody)
	firstRequest.Header.Set("Authorization", "Bearer must-not-be-stored")
	firstRequest.Header.Set("User-Agent", "private-user-agent")
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, firstRequest)
	if first.Code != http.StatusCreated {
		t.Fatalf("crash status = %d: %s", first.Code, first.Body.String())
	}
	var response reportResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ReportID == "" || first.Header().Get(antistaticReportIDHeader) != response.ReportID {
		t.Fatalf("crash response/header = %#v / %q", response, first.Header().Get(antistaticReportIDHeader))
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, postJSON("/1.2.3/reports/crash", crashBody))
	if second.Code != http.StatusCreated || second.Header().Get(antistaticReportIDHeader) != response.ReportID {
		t.Fatalf("duplicate crash = %d / %q, want stable ID %q", second.Code, second.Header().Get(antistaticReportIDHeader), response.ReportID)
	}
	crashes, _ := store.crashes()
	if len(crashes) != 1 || crashes[0].AppVersion != "1.1.9" {
		t.Fatalf("stored crashes = %#v, want originating app version 1.1.9", crashes)
	}
	storedCrash, err := os.ReadFile(store.collectionPath(crashCollection))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"must-not-be-stored", "private-user-agent", "198.51.100.10"} {
		if strings.Contains(string(storedCrash), forbidden) {
			t.Fatalf("stored crash contains transport data %q: %s", forbidden, storedCrash)
		}
	}

	gameplayBody := `{"event_id":"random-event-id-1002","mode":"versus","stage":"arena","character":"carbon","opponent_character":"silicon","online":true,"completed":true,"duration_frames":3600,"local_players":1,"cpu_players":0,"result":"win"}`
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, postJSON("/1.2.3/metrics/gameplay", gameplayBody))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("gameplay status = %d: %s", recorder.Code, recorder.Body.String())
		}
	}
	gameplay, _ := store.gameplay()
	if len(gameplay) != 1 {
		t.Fatalf("stored gameplay records = %d, want deduplicated 1", len(gameplay))
	}

	feedbackBody := `{"event_id":"random-event-id-1003","category":"feedback","subject":"Controller feel","body":"The new timing feels better","related_report_id":"` + response.ReportID + `"}`
	feedbackRecorder := httptest.NewRecorder()
	handler.ServeHTTP(feedbackRecorder, postJSON("/1.2.3/reports/feedback", feedbackBody))
	if feedbackRecorder.Code != http.StatusCreated || feedbackRecorder.Header().Get(antistaticReportIDHeader) == "" {
		t.Fatalf("feedback response = %d / %q: %s", feedbackRecorder.Code, feedbackRecorder.Header().Get(antistaticReportIDHeader), feedbackRecorder.Body.String())
	}
	performanceBody := `{"event_id":"random-event-id-1004","platform":"linux","arch":"amd64","renderer_family":"vulkan","gpu_vendor":"amd","memory_gib_bucket":"16-31","cpu_cores_bucket":"9-16","resolution_bucket":"1440p","sample_frames":600,"frame_ms_avg":8.4,"frame_ms_p95":12.1}`
	performanceRecorder := httptest.NewRecorder()
	handler.ServeHTTP(performanceRecorder, postJSON("/1.2.3/metrics/performance", performanceBody))
	if performanceRecorder.Code != http.StatusNoContent {
		t.Fatalf("performance status = %d: %s", performanceRecorder.Code, performanceRecorder.Body.String())
	}
	performance, _ := store.performance()
	if len(performance) != 1 || performance[0].GPUVendor != "amd" {
		t.Fatalf("stored performance = %#v", performance)
	}
}

func TestReportEndpointsEnforceStrictJSONAndPrivacyBoundary(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := reportTestHandler(t, store)
	valid := `{"event_id":"random-event-id-2001","category":"bug","subject":"A subject","body":"A bounded description"}`
	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"missing content type", "", valid, http.StatusUnsupportedMediaType},
		{"unknown IP field", "application/json", strings.TrimSuffix(valid, "}") + `,"client_ip":"198.51.100.1"}`, http.StatusBadRequest},
		{"unknown timestamp", "application/json", strings.TrimSuffix(valid, "}") + `,"timestamp":"2026-01-01T00:00:00Z"}`, http.StatusBadRequest},
		{"unknown metadata", "application/json", strings.TrimSuffix(valid, "}") + `,"metadata":{"token":"secret"}}`, http.StatusBadRequest},
		{"trailing JSON", "application/json", valid + `{}`, http.StatusBadRequest},
		{"invalid related ID", "application/json", strings.TrimSuffix(valid, "}") + `,"related_report_id":"/tmp/report"}`, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/1.2.3/reports/feedback", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
	missingSymbols := httptest.NewRecorder()
	handler.ServeHTTP(missingSymbols, postJSON("/1.2.3/reports/crash", `{"event_id":"random-event-id-2003","app_version":"1.2.2","platform":"linux","arch":"amd64","reason_code":"segfault"}`))
	if missingSymbols.Code != http.StatusBadRequest {
		t.Fatalf("missing crash symbols status = %d, want 400", missingSymbols.Code)
	}
	for name, body := range map[string]string{
		"missing": `{"event_id":"random-event-id-2005","platform":"linux","arch":"amd64","reason_code":"segfault","symbols":[]}`,
		"invalid": `{"event_id":"random-event-id-2006","app_version":"bad/version","platform":"linux","arch":"amd64","reason_code":"segfault","symbols":[]}`,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, postJSON("/1.2.3/reports/crash", body))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s crash app_version status = %d, want 400", name, recorder.Code)
		}
	}
	missingBooleans := httptest.NewRecorder()
	handler.ServeHTTP(missingBooleans, postJSON("/1.2.3/metrics/gameplay", `{"event_id":"random-event-id-2004","mode":"versus","stage":"arena","character":"carbon","opponent_character":"silicon","duration_frames":10,"local_players":1,"cpu_players":0,"result":"win"}`))
	if missingBooleans.Code != http.StatusBadRequest {
		t.Fatalf("missing gameplay booleans status = %d, want 400", missingBooleans.Code)
	}

	oversized := `{"event_id":"random-event-id-2002","category":"bug","subject":"subject","body":"` + strings.Repeat("x", 11000) + `"}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, postJSON("/1.2.3/reports/feedback", oversized))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413: %s", recorder.Code, recorder.Body.String())
	}
	feedback, _ := store.feedback()
	if len(feedback) != 0 {
		t.Fatalf("invalid requests persisted %d feedback records", len(feedback))
	}
}

func TestReportEndpointValidationAndUnavailableStorage(t *testing.T) {
	unavailable := reportTestHandler(t, nil)
	recorder := httptest.NewRecorder()
	unavailable.ServeHTTP(recorder, postJSON("/1.2.3/reports/crash", `{}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured storage status = %d, want 503", recorder.Code)
	}

	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := reportTestHandler(t, store)
	invalidCrash := `{"event_id":"random-event-id-3001","app_version":"1.2.3","platform":"linux","arch":"amd64","reason_code":"segfault","symbols":["/home/player/crash.log"]}`
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, postJSON("/1.2.3/reports/crash", invalidCrash))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("path-like symbol status = %d, want 400", recorder.Code)
	}
	invalidPerformance := `{"event_id":"random-event-id-3002","platform":"linux","arch":"amd64","renderer_family":"vulkan","gpu_vendor":"nvidia","memory_gib_bucket":"8-15","cpu_cores_bucket":"5-8","resolution_bucket":"1440p","sample_frames":600,"frame_ms_avg":20,"frame_ms_p95":10}`
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, postJSON("/1.2.3/metrics/performance", invalidPerformance))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid percentile status = %d, want 400", recorder.Code)
	}
}
