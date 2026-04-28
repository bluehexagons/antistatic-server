package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type lobbyHandler struct {
	Mu       sync.RWMutex
	Lobbies  map[string]*Lobby
	Tickets  map[string]*MatchmakingTicket
	Matches  map[string]*Match
	Ticker   *time.Ticker
	Done     chan struct{}
	Once     sync.Once
	StopOnce sync.Once
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
	ip := getClientIP(r)
	if ip == "" {
		http.Error(w, "Invalid remote address", http.StatusBadRequest)
		slog.Error("Request rejected: invalid remote address", "requestID", getRequestID(r), "remoteAddr", r.RemoteAddr)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions:
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		http.Error(w, "Invalid path", http.StatusNotFound)
		return
	}

	parts := strings.Split(path, "/")
	version := ""
	info := parts
	if parts[0] != "lobby" {
		if len(parts) < 2 {
			http.Error(w, "Invalid path", http.StatusNotFound)
			return
		}
		version = parts[0]
		info = parts[1:]
	}
	if len(info) < 1 || info[0] != "lobby" {
		if len(info) < 1 || info[0] != "matchmaking" {
			http.Error(w, "Invalid path", http.StatusNotFound)
			return
		}
	}

	if r.Method == "OPTIONS" {
		return
	}

	if info[0] == "lobby" {
		if len(info) != 3 {
			http.Error(w, "Missing parameters", http.StatusBadRequest)
			return
		}

		key := info[1]
		if !validateLobbyKey(key) {
			http.Error(w, "Invalid lobby key", http.StatusBadRequest)
			slog.Error("Request rejected: invalid lobby key", "requestID", getRequestID(r), "remoteAddr", r.RemoteAddr)
			return
		}

		port, err := strconv.Atoi(info[2])
		if err != nil || !validatePort(port) {
			http.Error(w, "Invalid port", http.StatusBadRequest)
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

			l = &Lobby{Key: key, Version: version}

			if r.Method == "PUT" {
				h.Lobbies[key] = l
				slog.Info("Created lobby", "requestID", getRequestID(r), "key", key, "version", version)
			}
		} else {
			l.Clean()
		}

		switch r.Method {
		case "PUT":
			l.CheckIn(ip, port)
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
			http.Error(w, "Internal error", http.StatusInternalServerError)
			slog.Error("JSON marshal error", "requestID", getRequestID(r), "error", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write(resp)
		if err != nil {
			slog.Error("Write error", "requestID", getRequestID(r), "error", err)
		}
		return
	}

	if len(info) != 4 {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	queue := info[1]
	ticket := info[2]
	port, err := strconv.Atoi(info[3])
	if err != nil || !validatePort(port) {
		http.Error(w, "Invalid port", http.StatusBadRequest)
		return
	}

	slog.Info("Matchmaking request", "requestID", getRequestID(r), "method", r.Method, "ip", ip, "port", port, "ticket", ticket, "queue", queue, "version", version)
	h.serveMatchmaking(w, r, ip, version, queue, ticket, port)
}

var handler = &lobbyHandler{
	Lobbies: map[string]*Lobby{},
	Tickets: map[string]*MatchmakingTicket{},
	Matches: map[string]*Match{},
}

const tickInterval = 5 * time.Minute
