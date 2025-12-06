package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type oldLobbyHandler struct {
	Mu      sync.RWMutex // guards Lobbies
	Lobbies map[string]*Lobby
	Ticker  *time.Ticker
}

type oldLobbyResponse struct {
	Lobby *Lobby `json:"lobby"`
	IP    string `json:"ip"`
	Port  int    `json:"port"`
}

func (h *oldLobbyHandler) Maintain() {
	maintenance := time.NewTicker(tickInterval)
	h.Ticker = maintenance
	go func() {
		for range maintenance.C {
			var deleted []string
			h.Mu.RLock()
			for k, l := range h.Lobbies {
				l.Clean()
				if len(l.Members) == 0 {
					deleted = append(deleted, k)
				}
			}
			h.Mu.RUnlock()
			if len(deleted) != 0 {
				h.Mu.Lock()
				for _, k := range deleted {
					delete(h.Lobbies, k)
				}
				h.Mu.Unlock()
			}
		}
	}()
}

func (h *oldLobbyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// IPv6 check
	if strings.Count(r.RemoteAddr, ":") >= 2 {
		http.Error(w, "IPv6 not supported", http.StatusBadRequest)
		log.Printf("Request rejected: IPv6 address %s", r.RemoteAddr)
		return
	}

	// Get split path after /lobby/
	info := strings.Split(r.RequestURI[7:], "/")
	if len(info) < 2 {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	key := info[0]
	if !validateLobbyKey(key) {
		http.Error(w, "Invalid lobby key", http.StatusBadRequest)
		log.Printf("Request rejected: invalid lobby key from %s", r.RemoteAddr)
		return
	}

	port, err := strconv.Atoi(info[1])
	if err != nil || !validatePort(port) {
		http.Error(w, "Invalid port", http.StatusBadRequest)
		return
	}

	// Extract client IP
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	log.Printf("Old lobby check-in [%s:%d] key=%s", ip, port, key)

	h.Mu.Lock()
	l, ok := h.Lobbies[key]
	if !ok {
		l = &Lobby{Key: key, Version: "v0.0.0"}
		h.Lobbies[key] = l
		log.Printf("Created old lobby: key=%s", key)
	} else {
		l.Clean()
	}
	l.CheckIn(ip, port)
	h.Mu.Unlock()
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	resp, err := json.Marshal(oldLobbyResponse{
		Lobby: l,
		IP:    ip,
		Port:  port,
	})
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		log.Printf("JSON marshal error: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(resp)
	if err != nil {
		log.Printf("Write error: %v", err)
	}
}

var oldHandler = &oldLobbyHandler{
	Lobbies: map[string]*Lobby{},
}
