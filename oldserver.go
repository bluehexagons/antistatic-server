package main

import (
	"encoding/json"
	"fmt"
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
				}
				handler.Mu.Unlock()
			}
		}
	}()
}

func (h *oldLobbyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// get split path after /lobby/
	info := strings.Split(r.RequestURI[7:], "/")
	if len(info) < 2 {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		return
	}

	key := info[0]
	if key == "" {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		return
	}

	port, err := strconv.Atoi(info[1])
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		return
	}

	if port > 65535 || port < 0 {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		return
	}

	if strings.Count(r.RemoteAddr, ":") >= 2 {
		w.WriteHeader(400)
		w.Write([]byte("Request error: IPv6 not supported\n"))
		return
	}

	// we don't need the request port, and this should never return an error
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	fmt.Printf("Requested old lobby [%s:%d] %s\n", ip, port, key)

	h.Mu.Lock()
	l, ok := h.Lobbies[key]
	if !ok {
		l = &Lobby{Key: key, Version: "v0.0.0"}
		h.Lobbies[key] = l
	} else {
		l.Clean()
	}
	l.CheckIn(ip, port)
	h.Mu.Unlock()
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	resp, err := json.Marshal(lobbyResponse{
		Lobby: l,
		IP:    ip,
		Port:  port,
	})
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("Response error\n"))
		return
	}

	_, err = w.Write(resp)
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("Response error\n"))
	}
}

var oldHandler = &oldLobbyHandler{
	Lobbies: map[string]*Lobby{},
}
