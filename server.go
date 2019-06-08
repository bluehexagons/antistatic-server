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
	if strings.Count(r.RemoteAddr, ":") >= 2 {
		w.WriteHeader(400)
		w.Write([]byte("Request error: IPv6 unsupported\n"))
		fmt.Printf("Request error: IPv6\n")
		return
	}

	if strings.Count(r.RequestURI, "/") < 2 {
		w.WriteHeader(404)
		w.Write([]byte("path not found\n"))
		fmt.Printf("path not found\n")
		return
	}
	version := strings.SplitN(r.RequestURI, "/", 3)[1]

	info := strings.Split(r.RequestURI, "/")[2:]
	if len(info) < 1 {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		fmt.Printf("Request error: insufficient parameters\n")
		return
	}
	if info[0] != "lobby" {
		// only thing so far
		w.WriteHeader(404)
		w.Write([]byte("path not found\n"))
		fmt.Printf("path not found: %s\n", info[0])
		return
	}
	if len(info) < 3 {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		fmt.Printf("Request error: insufficient parameters\n")
		return
	}
	fmt.Println(info)

	key := info[1]
	if key == "" {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		fmt.Printf("Request error: empty key\n")
		return
	}

	port, err := strconv.Atoi(info[2])
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		fmt.Printf("Request error: non-integer port %s\n", info[2])
		return
	}

	if port > 65535 || port < 0 {
		w.WriteHeader(400)
		w.Write([]byte("Request error\n"))
		fmt.Printf("Request error: invalid port\n")
		return
	}

	if r.Method == "OPTIONS" {
		return
	}

	// we don't need the request port, and this should never return an error
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	fmt.Printf("Requested %s lobby [%s:%d] %s\n", r.Method, ip, port, key)

	h.Mu.Lock()
	l, ok := h.Lobbies[key]
	if !ok {
		l = &Lobby{Key: key, Version: version}
		h.Lobbies[key] = l
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

var handler = &lobbyHandler{
	Lobbies: map[string]*Lobby{},
}
var tickInterval, _ = time.ParseDuration("5m")
