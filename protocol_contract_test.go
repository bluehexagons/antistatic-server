package main

import (
	"encoding/json"
	"os"
	"testing"
)

func readProtocolFixture(t *testing.T, path string, destination any) {
	t.Helper()
	data, err := os.ReadFile("protocol/fixtures/" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
}

func validContractEndpoint(endpoint Endpoint) bool {
	return validatePort(endpoint.Port) && endpoint.IP != ""
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
	if response.Status != "matched" || response.Ticket == "" || response.Token == "" || response.Match == nil {
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
