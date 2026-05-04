package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxPathLength = 512
const maxLobbies = 10000

type serverMetrics struct {
	lobbiesCreated  atomic.Int64
	successfulGames atomic.Int64
	errors          atomic.Int64
}

func (m *serverMetrics) recordLobbyCreated() {
	m.lobbiesCreated.Add(1)
}

func (m *serverMetrics) recordSuccessfulGame() {
	m.successfulGames.Add(1)
}

func (m *serverMetrics) recordError() {
	m.errors.Add(1)
}

type lobbyHandler struct {
	Mu       sync.RWMutex
	Lobbies  map[string]*Lobby
	Tickets  map[string]*MatchmakingTicket
	Waiting  map[string]map[string]*MatchmakingTicket
	Matches  map[string]*Match
	Metrics  serverMetrics
	Ticker   *time.Ticker
	Done     chan struct{}
	Once     sync.Once
	StopOnce sync.Once
}

type healthResponse struct {
	Status                  string `json:"status"`
	LobbyCount              int    `json:"lobby_count"`
	TicketCount             int    `json:"ticket_count"`
	MatchCount              int    `json:"match_count"`
	LobbiesCreated          int64  `json:"lobbies_created"`
	SuccessfulGamesEstimate int64  `json:"successful_games_estimate"`
	ErrorCount              int64  `json:"error_count"`
	Version                 string `json:"version"`
}

func (h *lobbyHandler) healthResponse() healthResponse {
	h.Mu.RLock()
	resp := healthResponse{
		Status:                  "ok",
		LobbyCount:              len(h.Lobbies),
		TicketCount:             len(h.Tickets),
		MatchCount:              len(h.Matches),
		LobbiesCreated:          h.Metrics.lobbiesCreated.Load(),
		SuccessfulGamesEstimate: h.Metrics.successfulGames.Load(),
		ErrorCount:              h.Metrics.errors.Load(),
		Version:                 "1.0.0",
	}
	h.Mu.RUnlock()
	return resp
}

func (h *lobbyHandler) respondError(w http.ResponseWriter, msg string, status int) {
	h.Metrics.recordError()
	http.Error(w, msg, status)
}

func (h *lobbyHandler) Maintain() {
	h.Once.Do(func() {
		maintenance := time.NewTicker(tickInterval)
		h.Ticker = maintenance
		h.Done = make(chan struct{})
		go func() {
			defer maintenance.Stop()
			for {
				select {
				case <-h.Done:
					return
				case <-maintenance.C:
					now := time.Now()
					h.Mu.Lock()
					for k, l := range h.Lobbies {
						l.Clean()
						if len(l.Members) == 0 {
							delete(h.Lobbies, k)
							slog.Info("Lobby emptied (timeout)", "key", k)
						}
					}
					h.cleanupMatchmakingLocked(now)
					h.Mu.Unlock()
				}
			}
		}()
	})
}

func (h *lobbyHandler) Stop() {
	h.StopOnce.Do(func() {
		if h.Done != nil {
			close(h.Done)
		}
	})
}

type lobbyResponse struct {
	Lobby *LobbySnapshot `json:"lobby"`
	IP    string         `json:"ip"`
	Port  int            `json:"port"`
}

func (h *lobbyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Path) > maxPathLength {
		h.respondError(w, "Path too long", http.StatusRequestURITooLong)
		return
	}

	ip := getClientIP(r)
	if ip == "" {
		h.respondError(w, "Invalid remote address", http.StatusBadRequest)
		slog.Error("Request rejected: invalid remote address", "requestID", getRequestID(r), "remoteAddr", r.RemoteAddr)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions:
	default:
		h.respondError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		h.respondError(w, "Invalid path", http.StatusNotFound)
		return
	}

	parts := strings.Split(path, "/")
	version := ""
	info := parts
	if parts[0] != "lobby" {
		if len(parts) < 2 {
			h.respondError(w, "Invalid path", http.StatusNotFound)
			return
		}
		version = parts[0]
		if !validateVersion(version) {
			h.respondError(w, "Invalid version", http.StatusBadRequest)
			return
		}
		info = parts[1:]
	}
	if len(info) < 1 || info[0] != "lobby" {
		if len(info) < 1 || info[0] != "matchmaking" {
			h.respondError(w, "Invalid path", http.StatusNotFound)
			return
		}
	}

	if r.Method == "OPTIONS" {
		return
	}

	if info[0] == "lobby" {
		if len(info) != 3 {
			h.respondError(w, "Missing parameters", http.StatusBadRequest)
			return
		}

		key := info[1]
		if !validateLobbyKey(key) {
			h.respondError(w, "Invalid lobby key", http.StatusBadRequest)
			slog.Error("Request rejected: invalid lobby key", "requestID", getRequestID(r), "remoteAddr", r.RemoteAddr)
			return
		}

		port, err := strconv.Atoi(info[2])
		if err != nil || !validatePort(port) {
			h.respondError(w, "Invalid port", http.StatusBadRequest)
			return
		}

		slog.Info("Lobby request", "requestID", getRequestID(r), "method", r.Method, "ip", ip, "port", port, "key", key, "version", version)

		h.Mu.Lock()
		l, ok := h.Lobbies[key]
		if !ok {
			if r.Method == "DELETE" {
				h.Mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if len(h.Lobbies) >= maxLobbies {
				h.Mu.Unlock()
				h.respondError(w, "Server busy", http.StatusServiceUnavailable)
				return
			}

			l = &Lobby{Key: key, Version: version}

			if r.Method == "PUT" {
				h.Lobbies[key] = l
				h.Metrics.recordLobbyCreated()
				slog.Info("Created lobby", "requestID", getRequestID(r), "key", key, "version", version)
			}
		} else {
			l.Clean()
		}

		switch r.Method {
		case "PUT":
			if !l.CheckIn(ip, port) {
				h.Mu.Unlock()
				h.respondError(w, "Lobby full", http.StatusServiceUnavailable)
				return
			}
		case "DELETE":
			l.CheckOut(ip, port)
			if len(l.Members) == 0 {
				delete(h.Lobbies, key)
				slog.Info("Lobby emptied", "key", key)
			}
		}

		snapshot := l.Snapshot()
		h.Mu.Unlock()

		resp, err := json.Marshal(lobbyResponse{
			Lobby: snapshot,
			IP:    ip,
			Port:  port,
		})
		if err != nil {
			h.respondError(w, "Internal error", http.StatusInternalServerError)
			slog.Error("JSON marshal error", "requestID", getRequestID(r), "error", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write(resp)
		if err != nil {
			slog.Error("Write error", "requestID", getRequestID(r), "error", err)
			h.Metrics.recordError()
		}
		return
	}

	if len(info) != 4 {
		h.respondError(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	queue := info[1]
	ticket := info[2]
	port, err := strconv.Atoi(info[3])
	if err != nil || !validatePort(port) {
		h.respondError(w, "Invalid port", http.StatusBadRequest)
		return
	}

	slog.Info("Matchmaking request", "requestID", getRequestID(r), "method", r.Method, "ip", ip, "port", port, "ticket", ticket, "queue", queue, "version", version)
	h.serveMatchmaking(w, r, ip, version, queue, ticket, port)
}

var handler = &lobbyHandler{
	Lobbies: map[string]*Lobby{},
	Tickets: map[string]*MatchmakingTicket{},
	Waiting: map[string]map[string]*MatchmakingTicket{},
	Matches: map[string]*Match{},
}

const tickInterval = 5 * time.Minute
