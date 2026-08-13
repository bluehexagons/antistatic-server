package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func protocolFixtureBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile("protocol/fixtures/" + path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readProtocolFixture(t *testing.T, path string, destination any) {
	t.Helper()
	if err := json.Unmarshal(protocolFixtureBytes(t, path), destination); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
}

func decodeProtocolFixtureStrict(t *testing.T, path string, destination any) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(protocolFixtureBytes(t, path)))
	request.Header.Set("Content-Type", "application/json")
	return decodeStrictJSON(httptest.NewRecorder(), request, destination)
}

func validContractEndpoint(endpoint Endpoint) bool {
	return validatePeerPort(endpoint.Port) && endpoint.IP != ""
}

func validContractLobbyResponse(response lobbyResponse) bool {
	if response.Lobby == nil || response.Lobby.Key == "" || response.Token == "" || !validContractEndpoint(response.Endpoint) {
		return false
	}
	for _, member := range response.Lobby.Members {
		if len(member.Endpoints) == 0 {
			return false
		}
		for _, endpoint := range member.Endpoints {
			if !validContractEndpoint(endpoint) {
				return false
			}
		}
	}
	return true
}

func validContractMatchmakingResponse(response matchmakingResponse) bool {
	if response.Ticket == "" || response.Token == "" {
		return false
	}
	for _, endpoint := range response.Endpoints {
		if !validContractEndpoint(endpoint) {
			return false
		}
	}
	if response.Status == "waiting" || response.Status == "canceled" {
		return response.Match == nil && (response.Status == "canceled" || len(response.Endpoints) > 0)
	}
	if response.Status != "matched" || response.Match == nil {
		return false
	}
	if response.Match.Role != "host" && response.Match.Role != "client" {
		return false
	}
	for _, peer := range []matchmakingPeer{response.Match.Peer, response.Match.Self} {
		if !validateMatchmakingCharacter(peer.Metadata.Character) || len(peer.Endpoints) == 0 {
			return false
		}
		for _, endpoint := range peer.Endpoints {
			if !validContractEndpoint(endpoint) {
				return false
			}
		}
	}
	return true
}

func TestSharedProtocolResponseFixtures(t *testing.T) {
	var lobby lobbyResponse
	readProtocolFixture(t, "valid/lobby-response.json", &lobby)
	if !validContractLobbyResponse(lobby) {
		t.Fatalf("valid lobby fixture rejected: %#v", lobby)
	}

	var invalidLobby lobbyResponse
	readProtocolFixture(t, "invalid/lobby-response-missing-token.json", &invalidLobby)
	if validContractLobbyResponse(invalidLobby) {
		t.Fatalf("invalid lobby fixture accepted: %#v", invalidLobby)
	}
	readProtocolFixture(t, "invalid/lobby-response-zero-port.json", &invalidLobby)
	if validContractLobbyResponse(invalidLobby) {
		t.Fatalf("zero-port lobby fixture accepted: %#v", invalidLobby)
	}

	for _, fixture := range []string{"valid/matchmaking-waiting-response.json", "valid/matchmaking-canceled-response.json"} {
		var response matchmakingResponse
		readProtocolFixture(t, fixture, &response)
		if !validContractMatchmakingResponse(response) {
			t.Fatalf("valid matchmaking fixture %s rejected: %#v", fixture, response)
		}
	}

	var matched matchmakingResponse
	readProtocolFixture(t, "valid/matchmaking-matched-response.json", &matched)
	if !validContractMatchmakingResponse(matched) {
		t.Fatalf("valid matchmaking fixture rejected: %#v", matched)
	}

	var invalidMatched matchmakingResponse
	readProtocolFixture(t, "invalid/matchmaking-matched-response-bad-port.json", &invalidMatched)
	if validContractMatchmakingResponse(invalidMatched) {
		t.Fatalf("invalid matchmaking fixture accepted: %#v", invalidMatched)
	}
}

func TestSharedProtocolRequestAndServiceFixtures(t *testing.T) {
	var lobby lobbyCheckInBody
	if status := decodeProtocolFixtureStrict(t, "valid/lobby-request.json", &lobby); status != 0 || !validatePeerPort(lobby.Port) {
		t.Fatalf("valid lobby request status/body = %d/%#v", status, lobby)
	}
	var leave lobbyLeaveBody
	if status := decodeProtocolFixtureStrict(t, "valid/lobby-leave-request.json", &leave); status != 0 || !validatePeerPort(leave.Port) {
		t.Fatalf("valid lobby leave status/body = %d/%#v", status, leave)
	}
	if status := decodeProtocolFixtureStrict(t, "invalid/lobby-leave-request-extra-fields.json", &leave); status != http.StatusBadRequest {
		t.Fatalf("lobby leave with extra fields status = %d, want %d", status, http.StatusBadRequest)
	}

	var matchmaking matchmakingRequest
	if status := decodeProtocolFixtureStrict(t, "valid/matchmaking-request.json", &matchmaking); status != 0 {
		t.Fatalf("valid matchmaking request status = %d", status)
	}
	if _, _, ok := normalizeMatchmakingTags(matchmaking.MatchCode, matchmaking.Queue); !ok || !validateMatchmakingCharacter(matchmaking.Metadata.Character) {
		t.Fatalf("valid matchmaking request rejected: %#v", matchmaking)
	}
	if status := decodeProtocolFixtureStrict(t, "invalid/matchmaking-request-extra-metadata.json", &matchmaking); status != http.StatusBadRequest {
		t.Fatalf("extra metadata status = %d, want %d", status, http.StatusBadRequest)
	}
	if status := decodeProtocolFixtureStrict(t, "invalid/matchmaking-request-bad-match-code.json", &matchmaking); status != 0 {
		t.Fatalf("bad match-code fixture decode status = %d", status)
	}
	if _, _, ok := normalizeMatchmakingTags(matchmaking.MatchCode, matchmaking.Queue); ok {
		t.Fatalf("bad match-code fixture accepted: %#v", matchmaking.MatchCode)
	}
	var cancel matchmakingCancelRequest
	if status := decodeProtocolFixtureStrict(t, "valid/matchmaking-cancel-request.json", &cancel); status != 0 {
		t.Fatalf("valid matchmaking cancel status = %d", status)
	}
	if status := decodeProtocolFixtureStrict(t, "invalid/matchmaking-cancel-request-extra-fields.json", &cancel); status != http.StatusBadRequest {
		t.Fatalf("matchmaking cancel with extra fields status = %d, want %d", status, http.StatusBadRequest)
	}

	var events eventsResponse
	readProtocolFixture(t, "valid/events-response.json", &events)
	if len(events.Events) != 1 || events.Events[0].DurationMinutes < 1 {
		t.Fatalf("valid events fixture rejected: %#v", events)
	}
	readProtocolFixture(t, "invalid/events-response-zero-duration.json", &events)
	if len(events.Events) != 1 || events.Events[0].DurationMinutes >= 1 {
		t.Fatalf("invalid events fixture accepted: %#v", events)
	}

	var health healthResponse
	readProtocolFixture(t, "valid/health-response.json", &health)
	if health.Status != "ok" || health.Activity.Timezone != "UTC" || health.Events == nil {
		t.Fatalf("valid health fixture rejected: %#v", health)
	}

	var crash crashRequest
	if status := decodeProtocolFixtureStrict(t, "valid/crash-report-request.json", &crash); status != 0 || !crash.valid() {
		t.Fatalf("valid crash fixture status/body = %d/%#v", status, crash)
	}
	var feedback feedbackRequest
	if status := decodeProtocolFixtureStrict(t, "valid/feedback-request.json", &feedback); status != 0 || !feedback.valid() {
		t.Fatalf("valid feedback fixture status/body = %d/%#v", status, feedback)
	}
	var gameplay gameplayRequest
	if status := decodeProtocolFixtureStrict(t, "valid/gameplay-metric-request.json", &gameplay); status != 0 || !gameplay.valid() {
		t.Fatalf("valid gameplay fixture status/body = %d/%#v", status, gameplay)
	}
	var performance performanceRequest
	if status := decodeProtocolFixtureStrict(t, "valid/performance-metric-request.json", &performance); status != 0 || !performance.valid() {
		t.Fatalf("valid performance fixture status/body = %d/%#v", status, performance)
	}
}

func TestOpenAPIAntistaticCompatibilityIDMatchesDefault(t *testing.T) {
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Const string `json:"const"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(protocolFixtureBytes(t, "../openapi.json"), &document); err != nil {
		t.Fatal(err)
	}
	got := document.Components.Schemas["AntistaticCompatibilityID"].Const
	if got != DefaultConfig().Service.CompatibilityID {
		t.Fatalf("OpenAPI compatibility ID = %q, default = %q", got, DefaultConfig().Service.CompatibilityID)
	}
}
