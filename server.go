package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxPathLength = 512
const maxLobbies = 10000

const recentErrorCap = 20
const activityWindowDays = 14
const activityBucketCount = activityWindowDays * 24
const activityPrivacyThreshold = 3
const activityHourCount = 24
const activityBucketDuration = time.Hour
const healthTimestampPrecision = 15 * time.Minute

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
	Time   time.Time `json:"time"`
	Code   string    `json:"code"`
	Status int       `json:"status"`
}

type recentGameError struct {
	Time time.Time `json:"time"`
	Code string    `json:"code"`
}

type activityBucket struct {
	Start              time.Time
	Attempts           int64
	Matches            int64
	MatchWaitTotal     int64
	MatchSuccesses     int64
	MatchFailures      int64
	QueueCancellations int64
	QueueExpirations   int64
}

type activityHour struct {
	HourUTC            int   `json:"hour_utc"`
	Attempts           int64 `json:"attempts,omitempty"`
	Matches            int64 `json:"matches,omitempty"`
	MatchSuccesses     int64 `json:"match_successes,omitempty"`
	MatchFailures      int64 `json:"match_failures,omitempty"`
	QueueCancellations int64 `json:"queue_cancellations,omitempty"`
	QueueExpirations   int64 `json:"queue_expirations,omitempty"`
	AverageMatchWaitMs int64 `json:"average_match_wait_ms,omitempty"`
	Suppressed         bool  `json:"suppressed,omitempty"`
}

type activitySummary struct {
	WindowDays int            `json:"window_days"`
	Timezone   string         `json:"timezone"`
	Hours      []activityHour `json:"hours"`
}

type serverMetrics struct {
	lobbiesCreated     atomic.Int64
	successfulMatches  atomic.Int64
	queueAttempts      atomic.Int64
	matchSuccesses     atomic.Int64
	matchFailures      atomic.Int64
	queueCancellations atomic.Int64
	queueExpirations   atomic.Int64
	httpErrors         atomic.Int64
	gameErrors         atomic.Int64

	recentMu         sync.Mutex
	recentHTTPErrors []recentError
	recentGameErrors []recentGameError

	activityMu sync.Mutex
	activity   [activityBucketCount]activityBucket
}

func (m *serverMetrics) recordLobbyCreated() {
	m.lobbiesCreated.Add(1)
}

func (m *serverMetrics) recordSuccessfulMatch() {
	m.successfulMatches.Add(1)
}

func normalizeMetricCode(message string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(message) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case b.Len() == 0 || b.String()[b.Len()-1] != '_':
			b.WriteByte('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.Trim(b.String(), "_")
}

func healthTime(t time.Time) time.Time {
	return t.UTC().Truncate(healthTimestampPrecision)
}

func (m *serverMetrics) recordHTTPError(msg string, status int) {
	m.httpErrors.Add(1)
	m.recentMu.Lock()
	m.recentHTTPErrors = append(m.recentHTTPErrors, recentError{
		Time:   healthTime(time.Now()),
		Code:   normalizeMetricCode(msg),
		Status: status,
	})
	if len(m.recentHTTPErrors) > recentErrorCap {
		m.recentHTTPErrors = m.recentHTTPErrors[len(m.recentHTTPErrors)-recentErrorCap:]
	}
	m.recentMu.Unlock()
}

func (m *serverMetrics) recordGameError(msg string) {
	m.gameErrors.Add(1)
	m.recentMu.Lock()
	m.recentGameErrors = append(m.recentGameErrors, recentGameError{
		Time: healthTime(time.Now()),
		Code: normalizeMetricCode(msg),
	})
	if len(m.recentGameErrors) > recentErrorCap {
		m.recentGameErrors = m.recentGameErrors[len(m.recentGameErrors)-recentErrorCap:]
	}
	m.recentMu.Unlock()
}

// recordError is retained as the single response-error entry point. Anything
// the client can correct is an HTTP error; 5xx responses are server/game
// failures. Neither category stores request paths or other user input.
func (m *serverMetrics) recordError(msg string, status int) {
	if status >= 500 {
		m.recordGameError(msg)
	} else {
		m.recordHTTPError(msg, status)
	}
}

func (m *serverMetrics) snapshotRecentErrors() ([]recentError, []recentGameError) {
	m.recentMu.Lock()
	defer m.recentMu.Unlock()
	var httpErrors []recentError
	if len(m.recentHTTPErrors) > 0 {
		httpErrors = append([]recentError(nil), m.recentHTTPErrors...)
	}
	var gameErrors []recentGameError
	if len(m.recentGameErrors) > 0 {
		gameErrors = append([]recentGameError(nil), m.recentGameErrors...)
	}
	return httpErrors, gameErrors
}

func (m *serverMetrics) activityIndex(start time.Time) int {
	return int(start.Unix()/int64(activityBucketDuration/time.Hour)) % activityBucketCount
}

func (m *serverMetrics) activityBucketLocked(now time.Time) *activityBucket {
	start := now.UTC().Truncate(activityBucketDuration)
	index := m.activityIndex(start)
	bucket := &m.activity[index]
	if !bucket.Start.Equal(start) {
		*bucket = activityBucket{Start: start}
	}
	return bucket
}

func (m *serverMetrics) recordMatchmakingAttempt(now time.Time) {
	m.queueAttempts.Add(1)
	m.activityMu.Lock()
	m.activityBucketLocked(now).Attempts++
	m.activityMu.Unlock()
}

func (m *serverMetrics) recordMatchmakingMatch(now time.Time, wait time.Duration) {
	m.activityMu.Lock()
	bucket := m.activityBucketLocked(now)
	bucket.Matches++
	bucket.MatchWaitTotal += maxInt64(0, wait.Milliseconds())
	m.activityMu.Unlock()
}

func (m *serverMetrics) recordMatchmakingOutcome(now time.Time, event string) {
	m.activityMu.Lock()
	bucket := m.activityBucketLocked(now)
	switch event {
	case "match_connected":
		m.matchSuccesses.Add(1)
		bucket.MatchSuccesses++
	case "match_connect_failed", "match_handshake_failed", "match_runtime_error":
		m.matchFailures.Add(1)
		bucket.MatchFailures++
	default:
		m.activityMu.Unlock()
		return
	}
	m.activityMu.Unlock()
}

func (m *serverMetrics) recordQueueCancellation(now time.Time) {
	m.queueCancellations.Add(1)
	m.activityMu.Lock()
	m.activityBucketLocked(now).QueueCancellations++
	m.activityMu.Unlock()
}

func (m *serverMetrics) recordQueueExpiration(now time.Time) {
	m.queueExpirations.Add(1)
	m.activityMu.Lock()
	m.activityBucketLocked(now).QueueExpirations++
	m.activityMu.Unlock()
}

func (m *serverMetrics) snapshotActivity(now time.Time) activitySummary {
	now = now.UTC()
	cutoff := now.Add(-activityWindowDays * 24 * time.Hour)
	var attempts [activityHourCount]int64
	var matches [activityHourCount]int64
	var waits [activityHourCount]int64
	var successes [activityHourCount]int64
	var failures [activityHourCount]int64
	var cancellations [activityHourCount]int64
	var expirations [activityHourCount]int64
	m.activityMu.Lock()
	for _, bucket := range m.activity {
		if bucket.Start.IsZero() || bucket.Start.Before(cutoff) || bucket.Start.After(now) {
			continue
		}
		hour := bucket.Start.Hour()
		attempts[hour] += bucket.Attempts
		matches[hour] += bucket.Matches
		waits[hour] += bucket.MatchWaitTotal
		successes[hour] += bucket.MatchSuccesses
		failures[hour] += bucket.MatchFailures
		cancellations[hour] += bucket.QueueCancellations
		expirations[hour] += bucket.QueueExpirations
	}
	m.activityMu.Unlock()

	hours := make([]activityHour, 0, activityHourCount)
	for hour := 0; hour < activityHourCount; hour++ {
		entry := activityHour{HourUTC: hour}
		if attempts[hour] < activityPrivacyThreshold {
			entry.Suppressed = attempts[hour] > 0
		} else {
			entry.Attempts = attempts[hour]
			entry.Matches = matches[hour]
			entry.MatchSuccesses = successes[hour]
			entry.MatchFailures = failures[hour]
			entry.QueueCancellations = cancellations[hour]
			entry.QueueExpirations = expirations[hour]
			if matches[hour] > 0 {
				entry.AverageMatchWaitMs = waits[hour] / matches[hour]
			}
		}
		hours = append(hours, entry)
	}
	return activitySummary{WindowDays: activityWindowDays, Timezone: "UTC", Hours: hours}
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
	Status            string                `json:"status"`
	StartTime         time.Time             `json:"start_time"`
	LobbyCount        int                   `json:"lobby_count"`
	TicketCount       int                   `json:"ticket_count"`
	MatchCount        int                   `json:"match_count"`
	TagLeaseCount     int                   `json:"tag_lease_count"`
	LobbiesCreated    int64                 `json:"lobbies_created"`
	SuccessfulMatches int64                 `json:"successful_matches"`
	MatchCreatedCount int64                 `json:"match_created_count"`
	QueueAttemptCount int64                 `json:"queue_attempt_count"`
	MatchSuccessCount int64                 `json:"match_connection_success_count"`
	MatchFailureCount int64                 `json:"match_connection_failure_count"`
	QueueCancelCount  int64                 `json:"queue_cancellation_count"`
	QueueExpireCount  int64                 `json:"queue_expiration_count"`
	HTTPErrorCount    int64                 `json:"http_error_count"`
	GameErrorCount    int64                 `json:"game_error_count"`
	ClientErrorCount  int64                 `json:"client_error_count"` // deprecated alias
	ServerErrorCount  int64                 `json:"server_error_count"` // deprecated alias
	RecentHTTPErrors  []recentError         `json:"http_errors,omitempty"`
	RecentGameErrors  []recentGameError     `json:"game_errors,omitempty"`
	Activity          activitySummary       `json:"activity"`
	Events            []recurringQueueEvent `json:"events"`
	Version           string                `json:"version"`
}

func (h *lobbyHandler) healthResponse() healthResponse {
	h.Mu.RLock()
	httpErrors, gameErrors := h.Metrics.snapshotRecentErrors()
	httpErrorCount := h.Metrics.httpErrors.Load()
	gameErrorCount := h.Metrics.gameErrors.Load()
	resp := healthResponse{
		Status:            "ok",
		StartTime:         serverStartTime,
		LobbyCount:        len(h.Lobbies),
		TicketCount:       len(h.Tickets),
		MatchCount:        len(h.Matches),
		TagLeaseCount:     len(h.TagLeases),
		LobbiesCreated:    h.Metrics.lobbiesCreated.Load(),
		SuccessfulMatches: h.Metrics.successfulMatches.Load(),
		MatchCreatedCount: h.Metrics.successfulMatches.Load(),
		QueueAttemptCount: h.Metrics.queueAttempts.Load(),
		MatchSuccessCount: h.Metrics.matchSuccesses.Load(),
		MatchFailureCount: h.Metrics.matchFailures.Load(),
		QueueCancelCount:  h.Metrics.queueCancellations.Load(),
		QueueExpireCount:  h.Metrics.queueExpirations.Load(),
		HTTPErrorCount:    httpErrorCount,
		GameErrorCount:    gameErrorCount,
		ClientErrorCount:  httpErrorCount,
		ServerErrorCount:  gameErrorCount,
		RecentHTTPErrors:  httpErrors,
		RecentGameErrors:  gameErrors,
		Activity:          h.Metrics.snapshotActivity(time.Now()),
		Events:            recurringQueueEvents(time.Now()),
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
							slog.Info("Lobby emptied (timeout)")
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
		slog.Error("Request rejected: invalid remote address", "requestID", getRequestID(r))
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodPost:
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

	if r.Method == http.MethodPost {
		if info[0] != "matchmaking" || len(info) != 5 || info[4] != "report" {
			h.respondError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		port, err := strconv.Atoi(info[3])
		if err != nil || !validatePort(port) {
			h.respondError(w, "Invalid port", http.StatusBadRequest)
			return
		}
		h.serveGameReport(w, r, version, info[1], info[2], port)
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
			slog.Error("Request rejected: invalid lobby key", "requestID", getRequestID(r))
			return
		}

		port, err := strconv.Atoi(info[2])
		if err != nil || !validatePort(port) {
			h.respondError(w, "Invalid port", http.StatusBadRequest)
			return
		}

		slog.Info("Lobby request", "requestID", getRequestID(r), "method", r.Method, "version", version)
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
				slog.Info("Created lobby", "requestID", getRequestID(r), "version", version)
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
				slog.Info("Lobby emptied")
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

	slog.Info("Matchmaking request", "requestID", getRequestID(r), "method", r.Method, "version", version)
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
