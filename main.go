package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var host = ""
var sslHost = ""
var port = 80
var sslPort = 0
var noHTTP = false
var useSSL = false
var dir = "."
var sslCert = "cert.crt"
var sslKey = "cert.key"

func init() {
	flag.StringVar(&host, "host", host, "HTTP host to listen on")
	flag.StringVar(&sslHost, "sslhost", sslHost, "SSL host to listen on")
	flag.IntVar(&port, "port", port, "HTTP port to listen on")
	flag.IntVar(&sslPort, "sslport", sslPort, "SSL port to listen on")
	flag.BoolVar(&noHTTP, "nohttp", noHTTP, "Disables HTTP")
	flag.BoolVar(&useSSL, "ssl", useSSL, "Enables SSL (sets sslport to 443 if unspecified)")
	flag.StringVar(&sslCert, "cert", sslCert, "File to use as SSL cert")
	flag.StringVar(&sslKey, "key", sslKey, "File to use as SSL key")
	flag.Parse()
}

// Member holds information about a lobby member
type Member struct {
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
	CheckedIn time.Time `json:"-"`
}

// Lobby holds information about a lobby
type Lobby struct {
	Key     string       `json:"key"`
	Mu      sync.RWMutex `json:"-"`
	Members []Member     `json:"members"`
}

type lobbyHandler struct {
	Mu      sync.RWMutex // guards lobbies
	Lobbies map[string]Lobby
}

func (h *lobbyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Mu.Lock()
	defer h.Mu.Unlock()
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

	fmt.Printf("Requested lobby [%s:%d] %s", r.RemoteAddr, port, key)

	l, ok := h.Lobbies[key]
	if !ok {
		h.Lobbies[key] = Lobby{Key: key}
	}

	resp, err := json.Marshal(l)
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

func main() {
	if sslPort <= 0 && useSSL {
		sslPort = 443
	}
	useSSL = sslPort > 0

	var wg sync.WaitGroup
	http.Handle("/lobby/", &lobbyHandler{})
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
	if useSSL {
		log.Printf("SSL listening on port %d (cert: %s, key: %s)", sslPort, sslCert, sslKey)
		go func() {
			err := http.ListenAndServeTLS(sslHost+":"+strconv.Itoa(sslPort), sslCert, sslKey, nil)
			wg.Add(1)
			if err != nil {
				log.Println("SSL listening error:", err)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	fmt.Println("Done - exiting")
}
