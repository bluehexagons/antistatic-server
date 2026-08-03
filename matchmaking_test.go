package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func serveMatchmakingRequest(h *lobbyHandler, method, target, remoteAddr string, body any) *httptest.ResponseRecorder {
	return serveMatchmakingRequestWithToken(h, method, target, remoteAddr, body, "")
}

func serveMatchmakingRequestWithToken(h *lobbyHandler, method, target, remoteAddr string, body any, token string) *httptest.ResponseRecorder {
	return serveMatchmakingRequestWithTokenAndTags(h, method, target, remoteAddr, body, token, "", "", "")
}

func serveMatchmakingRequestWithTokenAndTags(h *lobbyHandler, method, target, remoteAddr string, body any, token, selfTag, peerTag, tagToken string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		data, ok := body.(json.RawMessage)
		if !ok {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				panic(err)
			}
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
	if selfTag != "" {
		req.Header.Set(antistaticMatchSelfTagHeader, selfTag)
	}
	if peerTag != "" {
		req.Header.Set(antistaticMatchPeerTagHeader, peerTag)
	}
	if tagToken != "" {
		req.Header.Set(antistaticMatchSelfTagTokenHeader, tagToken)
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
	if got := h.Metrics.successfulMatches.Load(); got != 0 {
		t.Fatalf("successful matches = %d, want 0", got)
	}
}

func TestMatchmakingClientGameReportIsAuthenticatedAndAggregated(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	firstResponse := decodeMatchmakingResponse(t, first)
	second := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45861", "198.51.100.20:32001", matchmakingRequest{Character: "Silicon"})
	if second.Code != http.StatusOK || decodeMatchmakingResponse(t, second).Status != "matched" {
		t.Fatalf("second ticket did not match: status=%d body=%s", second.Code, second.Body.String())
	}

	target := "/0.9.5/matchmaking/default/TicketA/45860/report"
	rec := serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_connect_failed"}, firstResponse.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("game report status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if got := h.Metrics.gameErrors.Load(); got != 1 {
		t.Fatalf("game error count = %d, want one report", got)
	}
	failureReportID := rec.Header().Get(antistaticReportIDHeader)
	if failureReportID == "" {
		t.Fatal("game report should return a report ID")
	}
	_, gameErrors := h.Metrics.snapshotRecentErrors()
	if len(gameErrors) != 1 || gameErrors[0].ReportID != failureReportID {
		t.Fatalf("recent game errors = %#v, want report ID %q", gameErrors, failureReportID)
	}

	rec = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_connected"}, firstResponse.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("successful game report status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := h.Metrics.matchSuccesses.Load(); got != 1 {
		t.Fatalf("match connection success count = %d, want one report", got)
	}

	rec = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_connected"}, firstResponse.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("duplicate game report status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	connectedReportID := rec.Header().Get(antistaticReportIDHeader)
	if connectedReportID == "" {
		t.Fatal("duplicate game report should return its original report ID")
	}
	rec = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_connected"}, firstResponse.Token)
	if got := rec.Header().Get(antistaticReportIDHeader); got != connectedReportID {
		t.Fatalf("duplicate report ID = %q, want stable %q", got, connectedReportID)
	}

	rec = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_sim_desync"}, firstResponse.Token)
	if rec.Code != http.StatusNoContent || rec.Header().Get(antistaticReportIDHeader) == "" {
		t.Fatalf("sim-desync report status/header = %d/%q, want 204/report ID", rec.Code, rec.Header().Get(antistaticReportIDHeader))
	}
	h.Mu.RLock()
	stats := h.Queues[matchmakingQueueKey("0.9.5", "default")]
	h.Mu.RUnlock()
	if stats == nil || stats.Attempts != 2 || stats.Matches != 1 || stats.FailedConnections != 1 || stats.SuccessfulConnections != 1 {
		t.Fatalf("queue stats = %#v, want attempts/match/outcomes", stats)
	}

	rec = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "not-a-real-error"}, firstResponse.Token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid game report status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	rec = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_runtime_error"}, "wrong-token")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauthorized game report status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := h.Metrics.gameErrors.Load(); got != 2 {
		t.Fatalf("authorized sim report plus invalid reports changed game error count to %d", got)
	}
}

func TestNetplayReportPersistenceAndFailureIsolation(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	h := newTestLobbyHandler()
	h.Store = store
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	firstResponse := decodeMatchmakingResponse(t, first)
	second := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45861", "198.51.100.20:32001", matchmakingRequest{Character: "Silicon"})
	if second.Code != http.StatusOK || decodeMatchmakingResponse(t, second).Status != "matched" {
		t.Fatalf("second ticket did not match: %d %s", second.Code, second.Body.String())
	}
	target := "/0.9.5/matchmaking/default/TicketA/45860/report"
	openNormally := store.openAppend
	writeStarted := make(chan struct{})
	writeRelease := make(chan struct{})
	store.mu.Lock()
	store.openAppend = func(path string) (appendFile, error) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		return &faultAppendFile{File: file, write: func(payload []byte) (int, error) {
			close(writeStarted)
			<-writeRelease
			return file.Write(payload)
		}}, nil
	}
	store.mu.Unlock()
	recorder := serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_connect_failed"}, firstResponse.Token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("persisted netplay report status = %d", recorder.Code)
	}
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("netplay worker did not start blocked persistence")
	}
	close(writeRelease)
	waitForTestCondition(t, func() bool {
		records, err := store.netplay()
		return err == nil && len(records) == 1
	})
	store.mu.Lock()
	store.openAppend = openNormally
	store.mu.Unlock()
	records, err := store.netplay()
	if err != nil || records[0].ID != recorder.Header().Get(antistaticReportIDHeader) || records[0].AppVersion != "0.9.5" || records[0].Event != "match_connect_failed" {
		t.Fatalf("persisted netplay records = %#v, %v", records, err)
	}

	store.mu.Lock()
	store.openAppend = func(path string) (appendFile, error) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		return &faultAppendFile{File: file, write: func([]byte) (int, error) {
			return 0, errors.New("injected netplay persistence failure")
		}}, nil
	}
	store.mu.Unlock()
	recorder = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_runtime_error"}, firstResponse.Token)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get(antistaticReportIDHeader) == "" {
		t.Fatalf("storage failure changed report response = %d / %q", recorder.Code, recorder.Header().Get(antistaticReportIDHeader))
	}
	runtimeReportID := recorder.Header().Get(antistaticReportIDHeader)
	waitForTestCondition(t, func() bool {
		store.netplayMu.Lock()
		defer store.netplayMu.Unlock()
		_, pending := store.netplayPending[runtimeReportID]
		return !pending
	})
	h.Mu.RLock()
	ticket := h.Tickets[matchmakingTicketKey("0.9.5", "default", "TicketA")]
	recorded := ticket != nil && ticket.reportedEvents&uint8(1<<gameReportEventIndex("match_runtime_error")) != 0
	h.Mu.RUnlock()
	if !recorded {
		t.Fatal("storage failure rolled back accepted netplay report state")
	}

	store.mu.Lock()
	store.openAppend = openNormally
	store.mu.Unlock()
	recorder = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_runtime_error"}, firstResponse.Token)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get(antistaticReportIDHeader) != runtimeReportID {
		t.Fatalf("duplicate retry response = %d / %q, want stable %q", recorder.Code, recorder.Header().Get(antistaticReportIDHeader), runtimeReportID)
	}
	waitForTestCondition(t, func() bool {
		records, err := store.netplay()
		return err == nil && len(records) == 2
	})
	recorder = serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_runtime_error"}, firstResponse.Token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("persisted duplicate status = %d", recorder.Code)
	}
	records, err = store.netplay()
	if err != nil || len(records) != 2 {
		t.Fatalf("persisted duplicate created extra records: %#v, %v", records, err)
	}
}

func waitForTestCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for asynchronous condition")
}

func TestMatchedTicketsAreScrubbedAndRetainedForRuntimeReports(t *testing.T) {
	h := newTestLobbyHandler()
	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{
		Character:      "Carbon",
		LocalIPs:       []string{"192.168.1.10"},
		LocalEndpoints: []Endpoint{{IP: "192.168.1.10", Port: 45860}},
	})
	firstResponse := decodeMatchmakingResponse(t, first)
	second := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45861", "198.51.100.20:32001", matchmakingRequest{Character: "Silicon"})
	if second.Code != http.StatusOK {
		t.Fatalf("second ticket did not match: status=%d body=%s", second.Code, second.Body.String())
	}

	now := time.Now()
	h.Mu.Lock()
	for _, match := range h.Matches {
		match.CreatedAt = now.Add(-matchmakingMatchTimeout - time.Second)
	}
	h.cleanupMatchmakingLocked(now)
	ticket := h.Tickets[matchmakingTicketKey("0.9.5", "default", "TicketA")]
	if len(h.Matches) != 0 || len(h.Tickets) != 2 {
		t.Fatalf("post-match cleanup retained matches/tickets = %d/%d, want 0/2", len(h.Matches), len(h.Tickets))
	}
	if ticket == nil || ticket.Character != "" || len(ticket.Endpoints) != 0 || len(ticket.LocalIPs) != 0 || len(ticket.LocalEndpoints) != 0 {
		t.Fatalf("retained report ticket still contains matchmaking metadata: %#v", ticket)
	}
	h.Mu.Unlock()

	target := "/0.9.5/matchmaking/default/TicketA/45860/report"
	rec := serveMatchmakingRequestWithToken(h, http.MethodPost, target, "198.51.100.10:32000", gameReportRequest{Event: "match_runtime_error"}, firstResponse.Token)
	if rec.Code != http.StatusNoContent || rec.Header().Get(antistaticReportIDHeader) == "" {
		t.Fatalf("retained runtime report status/header = %d/%q", rec.Code, rec.Header().Get(antistaticReportIDHeader))
	}

	h.Mu.Lock()
	for _, retained := range h.Tickets {
		retained.CheckedIn = now.Add(-matchmakingReportRetention - time.Second)
	}
	h.cleanupMatchmakingLocked(now)
	remaining := len(h.Tickets)
	h.Mu.Unlock()
	if remaining != 0 {
		t.Fatalf("report retention cleanup left %d tickets, want 0", remaining)
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
	if got := h.Metrics.successfulMatches.Load(); got != 1 {
		t.Fatalf("successful matches = %d, want 1", got)
	}
	for _, ticket := range h.Tickets {
		if ticket.MatchedID == "" {
			t.Fatalf("waiting ticket remained after match: %#v", ticket)
		}
	}
}

func TestMatchmakingCodeQueueRequiresValidTagHeaders(t *testing.T) {
	h := newTestLobbyHandler()
	body := matchmakingRequest{Character: "Carbon"}

	rec := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/code.alpha1-bravo2/TicketA/45860", "198.51.100.10:32000", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing tag headers returned %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-charl3/TicketA/45860",
		"198.51.100.10:32000",
		body,
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched tag headers returned %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-alpha1/TicketA/45860",
		"198.51.100.10:32000",
		body,
		"",
		"ALPHA1",
		"ALPHA1",
		"",
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same self/peer tag headers returned %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestMatchmakingCodeQueueLeasesTagsAndMatchesReciprocalSearch(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/CODE.BRAVO2-ALPHA1/TicketA/45860",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		"",
		"alpha1",
		"bravo2",
		"",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first code PUT returned %d: %s", first.Code, first.Body.String())
	}
	firstResp := decodeMatchmakingResponse(t, first)
	if firstResp.Status != "waiting" || firstResp.Token == "" || firstResp.TagToken == "" {
		t.Fatalf("first code response = %#v, want waiting with ticket and tag tokens", firstResp)
	}

	h.Mu.RLock()
	_, ticketStored := h.Tickets[matchmakingTicketKey("0.9.5", "code.alpha1-bravo2", "TicketA")]
	leaseCount := len(h.TagLeases)
	h.Mu.RUnlock()
	if !ticketStored || leaseCount != 1 {
		t.Fatalf("after first PUT ticketStored=%v leases=%d, want true/1", ticketStored, leaseCount)
	}

	second := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketB/45861",
		"198.51.100.20:32000",
		matchmakingRequest{Character: "Silicon"},
		"",
		"BRAVO2",
		"ALPHA1",
		"",
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second code PUT returned %d: %s", second.Code, second.Body.String())
	}
	secondResp := decodeMatchmakingResponse(t, second)
	if secondResp.Status != "matched" || secondResp.Match == nil {
		t.Fatalf("second code response = %#v, want matched", secondResp)
	}
	if secondResp.Match.Role != "client" || secondResp.Match.Peer.Character != "Carbon" || secondResp.Match.Self.Character != "Silicon" {
		t.Fatalf("code match = %#v, want reciprocal Carbon/Silicon match", secondResp.Match)
	}

	h.Mu.RLock()
	defer h.Mu.RUnlock()
	if len(h.TagLeases) != 2 {
		t.Fatalf("tag leases = %d after match, want both player codes leased", len(h.TagLeases))
	}
}

func TestMatchmakingCodeTagLeaseRejectsDifferentToken(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketA/45860",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first code PUT returned %d: %s", first.Code, first.Body.String())
	}

	conflict := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketB/45861",
		"198.51.100.20:32000",
		matchmakingRequest{Character: "Silicon"},
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("duplicate self tag returned %d, want %d", conflict.Code, http.StatusConflict)
	}

	h.Mu.RLock()
	defer h.Mu.RUnlock()
	if len(h.Tickets) != 1 || len(h.TagLeases) != 1 || len(h.Matches) != 0 {
		t.Fatalf("state after duplicate tag: tickets=%d leases=%d matches=%d, want 1/1/0", len(h.Tickets), len(h.TagLeases), len(h.Matches))
	}
}

func TestMatchmakingCodeTagLeasePersistsAfterDeleteForSameToken(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketA/45860",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first code PUT returned %d: %s", first.Code, first.Body.String())
	}
	firstResp := decodeMatchmakingResponse(t, first)
	token := firstResp.Token
	tagToken := firstResp.TagToken
	if tagToken == "" {
		t.Fatalf("first response tag token was empty")
	}

	deleted := serveMatchmakingRequestWithToken(h, http.MethodDelete, "/0.9.5/matchmaking/code.alpha1-bravo2/TicketA/45860", "198.51.100.10:32000", nil, token)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete returned %d, want %d: %s", deleted.Code, http.StatusOK, deleted.Body.String())
	}

	conflict := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketB/45861",
		"198.51.100.20:32000",
		matchmakingRequest{Character: "Silicon"},
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("second lease without tag token returned %d, want %d", conflict.Code, http.StatusConflict)
	}

	second := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketB/45861",
		"198.51.100.20:32000",
		matchmakingRequest{Character: "Silicon"},
		"",
		"ALPHA1",
		"BRAVO2",
		tagToken,
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second lease with tag token returned %d, want %d: %s", second.Code, http.StatusOK, second.Body.String())
	}
}

func TestMatchmakingCodeTagLeaseExpiresAfterTimeout(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketA/45860",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first code PUT returned %d: %s", first.Code, first.Body.String())
	}

	h.Mu.Lock()
	for _, lease := range h.TagLeases {
		lease.CheckedIn = time.Now().Add(-matchmakingTagLeaseTimeout - time.Second)
	}
	h.Mu.Unlock()

	second := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketB/45861",
		"198.51.100.20:32000",
		matchmakingRequest{Character: "Silicon"},
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second lease after expiry returned %d, want %d: %s", second.Code, http.StatusOK, second.Body.String())
	}
}

func TestMatchmakingCodeTagLeaseRefreshExtendsExpiry(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketA/45860",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		"",
		"ALPHA1",
		"BRAVO2",
		"",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first code PUT returned %d: %s", first.Code, first.Body.String())
	}
	firstResp := decodeMatchmakingResponse(t, first)

	h.Mu.Lock()
	lease := h.TagLeases[matchmakingTagLeaseKey("0.9.5", "ALPHA1")]
	lease.CheckedIn = time.Now().Add(-matchmakingTagLeaseTimeout / 2)
	before := lease.CheckedIn
	h.Mu.Unlock()

	refresh := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.alpha1-bravo2/TicketA/45860",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		firstResp.Token,
		"ALPHA1",
		"BRAVO2",
		firstResp.TagToken,
	)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh returned %d, want %d: %s", refresh.Code, http.StatusOK, refresh.Body.String())
	}

	h.Mu.RLock()
	after := h.TagLeases[matchmakingTagLeaseKey("0.9.5", "ALPHA1")].CheckedIn
	h.Mu.RUnlock()
	if !after.After(before) {
		t.Fatalf("lease CheckedIn did not refresh: before=%v after=%v", before, after)
	}
}

func TestMatchmakingCodeTagLeaseCapsLeasesPerIP(t *testing.T) {
	h := newTestLobbyHandler()

	for i := 0; i < maxMatchmakingTagLeasesPerIP; i++ {
		selfTag := fmt.Sprintf("A%03d", i)
		peerTag := fmt.Sprintf("Z%03d", i)
		rec := serveMatchmakingRequestWithTokenAndTags(
			h,
			http.MethodPut,
			fmt.Sprintf("/0.9.5/matchmaking/code.%s-%s/Ticket%d/45860", strings.ToLower(selfTag), strings.ToLower(peerTag), i),
			"198.51.100.10:32000",
			matchmakingRequest{Character: "Carbon"},
			"",
			selfTag,
			peerTag,
			"",
		)
		if rec.Code != http.StatusOK {
			t.Fatalf("lease %d returned %d, want %d: %s", i, rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	rec := serveMatchmakingRequestWithTokenAndTags(
		h,
		http.MethodPut,
		"/0.9.5/matchmaking/code.a999-z999/Ticket9/45860",
		"198.51.100.10:32000",
		matchmakingRequest{Character: "Carbon"},
		"",
		"A999",
		"Z999",
		"",
	)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("extra lease returned %d, want %d", rec.Code, http.StatusTooManyRequests)
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

func TestMatchmakingInitialPutLongPollWakesOnMatch(t *testing.T) {
	h := newTestLobbyHandler()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- serveMatchmakingRequest(
			h,
			http.MethodPut,
			"/0.9.5/matchmaking/default/TicketA/45860?wait=2",
			"198.51.100.10:32000",
			matchmakingRequest{Character: "Carbon"},
		)
	}()

	ticketKey := matchmakingTicketKey("0.9.5", "default", "TicketA")
	deadline := time.Now().Add(time.Second)
	for {
		h.Mu.RLock()
		_, waiting := h.Tickets[ticketKey]
		h.Mu.RUnlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial PUT did not register its ticket within 1s")
		}
		time.Sleep(time.Millisecond)
	}

	second := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45861", "198.51.100.20:32000", matchmakingRequest{Character: "Silicon"})
	if second.Code != http.StatusOK {
		t.Fatalf("second PUT returned %d: %s", second.Code, second.Body.String())
	}

	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("initial long-poll PUT returned %d: %s", rec.Code, rec.Body.String())
		}
		resp := decodeMatchmakingResponse(t, rec)
		if resp.Status != "matched" || resp.Match == nil || resp.Token == "" {
			t.Fatalf("initial long-poll response = %#v, want matched response with token", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("initial long-poll PUT did not wake within 1s of the match")
	}
}

func TestMatchmakingPutLongPollBroadcastsMatchToConcurrentWaiters(t *testing.T) {
	h := newTestLobbyHandler()

	first := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", matchmakingRequest{Character: "Carbon"})
	if first.Code != http.StatusOK {
		t.Fatalf("initial PUT returned %d: %s", first.Code, first.Body.String())
	}
	tokenA := decodeMatchmakingResponse(t, first).Token

	const waiterCount = 32
	done := make(chan *httptest.ResponseRecorder, waiterCount)
	for range waiterCount {
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
	}

	time.Sleep(100 * time.Millisecond)
	second := serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketB/45861", "198.51.100.20:32000", matchmakingRequest{Character: "Silicon"})
	if second.Code != http.StatusOK {
		t.Fatalf("second PUT returned %d: %s", second.Code, second.Body.String())
	}

	deadline := time.After(time.Second)
	for i := range waiterCount {
		select {
		case rec := <-done:
			if rec.Code != http.StatusOK {
				t.Fatalf("waiter %d returned %d: %s", i, rec.Code, rec.Body.String())
			}
			resp := decodeMatchmakingResponse(t, rec)
			if resp.Status != "matched" || resp.Match == nil {
				t.Fatalf("waiter %d response = %#v, want matched", i, resp)
			}
		case <-deadline:
			t.Fatalf("only %d/%d concurrent waiters received the match within 1s", i, waiterCount)
		}
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
	if got := h.Metrics.httpErrors.Load(); got == 0 {
		t.Fatalf("client error counter = %d, want > 0", got)
	}

	rec = serveMatchmakingRequest(h, http.MethodPut, "/0.9.5/matchmaking/default/TicketA/45860", "198.51.100.10:32000", json.RawMessage(`{"character":"Carbon"} {}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
