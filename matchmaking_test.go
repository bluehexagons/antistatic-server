package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serveMatchmakingRequest(h *lobbyHandler, method, target, remoteAddr string, body any) *httptest.ResponseRecorder {
	return serveMatchmakingRequestWithToken(h, method, target, remoteAddr, body, "")
}

func serveMatchmakingRequestWithToken(h *lobbyHandler, method, target, remoteAddr string, body any, token string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, target, reader)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set(antistaticTokenHeader, token)
	}
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	return rec
}

func decodeMatchmakingResponse(t *testing.T, rec *httptest.ResponseRecorder) matchmakingResponse {
	t.Helper()

	var response matchmakingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unable to decode response: %v; body=%q", err, rec.Body.String())
	}

	return response
}

func TestMatchmakingTicketCreatesWaitingResponse(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned status %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	response := decodeMatchmakingResponse(t, rec)
	if response.Status != "waiting" {
		t.Fatalf("status = %q, want waiting", response.Status)
	}
	if response.Ticket != "TicketA" || response.IP != "198.51.100.10" || response.Port != 45860 {
		t.Fatalf("response = %#v, want ticket/IP/port preserved", response)
	}
	if response.Token == "" {
		t.Fatalf("response token was empty")
	}
	if len(h.Tickets) != 1 || len(h.Matches) != 0 {
		t.Fatalf("handler maps = tickets:%d matches:%d, want 1/0", len(h.Tickets), len(h.Matches))
	}
	if got := h.Metrics.successfulGames.Load(); got != 0 {
		t.Fatalf("successful games = %d, want 0", got)
	}
}

func TestMatchmakingRefreshPreservesTicketAndUpdatesCheckIn(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	token := decodeMatchmakingResponse(t, first).Token

	key := matchmakingTicketKey("0.9.5", "default", "TicketA")
	h.Mu.Lock()
	h.Tickets[key].CheckedIn = time.Now().Add(-matchmakingTicketTimeout / 2)
	before := h.Tickets[key].CheckedIn
	h.Mu.Unlock()

	rec := serveMatchmakingRequestWithToken(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh returned status %d, want %d", rec.Code, http.StatusOK)
	}

	h.Mu.RLock()
	updated := h.Tickets[key].CheckedIn
	updatedPort := h.Tickets[key].Port
	h.Mu.RUnlock()
	if !updated.After(before) {
		t.Fatalf("checked-in time was not refreshed: before=%v after=%v", before, updated)
	}
	if updatedPort != 45860 {
		t.Fatalf("ticket port = %d, want %d", updatedPort, 45860)
	}
}

func TestMatchmakingRefreshUpdatesEndpoint(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	token := decodeMatchmakingResponse(t, first).Token

	rec := serveMatchmakingRequestWithToken(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/default/TicketA/45861",
		"198.51.100.11:32000",
		matchmakingRequest{Character: "Carbon"},
		token,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh returned status %d, want %d", rec.Code, http.StatusOK)
	}

	h.Mu.RLock()
	ticket := h.Tickets[matchmakingTicketKey("0.9.5", "default", "TicketA")]
	h.Mu.RUnlock()
	if ticket.IP != "198.51.100.11" || ticket.Port != 45861 {
		t.Fatalf("ticket endpoint = %s:%d, want 198.51.100.11:45861", ticket.IP, ticket.Port)
	}
}

func TestMatchmakingRefreshRejectsWrongToken(t *testing.T) {
	h := newTestLobbyHandler()
	_ = serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})

	rec := serveMatchmakingRequestWithToken(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/default/TicketA/45861",
		"198.51.100.11:32000",
		matchmakingRequest{Character: "Carbon"},
		"wrong-token",
	)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("refresh with wrong token returned status %d, want %d", rec.Code, http.StatusForbidden)
	}

	h.Mu.RLock()
	ticket := h.Tickets[matchmakingTicketKey("0.9.5", "default", "TicketA")]
	h.Mu.RUnlock()
	if ticket.IP != "198.51.100.10" || ticket.Port != 45860 {
		t.Fatalf("ticket endpoint changed to %s:%d after wrong-token refresh", ticket.IP, ticket.Port)
	}
}

func TestMatchmakingMatchesCompatibleTicketsFIFO(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	if first.Code != http.StatusOK {
		t.Fatalf("first PUT returned %d", first.Code)
	}

	second := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45861", "198.51.100.20:32000", matchmakingRequest{Character: "Silicon"})
	if second.Code != http.StatusOK {
		t.Fatalf("second PUT returned %d", second.Code)
	}

	response := decodeMatchmakingResponse(t, second)
	if response.Status != "matched" || response.Match == nil {
		t.Fatalf("response = %#v, want matched with peer data", response)
	}
	if response.Match.Role != "client" {
		t.Fatalf("second ticket role = %q, want client", response.Match.Role)
	}
	if response.Match.Peer.Character != "Carbon" || response.Match.Self.Character != "Silicon" {
		t.Fatalf("match characters = %#v, want Carbon vs Silicon", response.Match)
	}

	h.Mu.RLock()
	defer h.Mu.RUnlock()
	if len(h.Matches) != 1 {
		t.Fatalf("match count = %d, want 1", len(h.Matches))
	}
	if got := h.Metrics.successfulGames.Load(); got != 1 {
		t.Fatalf("successful games = %d, want 1", got)
	}
	for _, ticket := range h.Tickets {
		if ticket.MatchedID == "" {
			t.Fatalf("waiting ticket remained after match: %#v", ticket)
		}
	}
}

func TestMatchmakingDoesNotMatchSameEndpoint(t *testing.T) {
	h := newTestLobbyHandler()
	_ = serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	rec := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Silicon"})

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned %d", rec.Code)
	}

	response := decodeMatchmakingResponse(t, rec)
	if response.Status != "waiting" {
		t.Fatalf("same endpoint response = %#v, want waiting", response)
	}
	if len(h.Matches) != 0 || len(h.Tickets) != 2 {
		t.Fatalf("maps = tickets:%d matches:%d, want 2/0", len(h.Tickets), len(h.Matches))
	}
}

func TestMatchmakingDeleteRemovesWaitingTicket(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	token := decodeMatchmakingResponse(t, first).Token
	rec := serveMatchmakingRequestWithToken(h, http.MethodDelete, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", nil, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE returned %d, want %d", rec.Code, http.StatusOK)
	}

	response := decodeMatchmakingResponse(t, rec)
	if response.Status != "canceled" {
		t.Fatalf("delete response = %#v, want canceled", response)
	}

	h.Mu.RLock()
	defer h.Mu.RUnlock()
	if len(h.Tickets) != 0 || len(h.Matches) != 0 {
		t.Fatalf("maps not cleared after delete: tickets=%d matches=%d", len(h.Tickets), len(h.Matches))
	}
}

func TestMatchmakingDeleteRejectsWrongToken(t *testing.T) {
	h := newTestLobbyHandler()
	_ = serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	rec := serveMatchmakingRequestWithToken(h, http.MethodDelete, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", nil, "wrong-token")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("DELETE with wrong token returned %d, want %d", rec.Code, http.StatusForbidden)
	}

	h.Mu.RLock()
	defer h.Mu.RUnlock()
	if len(h.Tickets) != 1 {
		t.Fatalf("ticket count = %d, want 1 after rejected delete", len(h.Tickets))
	}
}

func TestMatchmakingStateRejectsWrongToken(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	token := decodeMatchmakingResponse(t, first).Token

	rec := serveMatchmakingRequestWithToken(h, http.MethodGet, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", nil, "wrong-token")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET with wrong token returned %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = serveMatchmakingRequestWithToken(h, http.MethodGet, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with correct token returned %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestMatchmakingCleanupDropsStaleTickets(t *testing.T) {
	h := newTestLobbyHandler()
	_ = serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})

	h.Mu.Lock()
	for _, ticket := range h.Tickets {
		ticket.CheckedIn = time.Now().Add(-matchmakingTicketTimeout - time.Second)
	}
	h.cleanupMatchmakingLocked(time.Now())
	remainingTickets := len(h.Tickets)
	remainingMatches := len(h.Matches)
	h.Mu.Unlock()

	if remainingTickets != 0 || remainingMatches != 0 {
		t.Fatalf("cleanup left tickets=%d matches=%d, want 0/0", remainingTickets, remainingMatches)
	}
}

func TestMatchmakingRejectsInvalidValues(t *testing.T) {
	h := newTestLobbyHandler()
	rec := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/bad!queue/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid queue status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Bad;Character"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid character status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := h.Metrics.errors.Load(); got == 0 {
		t.Fatalf("error counter = %d, want > 0", got)
	}
}
