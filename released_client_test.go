package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReleasedClient09125MatchmakingWireContract(t *testing.T) {
	h := newTestLobbyHandler()
	path := "/0.9.125/matchmaking/default/"
	first := serveMatchmakingRequest(h, http.MethodPut, path+"ReleasedA/45860",
		"198.51.100.10:32000", map[string]any{"character": "Carbon", "local_ips": []string{"192.168.1.20"}})
	if first.Code != http.StatusOK {
		t.Fatalf("check-in failed: %d %s", first.Code, first.Body.String())
	}
	token := decodeMatchmakingResponse(t, first).Token
	if token == "" {
		t.Fatal("released client needs an ownership token")
	}
	second := serveMatchmakingRequest(h, http.MethodPut, path+"ReleasedB/45861",
		"198.51.100.20:32000", map[string]any{"character": "Silicon"})
	if second.Code != http.StatusOK {
		t.Fatalf("peer check-in failed: %d %s", second.Code, second.Body.String())
	}
	poll := serveMatchmakingRequestWithToken(h, http.MethodGet, path+"ReleasedA/45860",
		"198.51.100.10:32000", nil, token)
	var response struct {
		Status string `json:"status"`
		Match  struct {
			Self struct {
				Character string `json:"character"`
			} `json:"self"`
			Peer struct {
				Character string            `json:"character"`
				Endpoints []json.RawMessage `json:"endpoints"`
			} `json:"peer"`
		} `json:"match"`
	}
	if err := json.Unmarshal(poll.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if poll.Code != http.StatusOK || response.Status != "matched" ||
		response.Match.Self.Character != "Carbon" || response.Match.Peer.Character != "Silicon" ||
		len(response.Match.Peer.Endpoints) == 0 {
		t.Fatalf("released client's flat response contract changed: %d %s", poll.Code, poll.Body.String())
	}
	cancel := serveMatchmakingRequestWithToken(h, http.MethodDelete, path+"ReleasedA/45860",
		"198.51.100.10:32000", nil, token)
	if cancel.Code != http.StatusOK {
		t.Fatalf("released client's token cancellation failed: %d %s", cancel.Code, cancel.Body.String())
	}
}

func TestReleasedClient09125LobbyOwnership(t *testing.T) {
	h := newTestLobbyHandler()
	path := "/0.9.125/lobby/RELEASED/45860"
	checkIn := serveLobbyRequestWithBody(h, http.MethodPut, path, "198.51.100.10:32000", "",
		`{"local_ips":["192.168.1.20"]}`)
	if checkIn.Code != http.StatusOK {
		t.Fatalf("released lobby check-in failed: %d %s", checkIn.Code, checkIn.Body.String())
	}
	token := decodeLobbyResponse(t, checkIn).Token
	for _, owner := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		// Cleanup can arrive over IPv6 even when check-in used IPv4.
		req.RemoteAddr = "[2001:db8::1]:32000"
		if owner {
			req.Header.Set("X-Antistatic-Token", token)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		want := http.StatusForbidden
		if owner {
			want = http.StatusOK
		}
		if response.Code != want {
			t.Fatalf("lobby owner=%v: status=%d want=%d body=%s", owner, response.Code, want, response.Body.String())
		}
	}
	if len(h.Lobbies) != 0 {
		t.Fatal("authorized cross-family cleanup left a stale lobby")
	}
}
