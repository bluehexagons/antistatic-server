package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const matchmakingTicketTimeout = 30 * time.Second
const matchmakingMatchTimeout = 2 * time.Minute
const maxMatchmakingTickets = 20000
const maxMatchmakingMatches = 10000
const maxMatchmakingQueues = 10000

type MatchmakingTicket struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	Queue     string    `json:"queue"`
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
	Token     string    `json:"-"`
	Character string    `json:"character"`
	CreatedAt time.Time `json:"created_at"`
	CheckedIn time.Time `json:"checked_in"`
	MatchedID string    `json:"matched_id"`
}

type MatchParticipant struct {
	TicketID  string
	IP        string
	Port      int
	Character string
	Role      string
}

type Match struct {
	ID        string
	Version   string
	Queue     string
	CreatedAt time.Time
	Players   [2]MatchParticipant
}

type MatchmakingQueue struct {
	Matches            int64
	AverageMatchWaitMs int64
}

type matchmakingPeer struct {
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Character string `json:"character"`
}

type matchmakingMatchResponse struct {
	ID   string          `json:"id"`
	Role string          `json:"role"`
	Peer matchmakingPeer `json:"peer"`
	Self matchmakingPeer `json:"self"`
}

type matchmakingQueueResponse struct {
	PlayersWaiting     int   `json:"players_waiting"`
	OwnWaitMs          int64 `json:"own_wait_ms,omitempty"`
	OldestWaitMs       int64 `json:"oldest_wait_ms,omitempty"`
	MatchCount         int64 `json:"match_count,omitempty"`
	AverageMatchWaitMs int64 `json:"average_match_wait_ms,omitempty"`
}

type matchmakingResponse struct {
	Status string                    `json:"status"`
	Ticket string                    `json:"ticket"`
	IP     string                    `json:"ip"`
	Port   int                       `json:"port"`
	Token  string                    `json:"token,omitempty"`
	Match  *matchmakingMatchResponse `json:"match,omitempty"`
	Queue  *matchmakingQueueResponse `json:"queue,omitempty"`
}

type matchmakingRequest struct {
	Character string `json:"character"`
}

func matchmakingTicketKey(version, queue, ticket string) string {
	return strings.Join([]string{version, queue, ticket}, "|")
}

func matchmakingQueueKey(version, queue string) string {
	return strings.Join([]string{version, queue}, "|")
}

func matchmakingMatchID(version, queue string, first, second MatchParticipant) string {
	return fmt.Sprintf("%s|%s|%s|%s", version, queue, first.TicketID, second.TicketID)
}

func (t *MatchmakingTicket) waiting(now time.Time) bool {
	return t.MatchedID == "" && now.Before(t.CheckedIn.Add(matchmakingTicketTimeout))
}

func (m *Match) expired(now time.Time) bool {
	return now.After(m.CreatedAt.Add(matchmakingMatchTimeout))
}

func (m *Match) participant(ticketID string) (self MatchParticipant, peer MatchParticipant, ok bool) {
	switch {
	case m.Players[0].TicketID == ticketID:
		return m.Players[0], m.Players[1], true
	case m.Players[1].TicketID == ticketID:
		return m.Players[1], m.Players[0], true
	default:
		return MatchParticipant{}, MatchParticipant{}, false
	}
}

func (m *Match) responseFor(ticketID string) *matchmakingMatchResponse {
	self, peer, ok := m.participant(ticketID)
	if !ok {
		return nil
	}

	return &matchmakingMatchResponse{
		ID:   m.ID,
		Role: self.Role,
		Self: matchmakingPeer{
			IP:        self.IP,
			Port:      self.Port,
			Character: self.Character,
		},
		Peer: matchmakingPeer{
			IP:        peer.IP,
			Port:      peer.Port,
			Character: peer.Character,
		},
	}
}

func (h *lobbyHandler) cleanupMatchmakingLocked(now time.Time) {
	h.ensureMatchmakingIndexesLocked()
	for matchID, match := range h.Matches {
		if !match.expired(now) {
			continue
		}

		for _, player := range match.Players {
			h.deleteTicketLocked(match.Version, match.Queue, player.TicketID)
		}
		delete(h.Matches, matchID)
	}

	for key, ticket := range h.Tickets {
		if ticket.MatchedID != "" {
			if _, ok := h.Matches[ticket.MatchedID]; !ok {
				delete(h.Tickets, key)
			}
			continue
		}

		if !ticket.waiting(now) {
			h.deleteTicketLocked(ticket.Version, ticket.Queue, ticket.ID)
		}
	}
}

func (h *lobbyHandler) matchmakingQueueResponseLocked(ticket *MatchmakingTicket, now time.Time) *matchmakingQueueResponse {
	h.ensureMatchmakingIndexesLocked()
	queueKey := matchmakingQueueKey(ticket.Version, ticket.Queue)
	response := &matchmakingQueueResponse{}

	for _, other := range h.Waiting[queueKey] {
		if other.MatchedID != "" || !other.waiting(now) {
			continue
		}
		response.PlayersWaiting++
		waitMs := maxInt64(0, now.Sub(other.CreatedAt).Milliseconds())
		if waitMs > response.OldestWaitMs {
			response.OldestWaitMs = waitMs
		}
	}

	if ticket.MatchedID == "" {
		response.OwnWaitMs = maxInt64(0, now.Sub(ticket.CreatedAt).Milliseconds())
		if response.OwnWaitMs > response.OldestWaitMs {
			response.OldestWaitMs = response.OwnWaitMs
		}
	}

	if stats := h.Queues[queueKey]; stats != nil {
		response.MatchCount = stats.Matches
		response.AverageMatchWaitMs = stats.AverageMatchWaitMs
	}

	return response
}

func (h *lobbyHandler) matchmakingTicketResponseLocked(status string, ticket *MatchmakingTicket, now time.Time) *matchmakingResponse {
	return &matchmakingResponse{
		Status: status,
		Ticket: ticket.ID,
		IP:     ticket.IP,
		Port:   ticket.Port,
		Token:  ticket.Token,
		Queue:  h.matchmakingQueueResponseLocked(ticket, now),
	}
}

func (h *lobbyHandler) refreshOrCreateMatchmakingTicketLocked(ticketID, version, queue, ip string, port int, character, token string, now time.Time) (*matchmakingResponse, int) {
	h.ensureMatchmakingIndexesLocked()
	key := matchmakingTicketKey(version, queue, ticketID)
	if existing, ok := h.Tickets[key]; ok {
		if token == "" || token != existing.Token {
			return nil, http.StatusForbidden
		}
		if existing.Character != character {
			return h.matchmakingTicketResponseLocked("conflict", existing, now), http.StatusConflict
		}

		existing.IP = ip
		existing.Port = port
		existing.CheckedIn = now
		if existing.MatchedID != "" {
			if match, ok := h.Matches[existing.MatchedID]; ok {
				for i := range match.Players {
					if match.Players[i].TicketID == existing.ID {
						match.Players[i].IP = ip
						match.Players[i].Port = port
					}
				}
				resp := h.matchmakingTicketResponseLocked("matched", existing, now)
				resp.Match = match.responseFor(existing.ID)
				return resp, http.StatusOK
			}
		}

		match, other := h.findCompatibleMatchLocked(existing, now)
		if match != nil {
			h.registerMatchLocked(match, existing, other)
			resp := h.matchmakingTicketResponseLocked("matched", existing, now)
			resp.Match = match.responseFor(existing.ID)
			return resp, http.StatusOK
		}

		return h.matchmakingTicketResponseLocked("waiting", existing, now), http.StatusOK
	}

	if len(h.Tickets) >= maxMatchmakingTickets || len(h.Matches) >= maxMatchmakingMatches {
		return nil, http.StatusServiceUnavailable
	}

	ticketToken, err := generateBearerToken()
	if err != nil {
		return nil, http.StatusInternalServerError
	}

	ticket := &MatchmakingTicket{
		ID:        ticketID,
		Version:   version,
		Queue:     queue,
		IP:        ip,
		Port:      port,
		Token:     ticketToken,
		Character: character,
		CreatedAt: now,
		CheckedIn: now,
	}

	match, other := h.findCompatibleMatchLocked(ticket, now)
	if match != nil {
		h.Tickets[key] = ticket
		h.addWaitingTicketLocked(ticket)
		h.registerMatchLocked(match, ticket, other)
		resp := h.matchmakingTicketResponseLocked("matched", ticket, now)
		resp.Match = match.responseFor(ticket.ID)
		return resp, http.StatusOK
	}

	h.Tickets[key] = ticket
	h.addWaitingTicketLocked(ticket)
	return h.matchmakingTicketResponseLocked("waiting", ticket, now), http.StatusOK
}

func (h *lobbyHandler) findCompatibleMatchLocked(ticket *MatchmakingTicket, now time.Time) (*Match, *MatchmakingTicket) {
	var candidate *MatchmakingTicket
	for _, other := range h.Waiting[matchmakingQueueKey(ticket.Version, ticket.Queue)] {
		if other == ticket {
			continue
		}
		if other.MatchedID != "" || !other.waiting(now) {
			continue
		}
		if other.IP == ticket.IP && other.Port == ticket.Port {
			continue
		}

		if candidate == nil || other.CreatedAt.Before(candidate.CreatedAt) || (other.CreatedAt.Equal(candidate.CreatedAt) && other.ID < candidate.ID) {
			candidate = other
		}
	}
	if candidate == nil {
		return nil, nil
	}

	first, second := ticket, candidate
	if candidate.CreatedAt.Before(ticket.CreatedAt) || (candidate.CreatedAt.Equal(ticket.CreatedAt) && candidate.ID < ticket.ID) {
		first, second = candidate, ticket
	}

	match := &Match{
		ID:        matchmakingMatchID(ticket.Version, ticket.Queue, first.participantForMatch(), second.participantForMatch()),
		Version:   ticket.Version,
		Queue:     ticket.Queue,
		CreatedAt: now,
		Players: [2]MatchParticipant{
			{
				TicketID:  first.ID,
				IP:        first.IP,
				Port:      first.Port,
				Character: first.Character,
				Role:      "host",
			},
			{
				TicketID:  second.ID,
				IP:        second.IP,
				Port:      second.Port,
				Character: second.Character,
				Role:      "client",
			},
		},
	}

	return match, candidate
}

func (t *MatchmakingTicket) participantForMatch() MatchParticipant {
	return MatchParticipant{
		TicketID:  t.ID,
		IP:        t.IP,
		Port:      t.Port,
		Character: t.Character,
	}
}

func (h *lobbyHandler) registerMatchLocked(match *Match, first, second *MatchmakingTicket) {
	first.MatchedID = match.ID
	second.MatchedID = match.ID
	first.CheckedIn = match.CreatedAt
	second.CheckedIn = match.CreatedAt
	h.removeWaitingTicketLocked(first)
	h.removeWaitingTicketLocked(second)
	h.recordMatchmakingQueueWaitLocked(match, first, second)
	h.Matches[match.ID] = match
	h.Metrics.recordSuccessfulGame()
}

func (h *lobbyHandler) recordMatchmakingQueueWaitLocked(match *Match, first, second *MatchmakingTicket) {
	queueKey := matchmakingQueueKey(match.Version, match.Queue)
	if h.Queues[queueKey] == nil {
		if len(h.Queues) >= maxMatchmakingQueues {
			return
		}
		h.Queues[queueKey] = &MatchmakingQueue{}
	}
	stats := h.Queues[queueKey]
	waitMs := maxInt64(0, (match.CreatedAt.Sub(first.CreatedAt).Milliseconds()+match.CreatedAt.Sub(second.CreatedAt).Milliseconds())/2)
	stats.Matches++
	stats.AverageMatchWaitMs += (waitMs - stats.AverageMatchWaitMs) / stats.Matches
}

func (h *lobbyHandler) matchmakingStateLocked(version, queue, ticketID string, token string, now time.Time) (*matchmakingResponse, int) {
	h.ensureMatchmakingIndexesLocked()
	key := matchmakingTicketKey(version, queue, ticketID)
	ticket, ok := h.Tickets[key]
	if !ok {
		return nil, http.StatusNotFound
	}
	if token == "" || token != ticket.Token {
		return nil, http.StatusForbidden
	}

	if ticket.MatchedID != "" {
		if match, ok := h.Matches[ticket.MatchedID]; ok {
			resp := h.matchmakingTicketResponseLocked("matched", ticket, now)
			resp.Match = match.responseFor(ticket.ID)
			return resp, http.StatusOK
		}
		h.deleteTicketLocked(version, queue, ticketID)
		return nil, http.StatusNotFound
	}

	if !ticket.waiting(now) {
		h.deleteTicketLocked(version, queue, ticketID)
		return nil, http.StatusNotFound
	}

	return h.matchmakingTicketResponseLocked("waiting", ticket, now), http.StatusOK
}

func (h *lobbyHandler) cancelMatchmakingLocked(version, queue, ticketID string, token string) (*matchmakingResponse, int) {
	h.ensureMatchmakingIndexesLocked()
	key := matchmakingTicketKey(version, queue, ticketID)
	ticket, ok := h.Tickets[key]
	if !ok {
		return nil, http.StatusNoContent
	}
	if token == "" || token != ticket.Token {
		return nil, http.StatusForbidden
	}

	resp := h.matchmakingTicketResponseLocked("canceled", ticket, time.Now())

	if ticket.MatchedID != "" {
		if match, ok := h.Matches[ticket.MatchedID]; ok {
			for _, player := range match.Players {
				h.deleteTicketLocked(match.Version, match.Queue, player.TicketID)
			}
			delete(h.Matches, ticket.MatchedID)
			return resp, http.StatusOK
		}
	}
	h.deleteTicketLocked(version, queue, ticketID)
	return resp, http.StatusOK
}

func (h *lobbyHandler) ensureMatchmakingIndexesLocked() {
	if h.Waiting == nil {
		h.Waiting = make(map[string]map[string]*MatchmakingTicket)
	}
	if h.Queues == nil {
		h.Queues = make(map[string]*MatchmakingQueue)
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (h *lobbyHandler) addWaitingTicketLocked(ticket *MatchmakingTicket) {
	if ticket.MatchedID != "" {
		return
	}
	queueKey := matchmakingQueueKey(ticket.Version, ticket.Queue)
	if h.Waiting[queueKey] == nil {
		h.Waiting[queueKey] = make(map[string]*MatchmakingTicket)
	}
	h.Waiting[queueKey][ticket.ID] = ticket
}

func (h *lobbyHandler) removeWaitingTicketLocked(ticket *MatchmakingTicket) {
	queueKey := matchmakingQueueKey(ticket.Version, ticket.Queue)
	waiting := h.Waiting[queueKey]
	if waiting == nil {
		return
	}
	delete(waiting, ticket.ID)
	if len(waiting) == 0 {
		delete(h.Waiting, queueKey)
	}
}

func (h *lobbyHandler) deleteTicketLocked(version, queue, ticketID string) {
	key := matchmakingTicketKey(version, queue, ticketID)
	if ticket, ok := h.Tickets[key]; ok {
		h.removeWaitingTicketLocked(ticket)
	}
	delete(h.Tickets, key)
}

func (h *lobbyHandler) serveMatchmaking(w http.ResponseWriter, r *http.Request, ip, version, queue, ticket string, port int) {
	if !validateVersion(version) {
		h.respondError(w, "Invalid version", http.StatusBadRequest)
		return
	}
	if !validateMatchmakingQueue(queue) {
		h.respondError(w, "Invalid matchmaking queue", http.StatusBadRequest)
		return
	}
	if !validateMatchmakingTicket(ticket) {
		h.respondError(w, "Invalid matchmaking ticket", http.StatusBadRequest)
		return
	}
	if !validatePort(port) {
		h.respondError(w, "Invalid port", http.StatusBadRequest)
		return
	}

	var request matchmakingRequest
	if r.Method == http.MethodPut {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			h.respondError(w, "Invalid matchmaking request", http.StatusBadRequest)
			return
		}
		if !validateMatchmakingCharacter(request.Character) {
			h.respondError(w, "Invalid matchmaking character", http.StatusBadRequest)
			return
		}
	}

	now := time.Now()
	token := r.Header.Get(antistaticTokenHeader)

	h.Mu.Lock()
	var resp *matchmakingResponse
	status := http.StatusOK

	switch r.Method {
	case http.MethodGet:
		resp, status = h.matchmakingStateLocked(version, queue, ticket, token, now)
	case http.MethodPut:
		resp, status = h.refreshOrCreateMatchmakingTicketLocked(ticket, version, queue, ip, port, request.Character, token, now)
	case http.MethodDelete:
		resp, status = h.cancelMatchmakingLocked(version, queue, ticket, token)
	default:
		status = http.StatusMethodNotAllowed
	}

	h.Mu.Unlock()

	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	if resp == nil {
		if status == http.StatusNotFound {
			h.respondError(w, "Matchmaking ticket not found", status)
			return
		}
		if status == http.StatusServiceUnavailable {
			h.respondError(w, "Server busy", status)
			return
		}
		if status == http.StatusMethodNotAllowed {
			h.respondError(w, "Method not allowed", status)
			return
		}
		if status == http.StatusForbidden {
			h.respondError(w, "Invalid matchmaking ticket token", status)
			return
		}
		h.respondError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(resp)
	if err != nil {
		h.respondError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
