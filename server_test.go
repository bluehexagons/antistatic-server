package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestLobbyHandler() *lobbyHandler {
	return &lobbyHandler{
		Lobbies: map[string]*Lobby{},
		Tickets: map[string]*MatchmakingTicket{},
		Waiting: map[string]map[string]*MatchmakingTicket{},
		Matches: map[string]*Match{},
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
	h.Metrics.recordSuccessfulGame()
	h.Metrics.recordError()

	resp := h.healthResponse()
	if resp.Status != "ok" || resp.LobbiesCreated != 1 || resp.SuccessfulGamesEstimate != 1 || resp.ErrorCount != 1 {
		t.Fatalf("health response = %#v, want counters to be reported", resp)
	}
}

func TestVersionedLobbyCheckInIgnoresQueryString(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860?debug=1", "198.51.100.10:32000")

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned status %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	response := decodeLobbyResponse(t, rec)

	if response.IP != "198.51.100.10" || response.Port != 45860 {
		t.Fatalf("response endpoint = %s:%d, want 198.51.100.10:45860", response.IP, response.Port)
	}
	if response.Token == "" {
		t.Fatalf("response token was empty")
	}
	if response.Lobby == nil || response.Lobby.Version != "0.9.5" {
		t.Fatalf("response lobby version = %#v, want 0.9.5", response.Lobby)
	}
	if len(response.Lobby.Members) != 1 || response.Lobby.Members[0].IP != "198.51.100.10" {
		t.Fatalf("response members = %#v, want one checked-in member", response.Lobby.Members)
	}
	if response.Lobby.Members[0].Token != "" {
		t.Fatalf("member token leaked in lobby snapshot")
	}
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
	if len(response.Lobby.Members) != 1 || response.Lobby.Members[0].Port != 45860 {
		t.Fatalf("legacy response members = %#v, want one checked-in member", response.Lobby.Members)
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

func TestLobbyAcceptsIPv6RemoteAddress(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveLobbyRequest(h, http.MethodPut, "/0.9.5/lobby/ABC123/45860", "[2001:db8::1]:32000")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	response := decodeLobbyResponse(t, rec)
	if response.IP != "2001:db8::1" || response.Port != 45860 {
		t.Fatalf("response endpoint = %s:%d, want 2001:db8::1:45860", response.IP, response.Port)
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
	if len(h.Lobbies["ABC123"].Members) != 1 {
		t.Fatalf("member was removed by wrong-token delete")
	}
	h.Mu.RUnlock()

	rec = serveLobbyRequestWithToken(h, http.MethodDelete, "/0.9.5/lobby/ABC123/45860", "198.51.100.10:32000", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE with token status = %d, want %d", rec.Code, http.StatusOK)
	}
	h.Mu.RLock()
	_, ok := h.Lobbies["ABC123"]
	h.Mu.RUnlock()
	if ok {
		t.Fatalf("lobby remained after authorized delete")
	}
}

func TestHealthEndpointIncludesMetrics(t *testing.T) {
	handler = newTestLobbyHandler()
	handler.Mu.Lock()
	handler.Lobbies["ABC123"] = &Lobby{Key: "ABC123"}
	handler.Matches["match-1"] = &Match{}
	handler.Mu.Unlock()
	handler.Metrics.recordLobbyCreated()
	handler.Metrics.recordSuccessfulGame()
	handler.Metrics.recordError()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	resp := decodeHealthResponse(t, rec)
	if resp.LobbyCount != 1 || resp.MatchCount != 1 || resp.LobbiesCreated != 1 || resp.SuccessfulGamesEstimate != 1 || resp.ErrorCount != 1 {
		t.Fatalf("health payload = %#v, want counters", resp)
	}
}
