package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var host = ""
var tlsHost = ""
var port = 80
var tlsPort = 0
var noHTTP = false
var useTLS = false
var tlsCert = "cert.crt"
var tlsKey = "cert.key"

func init() {
	flag.StringVar(&host, "host", host, "HTTP host to listen on")
	flag.StringVar(&tlsHost, "tlshost", tlsHost, "TLS host to listen on")
	flag.IntVar(&port, "port", port, "HTTP port to listen on")
	flag.IntVar(&tlsPort, "tlsport", tlsPort, "TLS port to listen on")
	flag.BoolVar(&noHTTP, "nohttp", noHTTP, "Disables HTTP")
	flag.BoolVar(&useTLS, "tls", useTLS, "Enables TLS (sets tlsport to 443 if unspecified)")
	flag.StringVar(&tlsCert, "cert", tlsCert, "File to use as TLS cert")
	flag.StringVar(&tlsKey, "key", tlsKey, "File to use as TLS key")
	flag.Parse()
}

// Member holds information about a lobby member
type Member struct {
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
	CheckedIn time.Time `json:"-"`
}

var memberTimeout, _ = time.ParseDuration("30s")

// Stale returns if the member has not checked in within the timeout duration
// Assumes the member's lobby is already read-locked.
func (m *Member) Stale() bool {
	now := time.Now()
	return now.After(m.CheckedIn.Add(memberTimeout))
}

// Lobby holds information about a lobby
type Lobby struct {
	Key     string       `json:"key"`
	Mu      sync.RWMutex `json:"-"` // guards self and members
	Members []*Member    `json:"members"`
}

// Clean will check if any members are stale, then recreate or nils its Members list
func (l *Lobby) Clean() {
	l.Mu.RLock()
	stale := 0
	for _, m := range l.Members {
		if m.Stale() {
			stale++
		}
	}

	l.Mu.RUnlock()
	if stale == 0 {
		return
	}

	l.Mu.Lock()
	defer l.Mu.Unlock()
	if stale == len(l.Members) {
		l.Members = nil
		return
	}

	members := make([]*Member, 0, len(l.Members)-stale)

	for _, m := range l.Members {
		if !m.Stale() {
			members = append(members, m)
		}
	}

	l.Members = members
}

// CheckIn checks a member in, either adding the member or updating its CheckedIn time.
func (l *Lobby) CheckIn(ip string, port int) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	for _, m := range l.Members {
		if m.IP == ip && m.Port == port {
			m.CheckedIn = time.Now()
			return
		}
	}
	l.Members = append(l.Members, &Member{
		IP:        ip,
		Port:      port,
		CheckedIn: time.Now(),
	})
}

type lobbyHandler struct {
	Mu      sync.RWMutex // guards Lobbies
	Lobbies map[string]*Lobby
}

type lobbyResponse struct {
	Lobby *Lobby `json:"lobby"`
	IP    string `json:"ip"`
	Port  int    `json:"port"`
}

func (h *lobbyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	fmt.Printf("Requested lobby [%s:%d] %s\n", ip, port, key)

	h.Mu.Lock()
	l, ok := h.Lobbies[key]
	if !ok {
		l = &Lobby{Key: key}
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

var handler = &lobbyHandler{
	Lobbies: map[string]*Lobby{},
}
var tickInterval, _ = time.ParseDuration("5m")

func main() {
	if tlsPort <= 0 && useTLS {
		tlsPort = 443
	}
	useTLS = tlsPort > 0

	var wg sync.WaitGroup
	http.Handle("/lobby/", handler)
	if !noHTTP {
		log.Println("HTTP listening on port", port)
		wg.Add(1)
		go func() {
			err := http.ListenAndServe(host+":"+strconv.Itoa(port), nil)
			if err != nil {
				log.Println("HTTP listening error:", err)
			}
			wg.Done()
		}()
	}
	if useTLS {
		log.Printf("TLS listening on port %d (cert: %s, key: %s)", tlsPort, tlsCert, tlsKey)
		wg.Add(1)
		go func() {
			err := http.ListenAndServeTLS(tlsHost+":"+strconv.Itoa(tlsPort), tlsCert, tlsKey, nil)
			if err != nil {
				log.Println("TLS listening error:", err)
			}
			wg.Done()
		}()
	}

	maintenance := time.NewTicker(tickInterval)
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

	wg.Wait()
	maintenance.Stop()
	fmt.Println("Done - exiting")
}
