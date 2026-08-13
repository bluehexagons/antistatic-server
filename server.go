package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
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
	Time     time.Time `json:"time"`
	Code     string    `json:"code"`
	ReportID string    `json:"-"`
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
	lastSeparator := false
	for _, c := range strings.ToLower(message) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			lastSeparator = false
		case !lastSeparator:
			b.WriteByte('_')
			lastSeparator = true
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
	m.recordGameErrorWithReportID(msg, "")
}

func (m *serverMetrics) recordGameErrorWithReportID(msg, reportID string) {
	m.gameErrors.Add(1)
	m.recentMu.Lock()
	m.recentGameErrors = append(m.recentGameErrors, recentGameError{
		Time:     healthTime(time.Now()),
		Code:     normalizeMetricCode(msg),
		ReportID: reportID,
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
	bucket.MatchWaitTotal += max(0, wait.Milliseconds())
	m.activityMu.Unlock()
}

func (m *serverMetrics) recordMatchmakingOutcome(now time.Time, event string) {
	m.activityMu.Lock()
	bucket := m.activityBucketLocked(now)
	switch event {
	case "match_connected":
		m.matchSuccesses.Add(1)
		bucket.MatchSuccesses++
	case "match_connect_failed", "match_handshake_failed":
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
	for hour := range activityHourCount {
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
	Config              Config
	Mu                  sync.RWMutex
	Lobbies             map[string]*Lobby
	Tickets             map[string]*MatchmakingTicket
	Waiting             map[string]map[string]*MatchmakingTicket
	Matches             map[string]*Match
	Queues              map[string]*MatchmakingQueue
	TagLeases           map[string]*MatchmakingTagLease
	Metrics             serverMetrics
	Ticker              *time.Ticker
	Done                chan struct{}
	Once                sync.Once
	StopOnce            sync.Once
	MaintainWG          sync.WaitGroup
	Store               *reportStore
	LastStoreCompaction time.Time
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
	QueueAttemptCount int64                 `json:"queue_attempt_count"`
	MatchSuccessCount int64                 `json:"match_connection_success_count"`
	MatchFailureCount int64                 `json:"match_connection_failure_count"`
	QueueCancelCount  int64                 `json:"queue_cancellation_count"`
	QueueExpireCount  int64                 `json:"queue_expiration_count"`
	HTTPErrorCount    int64                 `json:"http_error_count"`
	GameErrorCount    int64                 `json:"game_error_count"`
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
		QueueAttemptCount: h.Metrics.queueAttempts.Load(),
		MatchSuccessCount: h.Metrics.matchSuccesses.Load(),
		MatchFailureCount: h.Metrics.matchFailures.Load(),
		QueueCancelCount:  h.Metrics.queueCancellations.Load(),
		QueueExpireCount:  h.Metrics.queueExpirations.Load(),
		HTTPErrorCount:    httpErrorCount,
		GameErrorCount:    gameErrorCount,
		RecentHTTPErrors:  httpErrors,
		RecentGameErrors:  gameErrors,
		Activity:          h.Metrics.snapshotActivity(time.Now()),
		Events:            h.recurringEvents(time.Now()),
		Version:           serverVersion,
	}
	h.Mu.RUnlock()
	return resp
}

func (h *lobbyHandler) respondError(w http.ResponseWriter, msg string, status int) {
	h.Metrics.recordError(msg, status)
	http.Error(w, msg, status)
}

func (h *lobbyHandler) recurringEvents(now time.Time) []recurringQueueEvent {
	if !h.Config.Features.Events {
		return []recurringQueueEvent{}
	}
	return recurringQueueEvents(h.Config.Events, now)
}

func (h *lobbyHandler) Maintain() {
	h.Once.Do(func() {
		maintenance := time.NewTicker(tickInterval)
		h.Ticker = maintenance
		h.Done = make(chan struct{})
		h.MaintainWG.Go(func() {
			defer maintenance.Stop()
			for {
				select {
				case <-h.Done:
					return
				case <-maintenance.C:
					h.maintainAt(time.Now())
				}
			}
		})
	})
}

func (h *lobbyHandler) maintainAt(now time.Time) {
	h.Mu.Lock()
	for k, l := range h.Lobbies {
		l.Clean(now, h.Config.Timeouts.LobbyMember.Duration())
		if len(l.Members) == 0 {
			delete(h.Lobbies, k)
			slog.Info("Lobby emptied (timeout)")
		}
	}
	h.cleanupMatchmakingLocked(now)
	h.Mu.Unlock()

	if h.Store == nil || (!h.LastStoreCompaction.IsZero() && now.Sub(h.LastStoreCompaction) < storeCompactionInterval) {
		return
	}
	if err := h.Store.Compact(now); err != nil {
		slog.Error("Periodic report compaction failed", "error", err)
		return
	}
	h.LastStoreCompaction = now
}

func (h *lobbyHandler) Stop() {
	h.StopOnce.Do(func() {
		if h.Done != nil {
			close(h.Done)
		}
	})
	h.MaintainWG.Wait()
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

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "v1" {
		h.respondError(w, "Invalid path", http.StatusNotFound)
		return
	}

	switch parts[2] {
	case "lobbies":
		if len(parts) != 4 || (r.Method != http.MethodPut && r.Method != http.MethodDelete) {
			h.respondError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.serveLobby(w, r, ip, parts[3])
	case "matchmaking":
		if len(parts) == 5 && parts[4] == "outcome" && r.Method == http.MethodPost {
			if !h.Config.Features.MatchmakingReports {
				h.respondError(w, "Not found", http.StatusNotFound)
				return
			}
			h.serveGameReport(w, r, parts[3])
			return
		}
		if len(parts) != 4 || (r.Method != http.MethodPut && r.Method != http.MethodDelete) {
			h.respondError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.serveMatchmaking(w, r, ip, parts[3])
	default:
		h.respondError(w, "Invalid path", http.StatusNotFound)
	}
}

var handler = &lobbyHandler{
	Config:    DefaultConfig(),
	Lobbies:   map[string]*Lobby{},
	Tickets:   map[string]*MatchmakingTicket{},
	Waiting:   map[string]map[string]*MatchmakingTicket{},
	Matches:   map[string]*Match{},
	Queues:    map[string]*MatchmakingQueue{},
	TagLeases: map[string]*MatchmakingTagLease{},
}

const tickInterval = 5 * time.Minute
const storeCompactionInterval = 24 * time.Hour

type lobbyCheckInBody struct {
	clientIdentity
	Port           int        `json:"port"`
	LocalIPs       []string   `json:"local_ips,omitempty"`
	LocalEndpoints []Endpoint `json:"local_endpoints,omitempty"`
}

func (h *lobbyHandler) serveLobby(w http.ResponseWriter, r *http.Request, ip, key string) {
	if !validateLobbyKey(key) {
		h.respondError(w, "Invalid lobby key", http.StatusBadRequest)
		return
	}
	var request lobbyCheckInBody
	if status := decodeStrictJSON(w, r, &request); status != 0 {
		writeIngestError(w, status)
		return
	}
	if !validateClientIdentity(w, h.Config, request.clientIdentity) {
		return
	}
	if !validatePeerPort(request.Port) {
		h.respondError(w, "Invalid port", http.StatusBadRequest)
		return
	}
	request.LocalIPs = sanitizeLocalIPs(request.LocalIPs)
	request.LocalEndpoints = sanitizeLocalEndpoints(request.LocalEndpoints, request.LocalIPs)
	token, validAuthorization := bearerToken(r)
	if !validAuthorization {
		h.respondError(w, "Invalid authorization", http.StatusBadRequest)
		return
	}
	storageKey := lobbyStorageKey(request.CompatibilityID, key)

	h.Mu.Lock()
	lobby := h.Lobbies[storageKey]
	if lobby == nil {
		if r.Method == http.MethodDelete {
			h.Mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(h.Lobbies) >= maxLobbies {
			h.Mu.Unlock()
			h.respondError(w, "Server busy", http.StatusServiceUnavailable)
			return
		}
		lobby = &Lobby{Key: key}
		h.Lobbies[storageKey] = lobby
		h.Metrics.recordLobbyCreated()
	} else {
		lobby.Clean(time.Now(), h.Config.Timeouts.LobbyMember.Duration())
	}

	if r.Method == http.MethodDelete {
		if err := lobby.CheckOut(token); err != nil {
			h.Mu.Unlock()
			h.respondError(w, "Invalid lobby member token", http.StatusForbidden)
			return
		}
		if len(lobby.Members) == 0 {
			delete(h.Lobbies, storageKey)
		}
		h.Mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	memberToken, err := lobby.CheckIn(ip, request.Port, token, request.LocalIPs, request.LocalEndpoints)
	if err != nil {
		h.Mu.Unlock()
		switch err {
		case errLobbyMemberTokenMismatch:
			h.respondError(w, "Invalid lobby member token", http.StatusForbidden)
		case errLobbyFull:
			h.respondError(w, "Lobby full", http.StatusServiceUnavailable)
		default:
			h.respondError(w, "Internal error", http.StatusInternalServerError)
		}
		return
	}
	response := lobbyResponse{
		Lobby:    lobby.SnapshotFor(ip),
		Endpoint: Endpoint{IP: ip, Port: request.Port},
		Token:    memberToken,
	}
	h.Mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}
