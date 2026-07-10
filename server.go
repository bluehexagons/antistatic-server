package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxPathLength = 512
const maxLobbies = 10000

const recentErrorCap = 20

func lobbyStorageKey(version, key string) string {
	return version + "|" + key
}

var serverVersion = resolveServerVersion()
var serverStartTime = time.Now()

func resolveServerVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "unknown"
	}
	return normalizeServerVersion(info.Main.Version)
}

func normalizeServerVersion(version string) string {
	if len(version) > 1 && version[0] == 'v' && version[1] >= '0' && version[1] <= '9' {
		return version[1:]
	}
	return version
}

type recentError struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
	Status  int       `json:"status"`
}

type pathCount struct {
	Path  string `json:"path"`
	Count int64  `json:"count"`
}

type serverMetrics struct {
	lobbiesCreated    atomic.Int64
	successfulMatches atomic.Int64
	clientErrors      atomic.Int64
	serverErrors      atomic.Int64

	recentMu     sync.Mutex
	recentErrors []recentError

	unknownPathMu     sync.Mutex
	unknownPathCounts map[string]int64
}

func (m *serverMetrics) recordLobbyCreated() {
	m.lobbiesCreated.Add(1)
}

func (m *serverMetrics) recordSuccessfulMatch() {
	m.successfulMatches.Add(1)
}

func (m *serverMetrics) recordError(msg string, status int) {
	if status >= 500 {
		m.serverErrors.Add(1)
	} else {
		m.clientErrors.Add(1)
	}
	m.recentMu.Lock()
	m.recentErrors = append(m.recentErrors, recentError{Time: time.Now(), Message: msg, Status: status})
	if len(m.recentErrors) > recentErrorCap {
		m.recentErrors = m.recentErrors[len(m.recentErrors)-recentErrorCap:]
	}
	m.recentMu.Unlock()
}

func (m *serverMetrics) snapshotRecentErrors() []recentError {
	m.recentMu.Lock()
	defer m.recentMu.Unlock()
	if len(m.recentErrors) == 0 {
		return nil
	}
	out := make([]recentError, len(m.recentErrors))
	copy(out, m.recentErrors)
	return out
}

func (m *serverMetrics) recordUnknownPath(path string) {
	const maxUnknownPathKeys = 1000
	if len(path) > 64 {
		path = path[:64]
	}
	m.unknownPathMu.Lock()
	if m.unknownPathCounts == nil {
		m.unknownPathCounts = make(map[string]int64)
	}
	if _, exists := m.unknownPathCounts[path]; exists || len(m.unknownPathCounts) < maxUnknownPathKeys {
		m.unknownPathCounts[path]++
	}
	m.unknownPathMu.Unlock()
}

func (m *serverMetrics) snapshotUnknownPaths() []pathCount {
	const maxUnknownPathsInResponse = 20
	m.unknownPathMu.Lock()
	defer m.unknownPathMu.Unlock()
	if len(m.unknownPathCounts) == 0 {
		return nil
	}
	out := make([]pathCount, 0, len(m.unknownPathCounts))
	for p, c := range m.unknownPathCounts {
		out = append(out, pathCount{Path: p, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > maxUnknownPathsInResponse {
		out = out[:maxUnknownPathsInResponse]
	}
	return out
}

type lobbyHandler struct {
	Mu        sync.RWMutex
	Lobbies   map[string]*Lobby
	Tickets   map[string]*MatchmakingTicket
	Waiting   map[string]map[string]*MatchmakingTicket
	Matches   map[string]*Match
	Queues    map[string]*MatchmakingQueue
	TagLeases map[string]*MatchmakingTagLease
	Metrics   serverMetrics
	Ticker    *time.Ticker
	Done      chan struct{}
	Once      sync.Once
	StopOnce  sync.Once
}

type healthResponse struct {
	Status            string        `json:"status"`
	StartTime         time.Time     `json:"start_time"`
	LobbyCount        int           `json:"lobby_count"`
	TicketCount       int           `json:"ticket_count"`
	MatchCount        int           `json:"match_count"`
	TagLeaseCount     int           `json:"tag_lease_count"`
	LobbiesCreated    int64         `json:"lobbies_created"`
	SuccessfulMatches int64         `json:"successful_matches"`
	ClientErrorCount  int64         `json:"client_error_count"`
	ServerErrorCount  int64         `json:"server_error_count"`
	RecentErrors      []recentError `json:"recent_errors,omitempty"`
	UnknownPaths      []pathCount   `json:"unknown_paths,omitempty"`
	Version           string        `json:"version"`
}

func (h *lobbyHandler) healthResponse() healthResponse {
	h.Mu.RLock()
	resp := healthResponse{
		Status:            "ok",
		StartTime:         serverStartTime,
		LobbyCount:        len(h.Lobbies),
		TicketCount:       len(h.Tickets),
		MatchCount:        len(h.Matches),
		TagLeaseCount:     len(h.TagLeases),
		LobbiesCreated:    h.Metrics.lobbiesCreated.Load(),
		SuccessfulMatches: h.Metrics.successfulMatches.Load(),
		ClientErrorCount:  h.Metrics.clientErrors.Load(),
		ServerErrorCount:  h.Metrics.serverErrors.Load(),
		RecentErrors:      h.Metrics.snapshotRecentErrors(),
		UnknownPaths:      h.Metrics.snapshotUnknownPaths(),
		Version:           serverVersion,
	}
	h.Mu.RUnlock()
	return resp
}

func (h *lobbyHandler) respondError(w http.ResponseWriter, msg string, status int) {
	h.Metrics.recordError(msg, status)
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
	Lobby    *LobbySnapshot `json:"lobby"`
	Endpoint Endpoint       `json:"endpoint"`
	Token    string         `json:"token,omitempty"`
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
		h.Metrics.recordUnknownPath("")
		h.respondError(w, "Invalid path", http.StatusNotFound)
		return
	}

	parts := strings.Split(path, "/")
	version := ""
	info := parts
	if parts[0] != "lobby" {
		if len(parts) < 2 {
			h.Metrics.recordUnknownPath(parts[0])
			h.respondError(w, "Invalid path", http.StatusNotFound)
			return
		}
		version = parts[0]
		if !validateVersion(version) {
			h.Metrics.recordUnknownPath(parts[0])
			h.respondError(w, "Invalid version", http.StatusBadRequest)
			return
		}
		info = parts[1:]
	}
	if len(info) < 1 || info[0] != "lobby" {
		if len(info) < 1 || info[0] != "matchmaking" {
			segment := ""
			if len(info) > 0 {
				segment = info[0]
			}
			h.Metrics.recordUnknownPath(version + "/" + segment)
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
		token := r.Header.Get(antistaticTokenHeader)
		storageKey := lobbyStorageKey(version, key)

		var checkInData lobbyCheckInData
		if r.Method == "PUT" {
			parsed, err := parseLobbyCheckInBody(r)
			if err != nil {
				h.respondError(w, "Invalid lobby request body", http.StatusBadRequest)
				return
			}
			checkInData = parsed
		}

		h.Mu.Lock()
		l, ok := h.Lobbies[storageKey]
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
				h.Lobbies[storageKey] = l
				h.Metrics.recordLobbyCreated()
				slog.Info("Created lobby", "requestID", getRequestID(r), "key", key, "version", version)
			}
		} else {
			l.Clean()
		}

		memberToken := token
		switch r.Method {
		case "PUT":
			var err error
			memberToken, err = l.CheckIn(ip, port, token, checkInData.LocalIPs, checkInData.LocalEndpoints)
			if err == errLobbyMemberTokenMismatch {
				h.Mu.Unlock()
				h.respondError(w, "Invalid lobby member token", http.StatusForbidden)
				return
			}
			if err == errLobbyFull {
				h.Mu.Unlock()
				h.respondError(w, "Lobby full", http.StatusServiceUnavailable)
				return
			}
			if err != nil {
				h.Mu.Unlock()
				h.respondError(w, "Internal error", http.StatusInternalServerError)
				slog.Error("Lobby token generation failed", "requestID", getRequestID(r), "error", err)
				return
			}
		case "DELETE":
			if err := l.CheckOut(ip, port, token); err == errLobbyMemberTokenMismatch {
				h.Mu.Unlock()
				h.respondError(w, "Invalid lobby member token", http.StatusForbidden)
				return
			}
			if len(l.Members) == 0 {
				delete(h.Lobbies, storageKey)
				slog.Info("Lobby emptied", "key", key)
			}
		}

		snapshot := l.SnapshotFor(ip)
		h.Mu.Unlock()

		resp, err := json.Marshal(lobbyResponse{
			Lobby:    snapshot,
			Endpoint: Endpoint{IP: ip, Port: port},
			Token:    memberToken,
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
			h.Metrics.recordError("Write error", http.StatusInternalServerError)
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
	Lobbies:   map[string]*Lobby{},
	Tickets:   map[string]*MatchmakingTicket{},
	Waiting:   map[string]map[string]*MatchmakingTicket{},
	Matches:   map[string]*Match{},
	Queues:    map[string]*MatchmakingQueue{},
	TagLeases: map[string]*MatchmakingTagLease{},
}

const tickInterval = 5 * time.Minute

// lobbyCheckInBody is the optional JSON payload accepted on lobby PUT requests.
// All fields are optional so older clients (and the matchmaking flow that
// reuses this endpoint shape) keep working with no body at all.
type lobbyCheckInBody struct {
	LocalIPs       []string   `json:"local_ips,omitempty"`
	LocalEndpoints []Endpoint `json:"local_endpoints,omitempty"`
}

type lobbyCheckInData struct {
	LocalIPs       []string
	LocalEndpoints []Endpoint
}

// parseLobbyCheckInBody pulls the optional JSON body off a lobby PUT request,
// returning the sanitized list of LAN candidate addresses (RFC 1918 / link-
// local / loopback). Empty body is valid and yields a nil slice.
func parseLobbyCheckInBody(r *http.Request) (lobbyCheckInData, error) {
	if r.Body == nil {
		return lobbyCheckInData{}, nil
	}
	if r.ContentLength == 0 {
		return lobbyCheckInData{}, nil
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body lobbyCheckInBody
	if err := decoder.Decode(&body); err != nil {
		if err == io.EOF {
			return lobbyCheckInData{}, nil
		}
		return lobbyCheckInData{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return lobbyCheckInData{}, errors.New("lobby request body contained trailing JSON")
	}
	localIPs := sanitizeLocalIPs(body.LocalIPs)
	return lobbyCheckInData{
		LocalIPs:       localIPs,
		LocalEndpoints: sanitizeLocalEndpoints(body.LocalEndpoints, localIPs),
	}, nil
}
