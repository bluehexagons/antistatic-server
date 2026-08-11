package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newTestLobbyHandler() *lobbyHandler {
	return &lobbyHandler{
		Lobbies:   map[string]*Lobby{},
		Tickets:   map[string]*MatchmakingTicket{},
		Waiting:   map[string]map[string]*MatchmakingTicket{},
		Matches:   map[string]*Match{},
		Queues:    map[string]*MatchmakingQueue{},
		TagLeases: map[string]*MatchmakingTagLease{},
	}
}

func serveLobbyRequest(h *lobbyHandler, method, target, remoteAddr string) *httptest.ResponseRecorder {
	return serveLobbyRequestWithToken(h, method, target, remoteAddr, "")
}

func serveLobbyRequestWithToken(h *lobbyHandler, method, target, remoteAddr string, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set(antistaticTokenHeader, token)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	return rec
}

func serveLobbyRequestWithBody(h *lobbyHandler, method, target, remoteAddr, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set(antistaticTokenHeader, token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	return rec
}

func decodeLobbyResponse(t *testing.T, rec *httptest.ResponseRecorder) lobbyResponse {
	t.Helper()

	var response lobbyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unable to decode response: %v; body=%q", err, rec.Body.String())
	}

	return response
}

func decodeHealthResponse(t *testing.T, rec *httptest.ResponseRecorder) healthResponse {
	t.Helper()

	var response healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unable to decode health response: %v; body=%q", err, rec.Body.String())
	}

	return response
}

func TestHealthReportsStartupMetrics(t *testing.T) {
	h := newTestLobbyHandler()
	h.Metrics.recordLobbyCreated()
	h.Metrics.recordSuccessfulMatch()
	h.Metrics.recordError("test error", http.StatusInternalServerError)
	h.Metrics.recordError("Invalid path", http.StatusNotFound)

	resp := h.healthResponse()
	if resp.Status != "ok" || resp.LobbiesCreated != 1 || resp.SuccessfulMatches != 1 || resp.GameErrorCount != 1 || resp.HTTPErrorCount != 1 {
		t.Fatalf("health response = %#v, want counters to be reported", resp)
	}
	if len(resp.RecentGameErrors) != 1 || resp.RecentGameErrors[0].Code != "test_error" {
		t.Fatalf("game errors = %#v, want one anonymized game error", resp.RecentGameErrors)
	}
	if len(resp.RecentHTTPErrors) != 1 || resp.RecentHTTPErrors[0].Code != "invalid_path" {
		t.Fatalf("HTTP errors = %#v, want one anonymized HTTP error", resp.RecentHTTPErrors)
	}
}

func TestNormalizeMetricCode(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "lowercase and collapse separators", message: "  Request REJECTED: invalid/path!  ", want: "request_rejected_invalid_path"},
		{name: "empty", message: "!@#$", want: ""},
		{name: "bounded", message: strings.Repeat("A", 80), want: strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeMetricCode(tt.message); got != tt.want {
				t.Fatalf("normalizeMetricCode(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}

func TestHealthActivityAggregatesAndSuppressesSmallBuckets(t *testing.T) {
	h := newTestLobbyHandler()
	now := time.Date(2026, time.July, 15, 12, 30, 0, 0, time.UTC)
	for i := range 2 {
		h.Metrics.recordMatchmakingAttempt(now.Add(-time.Duration(i) * 24 * time.Hour))
	}
	for i := range 3 {
		h.Metrics.recordMatchmakingAttempt(now.Add(-time.Duration(i) * 24 * time.Hour).Add(-2 * time.Hour))
	}
	h.Metrics.recordMatchmakingMatch(now.Add(-2*time.Hour), 4*time.Second)

	activity := h.Metrics.snapshotActivity(now)
	if activity.WindowDays != activityWindowDays || activity.Timezone != "UTC" || len(activity.Hours) != 24 {
		t.Fatalf("activity summary = %#v, want 14-day UTC hourly summary", activity)
	}
	if !activity.Hours[12].Suppressed || activity.Hours[12].Attempts != 0 {
		t.Fatalf("small activity bucket = %#v, want suppressed", activity.Hours[12])
	}
	if activity.Hours[10].Suppressed || activity.Hours[10].Attempts != 3 || activity.Hours[10].Matches != 1 || activity.Hours[10].AverageMatchWaitMs != 4000 {
		t.Fatalf("activity bucket = %#v, want aggregate counts and wait", activity.Hours[10])
	}
}

func TestVersionedLobbyCheckInIgnoresQueryString(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860?debug=1", "198.51.100.10:32000")

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned status %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	response := decodeLobbyResponse(t, rec)

	if response.Endpoint.IP != "198.51.100.10" || response.Endpoint.Port != 45860 {
		t.Fatalf("response endpoint = %s:%d, want 198.51.100.10:45860", response.Endpoint.IP, response.Endpoint.Port)
	}
	if response.Token == "" {
		t.Fatalf("response token was empty")
	}
	if response.Lobby == nil || response.Lobby.Version != "0.9.5" {
		t.Fatalf("response lobby version = %#v, want 0.9.5", response.Lobby)
	}
	if len(response.Lobby.Members) != 1 || len(response.Lobby.Members[0].Endpoints) != 1 || response.Lobby.Members[0].Endpoints[0].IP != "198.51.100.10" {
		t.Fatalf("response members = %#v, want one checked-in member", response.Lobby.Members)
	}
	// MemberView intentionally lacks a Token field; the leak this test used
	// to guard against is now structurally impossible.
}

func TestLegacyLobbyRoute(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/lobby/ABC123/45860", "198.51.100.20:32000")

	if rec.Code != http.StatusOK {
		t.Fatalf("legacy PUT returned status %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	response := decodeLobbyResponse(t, rec)

	if response.Lobby == nil || response.Lobby.Version != "" {
		t.Fatalf("legacy lobby version = %#v, want empty version", response.Lobby)
	}
	if len(response.Lobby.Members) != 1 || len(response.Lobby.Members[0].Endpoints) != 1 || response.Lobby.Members[0].Endpoints[0].Port != 45860 {
		t.Fatalf("legacy response members = %#v, want one checked-in member", response.Lobby.Members)
	}
}

func TestLobbyCodeIsIsolatedByVersion(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000")
	second := serveLobbyRequest(h, http.MethodPut, "/0.9.6/lobby/ABC123/45861", "198.51.100.20:32001")
	legacy := serveLobbyRequest(h, http.MethodPut, "/lobby/ABC123/45862", "198.51.100.30:32002")

	for label, rec := range map[string]*httptest.ResponseRecorder{"first": first, "second": second, "legacy": legacy} {
		if rec.Code != http.StatusOK {
			t.Fatalf("%s PUT returned status %d: %s", label, rec.Code, rec.Body.String())
		}
		response := decodeLobbyResponse(t, rec)
		if len(response.Lobby.Members) != 1 {
			t.Fatalf("%s lobby members = %#v, want one version-local member", label, response.Lobby.Members)
		}
	}

	if got := len(h.Lobbies); got != 3 {
		t.Fatalf("stored lobbies = %d, want one per route version", got)
	}
}

func TestLobbyRejectsUnexpectedPathSegments(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860/extra", "198.51.100.10:32000")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestLobbyRejectsUnsupportedMethod(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPost, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestLobbyRejectsInvalidVersion(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/bad!version/lobby/ABC123/45860", "198.51.100.10:32000")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestLobbyRejectsOverlongPath(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/"+strings.Repeat("A", maxPathLength)+"/45860", "198.51.100.10:32000")

	if rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestURITooLong)
	}
}

// TestLobbyMergesDualFamilyCheckIns covers the two-family enrollment flow:
// the same client posts once over IPv4 and once over IPv6 with the same
// lobby key + token. The server must merge them into a single member with
// two endpoints rather than treating them as two separate members.
func TestLobbyMergesDualFamilyCheckIns(t *testing.T) {
	h := newTestLobbyHandler()

	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000")
	if rec.Code != http.StatusOK {
		t.Fatalf("v4 PUT status = %d", rec.Code)
	}
	token := decodeLobbyResponse(t, rec).Token
	if token == "" {
		t.Fatalf("missing token from first PUT")
	}

	rec = serveLobbyRequestWithToken(h, http.MethodPut, "/0.9.5/lobby/ABC123/45861", "[2001:db8::1]:32001", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("v6 PUT status = %d: %s", rec.Code, rec.Body.String())
	}

	resp := decodeLobbyResponse(t, rec)
	if len(resp.Lobby.Members) != 1 {
		t.Fatalf("members = %d, want 1 merged member", len(resp.Lobby.Members))
	}
	endpoints := resp.Lobby.Members[0].Endpoints
	if len(endpoints) != 2 {
		t.Fatalf("endpoints = %#v, want both v4 + v6 endpoints", endpoints)
	}
	gotV4 := false
	gotV6 := false
	for _, e := range endpoints {
		if e.IP == "198.51.100.10" && e.Port == 45860 {
			gotV4 = true
		}
		if e.IP == "2001:db8::1" && e.Port == 45861 {
			gotV6 = true
		}
	}
	if !gotV4 || !gotV6 {
		t.Fatalf("endpoints = %#v, want v4 (198.51.100.10:45860) and v6 (2001:db8::1:45861)", endpoints)
	}
}

// TestLobbyReplacesEndpointInSameFamilyOnRefresh checks that a port-mapping
// shift on one family (common with carrier-grade NATs) updates that family's
// endpoint without disturbing the other family's recorded endpoint.
func TestLobbyReplacesEndpointInSameFamilyOnRefresh(t *testing.T) {
	h := newTestLobbyHandler()

	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000")
	token := decodeLobbyResponse(t, rec).Token
	_ = serveLobbyRequestWithToken(h, http.MethodPut, "/0.9.5/lobby/ABC123/45861", "[2001:db8::1]:32001", token)

	// v4 port shifts; v6 stays.
	rec = serveLobbyRequestWithToken(h, http.MethodPut, "/0.9.5/lobby/ABC123/45870", "198.51.100.10:32000", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("v4 re-PUT status = %d", rec.Code)
	}
	resp := decodeLobbyResponse(t, rec)
	if len(resp.Lobby.Members) != 1 || len(resp.Lobby.Members[0].Endpoints) != 2 {
		t.Fatalf("endpoints after v4 port shift = %#v, want still two", resp.Lobby.Members[0].Endpoints)
	}
	for _, e := range resp.Lobby.Members[0].Endpoints {
		if ipFamily(e.IP) == 4 && e.Port != 45870 {
			t.Fatalf("v4 endpoint did not update after port shift: %#v", resp.Lobby.Members[0].Endpoints)
		}
		if ipFamily(e.IP) == 6 && e.Port != 45861 {
			t.Fatalf("v6 endpoint changed when only v4 should have: %#v", resp.Lobby.Members[0].Endpoints)
		}
	}
}

func TestLobbyAcceptsIPv6RemoteAddress(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "[2001:db8::1]:32000")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	response := decodeLobbyResponse(t, rec)
	if response.Endpoint.IP != "2001:db8::1" || response.Endpoint.Port != 45860 {
		t.Fatalf("response endpoint = %s:%d, want 2001:db8::1:45860", response.Endpoint.IP, response.Endpoint.Port)
	}
}

func TestLobbyRefreshRequiresMemberToken(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000")
	token := decodeLobbyResponse(t, first).Token

	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("refresh without token status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = serveLobbyRequestWithToken(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh with token status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestLobbyDeleteRequiresMemberToken(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000")
	token := decodeLobbyResponse(t, first).Token

	rec := serveLobbyRequestWithToken(h, http.MethodDelete, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000", "wrong-token")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("DELETE with wrong token status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	h.Mu.RLock()
	if len(h.Lobbies[lobbyStorageKey("0.9.5", "ABC123")].Members) != 1 {
		t.Fatalf("member was removed by wrong-token delete")
	}
	h.Mu.RUnlock()

	rec = serveLobbyRequestWithToken(h, http.MethodDelete, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE with token status = %d, want %d", rec.Code, http.StatusOK)
	}
	h.Mu.RLock()
	_, ok := h.Lobbies[lobbyStorageKey("0.9.5", "ABC123")]
	h.Mu.RUnlock()
	if ok {
		t.Fatalf("lobby remained after authorized delete")
	}
}

func TestLobbyDeleteUsesTokenAcrossAddressFamilies(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "[2001:db8::1]:32000")
	token := decodeLobbyResponse(t, first).Token

	// The HTTP route used for cleanup need not match any UDP endpoint stored
	// on the member. This happens when a dual-stack client checks in over one
	// family but DNS selects the other family for its DELETE.
	rec := serveLobbyRequestWithToken(
		h,
		http.MethodDelete,
		"/0.9.5/lobby/ABC123/45860",
		"198.51.100.10:32000",
		token,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-family DELETE with token status = %d, want %d", rec.Code, http.StatusOK)
	}

	h.Mu.RLock()
	_, ok := h.Lobbies[lobbyStorageKey("0.9.5", "ABC123")]
	h.Mu.RUnlock()
	if ok {
		t.Fatalf("lobby remained after cross-family authorized delete")
	}
}

func TestHealthEndpointIncludesMetrics(t *testing.T) {
	handler = newTestLobbyHandler()
	handler.Mu.Lock()
	handler.Lobbies["ABC123"] = &Lobby{Key: "ABC123"}
	handler.Matches["match-1"] = &Match{}
	handler.Mu.Unlock()
	handler.Metrics.recordLobbyCreated()
	handler.Metrics.recordSuccessfulMatch()
	handler.Metrics.recordError("test error", http.StatusBadRequest)
	handler.Metrics.recordGameErrorWithReportID("match failed", "nr-0123456789abcdef")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	resp := decodeHealthResponse(t, rec)
	if resp.LobbyCount != 1 || resp.MatchCount != 1 || resp.LobbiesCreated != 1 || resp.SuccessfulMatches != 1 || resp.ClientErrorCount != 1 {
		t.Fatalf("health payload = %#v, want counters", resp)
	}
	if resp.Version != serverVersion {
		t.Fatalf("health version = %q, want %q", resp.Version, serverVersion)
	}
	if strings.Contains(rec.Body.String(), "nr-0123456789abcdef") {
		t.Fatal("public health JSON must not expose report IDs")
	}
}

func TestHealthHTMLEndpoint(t *testing.T) {
	previous := handler
	defer func() { handler = previous }()
	handler = newTestLobbyHandler()
	handler.Metrics.recordHTTPError("Invalid path", http.StatusNotFound)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health.html", nil)
	healthHTMLHandler(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("HTML health response = status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{"Antistatic server health", "Queue activity", "Recent HTTP errors", "invalid_path"} {
		if !strings.Contains(body, want) {
			t.Fatalf("HTML health body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<script") {
		t.Fatal("HTML health view should not contain scripts")
	}
}

func TestNormalizeServerVersion(t *testing.T) {
	if got := normalizeServerVersion("v0.9.0"); got != "0.9.0" {
		t.Fatalf("normalizeServerVersion(v0.9.0) = %q, want 0.9.0", got)
	}
	if got := normalizeServerVersion("(devel)"); got != "(devel)" {
		t.Fatalf("normalizeServerVersion((devel)) = %q, want (devel)", got)
	}
}

// TestLobbyLocalIPsRevealedOnlyToSamePublicIP verifies that LAN candidate
// addresses published via the PUT body are echoed back to peers behind the
// same public IP (so they can hairpin via LAN), but withheld from peers on
// other public IPs. This is the privacy contract for the same-NAT tunneling
// fallback.
func TestLobbyLocalIPsRevealedOnlyToSamePublicIP(t *testing.T) {
	h := newTestLobbyHandler()

	body := `{"local_ips":["192.168.1.5","10.0.0.5"],"local_endpoints":[{"ip":"192.168.1.5","port":45860},{"ip":"10.0.0.99","port":45860},{"ip":"127.0.0.1","port":45860}]}`
	rec := serveLobbyRequestWithBody(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "203.0.113.5:32000", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("first PUT status = %d: %s", rec.Code, rec.Body.String())
	}

	// Second peer behind same NAT (same public IP) should see the LAN IPs.
	rec = serveLobbyRequestWithBody(h, http.MethodPut, "/0.9.5/lobby/ABC123/45861", "203.0.113.5:32001", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("same-NAT PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeLobbyResponse(t, rec)
	var firstMember *MemberView
	for i := range resp.Lobby.Members {
		eps := resp.Lobby.Members[i].Endpoints
		if len(eps) > 0 && eps[0].Port == 45860 {
			firstMember = &resp.Lobby.Members[i]
		}
	}
	if firstMember == nil {
		t.Fatalf("did not find member with port 45860 in response: %#v", resp.Lobby.Members)
	}
	if len(firstMember.LocalIPs) != 2 {
		t.Fatalf("same-NAT peer saw local_ips = %v, want both LAN addresses", firstMember.LocalIPs)
	}
	wantLocalEndpoints := []Endpoint{{IP: "192.168.1.5", Port: 45860}, {IP: "127.0.0.1", Port: 45860}}
	if !reflect.DeepEqual(firstMember.LocalEndpoints, wantLocalEndpoints) {
		t.Fatalf("same-NAT peer saw local_endpoints = %v, want %v", firstMember.LocalEndpoints, wantLocalEndpoints)
	}

	// Third peer on a different public IP must not see the LAN IPs.
	rec = serveLobbyRequestWithBody(h, http.MethodPut, "/0.9.5/lobby/ABC123/45862", "198.51.100.10:32002", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stranger PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	resp = decodeLobbyResponse(t, rec)
	for _, m := range resp.Lobby.Members {
		if len(m.Endpoints) > 0 && m.Endpoints[0].IP == "203.0.113.5" && (m.LocalIPs != nil || m.LocalEndpoints != nil) {
			t.Fatalf("stranger received local data %v / %v from a peer behind a different NAT", m.LocalIPs, m.LocalEndpoints)
		}
	}
}

// TestLobbyRejectsMalformedCheckInBody guards against a malformed JSON body
// being silently accepted. Empty body remains valid (older clients send none).
func TestLobbyRejectsMalformedCheckInBody(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequestWithBody(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000", "", `{"local_ips":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = serveLobbyRequestWithBody(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000", "", `{} {}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestLobbyDropsPublicLocalIPs verifies that a hostile or buggy client cannot
// have public IPs propagated through the lobby; sanitizeLocalIPs filters them
// out before storage.
func TestLobbyDropsPublicLocalIPs(t *testing.T) {
	h := newTestLobbyHandler()
	body := `{"local_ips":["8.8.8.8","192.168.5.10"]}`
	rec := serveLobbyRequestWithBody(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "203.0.113.5:32000", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", rec.Code)
	}

	rec = serveLobbyRequestWithBody(h, http.MethodPut, "/0.9.5/lobby/ABC123/45861", "203.0.113.5:32001", "", "")
	resp := decodeLobbyResponse(t, rec)
	for _, m := range resp.Lobby.Members {
		if len(m.Endpoints) == 0 || m.Endpoints[0].Port != 45860 {
			continue
		}
		for _, ip := range m.LocalIPs {
			if ip == "8.8.8.8" {
				t.Fatalf("public IP leaked through local_ips: %v", m.LocalIPs)
			}
		}
	}
}
