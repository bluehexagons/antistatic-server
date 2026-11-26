package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountMatchmakingWaitsForDurableAssignment(t *testing.T) {
	const serviceToken = "test_relay_credential_for_account_db"
	const matchID = "rm_00000000000000000000000000000001"
	firstGrant := strings.Repeat("A", 43)
	secondGrant := strings.Repeat("B", 43)
	firstProof := strings.Repeat("X", 43)
	secondProof := strings.Repeat("Y", 43)
	var recovering atomic.Bool
	var mu sync.Mutex
	var registrations []accountAssignment
	var redeems = make(map[string]string)
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+serviceToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		switch r.URL.Path {
		case "/v1/relay/queue-grants/redeem":
			var input struct {
				Grant           string `json:"grant"`
				TicketID        string `json:"ticket_id"`
				Queue           string `json:"queue"`
				CompatibilityID string `json:"compatibility_id"`
			}
			if err := d.Decode(&input); err != nil || input.Queue != accountMatchmakingQueue ||
				input.CompatibilityID != "antistatic-v1" || len(input.TicketID) != 64 {
				t.Error("invalid grant redemption payload")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			previous := redeems[input.Grant]
			if previous == "" {
				redeems[input.Grant] = input.TicketID
			}
			mu.Unlock()
			if previous != "" && previous != input.TicketID {
				w.WriteHeader(http.StatusConflict)
				return
			}
			id := "qg_00000000000000000000000000000001"
			if input.Grant == secondGrant {
				id = "qg_00000000000000000000000000000002"
			}
			_ = json.NewEncoder(w).Encode(accountBinding{ID: id, ExpiresAt: time.Now().Add(time.Minute)})
		case "/v1/relay/matches":
			var input accountAssignment
			if err := d.Decode(&input); err != nil || input.Queue != accountMatchmakingQueue ||
				input.CompatibilityID != "antistatic-v1" || input.Version != "0.11.0" ||
				input.Host.Character != "Carbon" || input.Client.Character != "Silicon" {
				t.Error("invalid match handoff payload")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			registrations = append(registrations, input)
			mu.Unlock()
			if !recovering.Load() {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"match_id": matchID, "expires_at": time.Now().Add(15 * time.Minute)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(db.Close)

	h := newTestLobbyHandler()
	h.Config.Service.CompatibilityID = "antistatic-v1"
	if h.healthResponse().AccountMatchmakingQueue != "" {
		t.Fatal("disabled relay advertises the account queue")
	}
	var err error
	h.Accounts, err = newAccountAPI(db.URL, serviceToken)
	if err != nil {
		t.Fatal(err)
	}
	if h.healthResponse().AccountMatchmakingQueue != accountMatchmakingQueue {
		t.Fatal("configured relay did not advertise its account queue")
	}

	put := func(ticket, queue, proof, grant, token, ip, character string, status int) matchmakingResponse {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"queue": queue, "registration_token": proof, "queue_grant": grant, "port": 45860,
			"client_version": "0.11.0", "compatibility_id": "antistatic-v1",
			"metadata": map[string]string{"character": character},
		})
		if err != nil {
			t.Fatal(err)
		}
		origin := "https://relay.example"
		if ticket == "Insecure" {
			origin = "http://relay.example"
		}
		req := httptest.NewRequest(http.MethodPut, origin+apiPrefix+"/matchmaking/"+ticket, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("ticket %s status = %d, want %d: %s", ticket, w.Code, status, w.Body.String())
		}
		if status != http.StatusOK {
			return matchmakingResponse{}
		}
		return decodeMatchmakingResponse(t, w)
	}
	put("GuestWrongQueue", "default", firstProof, firstGrant, "", "198.51.100.11:1234", "Carbon", http.StatusBadRequest)
	put("Insecure", accountMatchmakingQueue, firstProof, firstGrant, "", "198.51.100.11:1234", "Carbon", http.StatusForbidden)
	put("MissingGrant", accountMatchmakingQueue, firstProof, "", "", "198.51.100.11:1234", "Carbon", http.StatusBadRequest)
	put("MissingProof", accountMatchmakingQueue, "", firstGrant, "", "198.51.100.11:1234", "Carbon", http.StatusBadRequest)
	first := put("First", accountMatchmakingQueue, firstProof, firstGrant, "", "198.51.100.11:1234", "Carbon", http.StatusOK)
	if first.Status != "waiting" {
		t.Fatalf("first account ticket = %s", first.Status)
	}
	put("Replay", accountMatchmakingQueue, secondProof, firstGrant, "", "198.51.100.12:1234", "Silicon", http.StatusConflict)
	second := put("Second", accountMatchmakingQueue, secondProof, secondGrant, "", "198.51.100.12:1234", "Silicon", http.StatusOK)
	recheck := put("First", accountMatchmakingQueue, "", "", first.Token, "198.51.100.11:1234", "Carbon", http.StatusOK)
	for _, pending := range []matchmakingResponse{second, recheck} {
		if pending.Status != "waiting" || pending.Match != nil {
			t.Fatal("relay published an unconfirmed account match")
		}
	}

	guestFirst := put("GuestFirst", "default", "", "", "", "198.51.100.21:1234", "Carbon", http.StatusOK)
	guestSecond := put("GuestSecond", "default", "", "", "", "198.51.100.22:1234", "Silicon", http.StatusOK)
	if guestFirst.Status != "waiting" || guestSecond.Match == nil || guestSecond.Match.AccountMatchID != "" {
		t.Fatal("guest matchmaking depended on the account API")
	}
	for _, match := range h.Matches {
		if match.account != nil {
			if time.Until(match.account.retryAt) < time.Second {
				t.Fatal("relay ignored Retry-After")
			}
			match.account.retryAt = time.Time{}
		}
	}
	recovering.Store(true)
	matched := put("First", accountMatchmakingQueue, "", "", first.Token, "198.51.100.11:1234", "Carbon", http.StatusOK)
	peer := put("Second", accountMatchmakingQueue, "", "", second.Token, "198.51.100.12:1234", "Silicon", http.StatusOK)
	if matched.Match == nil || peer.Match == nil || matched.Match.AccountMatchID != matchID ||
		peer.Match.AccountMatchID != matchID || matched.Match.Role != "host" || peer.Match.Role != "client" {
		t.Fatal("both players must receive the same durable match with complementary roles")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(registrations) != 2 || registrations[0] != registrations[1] {
		t.Fatal("assignment retry did not reuse the same payload")
	}
}

func TestAccountAPIOriginConfiguration(t *testing.T) {
	const token = "test_relay_credential_for_account_db"
	for _, invalid := range []string{"http://example.com", "https://name:secret@example.com", "https://example.com/v1", "https://example.com/?arg=1"} {
		if _, err := newAccountAPI(invalid, token); err == nil {
			t.Errorf("accepted invalid account API origin %q", invalid)
		}
	}
	if client, err := newAccountAPI("", ""); client != nil || err != nil {
		t.Fatal("omitting the configuration should disable account matchmaking")
	}
	if _, err := newAccountAPI("https://example.com", "invalid"); err == nil {
		t.Fatal("accepted an invalid relay token")
	}
}
