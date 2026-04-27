package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestLobbyHandler() *lobbyHandler {
	return &lobbyHandler{
		Lobbies: map[string]*Lobby{},
	}
}

func serveLobbyRequest(h *lobbyHandler, method, target, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remoteAddr
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
	if response.Lobby == nil || response.Lobby.Version != "0.9.5" {
		t.Fatalf("response lobby version = %#v, want 0.9.5", response.Lobby)
	}
	if len(response.Lobby.Members) != 1 || response.Lobby.Members[0].IP != "198.51.100.10" {
		t.Fatalf("response members = %#v, want one checked-in member", response.Lobby.Members)
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
