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

type lobbyHandler struct {
	Mu      sync.RWMutex // guards Lobbies
	Lobbies map[string]*Lobby
	Ticker  *time.Ticker
}

func (h *lobbyHandler) Maintain() {
	maintenance := time.NewTicker(tickInterval)
	h.Ticker = maintenance
	go func() {
		for range maintenance.C {
			var deleted []string
			handler.Mu.RLock()
			for k, l := range handler.Lobbies {
				l.Clean()
				if len(l.Members) == 0 {
					deleted = append(deleted, k)
				}
			}
			handler.Mu.RUnlock()
			if len(deleted) != 0 {
				handler.Mu.Lock()
				for _, k := range deleted {
					delete(handler.Lobbies, k)
					log.Printf("Lobby emptied (timeout): %s\n", k)
				}
				handler.Mu.Unlock()
			}
		}
	}()
}

type lobbyResponse struct {
	Lobby *Lobby `json:"lobby"`
	IP    string `json:"ip"`
	Port  int    `json:"port"`
}

func (h *lobbyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// IPv6 check
	if strings.Count(r.RemoteAddr, ":") >= 2 {
		http.Error(w, "IPv6 not supported", http.StatusBadRequest)
		log.Printf("Request rejected: IPv6 address %s", r.RemoteAddr)
		return
	}

	// Parse URL path
	if strings.Count(r.RequestURI, "/") < 2 {
		http.Error(w, "Invalid path", http.StatusNotFound)
		return
	}
	version := strings.SplitN(r.RequestURI, "/", 3)[1]

	info := strings.Split(r.RequestURI, "/")[2:]
	if len(info) < 1 || info[0] != "lobby" {
		http.Error(w, "Invalid path", http.StatusNotFound)
		return
	}
	
	if len(info) < 3 {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	key := info[1]
	if !validateLobbyKey(key) {
		http.Error(w, "Invalid lobby key", http.StatusBadRequest)
		log.Printf("Request rejected: invalid lobby key from %s", r.RemoteAddr)
		return
	}

	port, err := strconv.Atoi(info[2])
	if err != nil || !validatePort(port) {
		http.Error(w, "Invalid port", http.StatusBadRequest)
		return
	}

	if r.Method == "OPTIONS" {
		return
	}

	// Extract client IP
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	log.Printf("%s lobby [%s:%d] key=%s version=%s", r.Method, ip, port, key, version)

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
			log.Printf("Created lobby: key=%s version=%s", key, version)
		}
	} else {
		l.Clean()
	}

	switch r.Method {
	case "PUT":
		l.CheckIn(ip, port)
	case "DELETE":
		l.CheckOut(h, ip, port)
	}

	h.Mu.Unlock()
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	resp, err := json.Marshal(lobbyResponse{
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

var handler = &lobbyHandler{
	Lobbies: map[string]*Lobby{},
}
var tickInterval, _ = time.ParseDuration("5m")
