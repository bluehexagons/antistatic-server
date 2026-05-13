package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	if response.Ticket != "TicketA" || len(response.Endpoints) != 1 || response.Endpoints[0].IP != "198.51.100.10" || response.Endpoints[0].Port != 45860 {
		t.Fatalf("response = %#v, want ticket/IP/port preserved", response)
	}
	if response.Token == "" {
		t.Fatalf("response token was empty")
	}
	if response.Queue == nil || response.Queue.PlayersWaiting != 1 {
		t.Fatalf("queue stats = %#v, want one waiting player", response.Queue)
	}
	if len(h.Tickets) != 1 || len(h.Matches) != 0 {
		t.Fatalf("handler maps = tickets:%d matches:%d, want 1/0", len(h.Tickets), len(h.Matches))
	}
	if got := h.Metrics.successfulGames.Load(); got != 0 {
		t.Fatalf("successful games = %d, want 0", got)
	}
}

func TestMatchmakingWaitingResponseIncludesQueueWaits(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	token := decodeMatchmakingResponse(t, first).Token

	h.Mu.Lock()
	ticket := h.Tickets[matchmakingTicketKey("0.9.5", "default", "TicketA")]
	ticket.CreatedAt = time.Now().Add(-12 * time.Second)
	h.Mu.Unlock()

	rec := serveMatchmakingRequestWithToken(h, http.MethodGet, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET returned status %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	response := decodeMatchmakingResponse(t, rec)
	if response.Queue == nil {
		t.Fatalf("queue stats missing from waiting response")
	}
	if response.Queue.PlayersWaiting != 1 {
		t.Fatalf("players waiting = %d, want 1", response.Queue.PlayersWaiting)
	}
	if response.Queue.OwnWaitMs < 11000 || response.Queue.OldestWaitMs < response.Queue.OwnWaitMs {
		t.Fatalf("queue waits = %#v, want own wait around 12s and oldest at least own wait", response.Queue)
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
	updatedEndpoints := append([]Endpoint(nil), h.Tickets[key].Endpoints...)
	h.Mu.RUnlock()
	if !updated.After(before) {
		t.Fatalf("checked-in time was not refreshed: before=%v after=%v", before, updated)
	}
	if len(updatedEndpoints) != 1 || updatedEndpoints[0].Port != 45860 {
		t.Fatalf("ticket endpoints = %#v, want one entry on port 45860", updatedEndpoints)
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
	if len(ticket.Endpoints) != 1 || ticket.Endpoints[0].IP != "198.51.100.11" || ticket.Endpoints[0].Port != 45861 {
		t.Fatalf("ticket endpoints = %#v, want single replacement 198.51.100.11:45861", ticket.Endpoints)
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
	if len(ticket.Endpoints) != 1 || ticket.Endpoints[0].IP != "198.51.100.10" || ticket.Endpoints[0].Port != 45860 {
		t.Fatalf("ticket endpoints = %#v after wrong-token refresh, want unchanged 198.51.100.10:45860", ticket.Endpoints)
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
	if response.Queue == nil || response.Queue.MatchCount != 1 || response.Queue.AverageMatchWaitMs < 0 {
		t.Fatalf("queue stats after match = %#v, want recorded match stats", response.Queue)
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

func TestMatchmakingPutLongPollWakesOnMatch(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	if first.Code != http.StatusOK {
		t.Fatalf("initial PUT returned %d: %s", first.Code, first.Body.String())
	}
	tokenA := decodeMatchmakingResponse(t, first).Token

	done := make(chan *httptest.ResponseRecorder, 1)
	started := time.Now()
	go func() {
		done <- serveMatchmakingRequestWithToken(
			h,
			http.MethodPut,
			"/0.9.5/matchmaking/default/TicketA/45860?wait=2",
			"198.51.100.10:32000",
			matchmakingRequest{Character: "Carbon"},
			tokenA,
		)
	}()

	// Give the goroutine a chance to enter the long-poll wait, then register
	// a compatible ticket so the long-poller's next check sees the match.
	time.Sleep(150 * time.Millisecond)
	second := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45861", "198.51.100.20:32000", matchmakingRequest{Character: "Silicon"})
	if second.Code != http.StatusOK {
		t.Fatalf("second PUT returned %d: %s", second.Code, second.Body.String())
	}

	rec := <-done
	elapsed := time.Since(started)
	if elapsed > time.Second {
		t.Fatalf("long-poll took %v, expected to wake within 1s of the match", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("long-poll PUT returned %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeMatchmakingResponse(t, rec)
	if resp.Status != "matched" || resp.Match == nil {
		t.Fatalf("long-poll response = %#v, want matched with peer data", resp)
	}
	if resp.Match.MatchedAtMs == 0 {
		t.Fatalf("MatchedAtMs was not populated in match response: %#v", resp.Match)
	}
}

func TestMatchmakingPutLongPollTimesOut(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	if first.Code != http.StatusOK {
		t.Fatalf("initial PUT returned %d: %s", first.Code, first.Body.String())
	}
	tokenA := decodeMatchmakingResponse(t, first).Token

	started := time.Now()
	rec := serveMatchmakingRequestWithToken(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/default/TicketA/45860?wait=1",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		tokenA,
	)
	elapsed := time.Since(started)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("long-poll returned in %v, expected to wait at least ~1s", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("long-poll PUT returned %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeMatchmakingResponse(t, rec)
	if resp.Status != "waiting" || resp.Match != nil {
		t.Fatalf("long-poll response = %#v, want still waiting", resp)
	}
}

func TestMatchmakingReflectsLocalIPsOnlyToSamePublicIP(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequest(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/default/TicketA/45860",
		"203.0.113.5:32000",
		matchmakingRequest{
			Character: "Carbon",
			LocalIPs:  []string{"8.8.8.8", "192.168.1.20"},
			LocalEndpoints: []Endpoint{
				{IP: "8.8.8.8", Port: 45860},
				{IP: "10.0.0.99", Port: 45860},
				{IP: "127.0.0.1", Port: 45860},
				{IP: "192.168.1.20", Port: 45860},
				{IP: "192.168.1.20", Port: 0},
			},
		},
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first PUT returned %d", first.Code)
	}

	second := serveMatchmakingRequest(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/default/TicketB/45861",
		"203.0.113.5:32001",
		matchmakingRequest{Character: "Silicon", LocalIPs: []string{"10.0.0.20"}},
	)
	response := decodeMatchmakingResponse(t, second)
	if response.Status != "matched" || response.Match == nil {
		t.Fatalf("response = %#v, want matched with peer data", response)
	}
	if !reflect.DeepEqual(response.Match.Peer.LocalIPs, []string{"192.168.1.20"}) {
		t.Fatalf("same-public-IP peer local_ips = %v, want sanitized LAN IP", response.Match.Peer.LocalIPs)
	}
	wantLocalEndpoints := []Endpoint{{IP: "127.0.0.1", Port: 45860}, {IP: "192.168.1.20", Port: 45860}}
	if !reflect.DeepEqual(response.Match.Peer.LocalEndpoints, wantLocalEndpoints) {
		t.Fatalf("same-public-IP peer local_endpoints = %v, want %v", response.Match.Peer.LocalEndpoints, wantLocalEndpoints)
	}

	third := serveMatchmakingRequest(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/default/TicketC/45862",
		"203.0.113.6:32002",
		matchmakingRequest{
			Character:      "Carbon",
			LocalIPs:       []string{"192.168.2.20"},
			LocalEndpoints: []Endpoint{{IP: "192.168.2.20", Port: 45862}},
		},
	)
	if third.Code != http.StatusOK {
		t.Fatalf("third PUT returned %d", third.Code)
	}

	fourth := serveMatchmakingRequest(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/default/TicketD/45863",
		"203.0.113.7:32003",
		matchmakingRequest{Character: "Silicon", LocalIPs: []string{"192.168.3.20"}},
	)
	response = decodeMatchmakingResponse(t, fourth)
	if response.Status != "matched" || response.Match == nil {
		t.Fatalf("cross-public-IP response = %#v, want matched with peer data", response)
	}
	if response.Match.Peer.LocalIPs != nil {
		t.Fatalf("different-public-IP peer local_ips = %v, want nil", response.Match.Peer.LocalIPs)
	}
	if response.Match.Peer.LocalEndpoints != nil {
		t.Fatalf("different-public-IP peer local_endpoints = %v, want nil", response.Match.Peer.LocalEndpoints)
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
	if response.Queue == nil || response.Queue.PlayersWaiting != 2 {
		t.Fatalf("same endpoint queue stats = %#v, want two waiting players", response.Queue)
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
	if got := h.Metrics.clientErrors.Load(); got == 0 {
		t.Fatalf("client error counter = %d, want > 0", got)
	}
}
