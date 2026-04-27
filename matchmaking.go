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

type MatchmakingTicket struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	Queue     string    `json:"queue"`
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
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

type matchmakingResponse struct {
	Status string                    `json:"status"`
	Ticket string                    `json:"ticket"`
	IP     string                    `json:"ip"`
	Port   int                       `json:"port"`
	Match  *matchmakingMatchResponse `json:"match,omitempty"`
}

type matchmakingRequest struct {
	Character string `json:"character"`
}

func matchmakingTicketKey(version, queue, ticket string) string {
	return strings.Join([]string{version, queue, ticket}, "|")
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
	for matchID, match := range h.Matches {
		if !match.expired(now) {
			continue
		}

		for _, player := range match.Players {
			delete(h.Tickets, matchmakingTicketKey(match.Version, match.Queue, player.TicketID))
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
			delete(h.Tickets, key)
		}
	}
}

func (h *lobbyHandler) refreshOrCreateMatchmakingTicketLocked(ticketID, version, queue, ip string, port int, character string, now time.Time) (*matchmakingResponse, int) {
	key := matchmakingTicketKey(version, queue, ticketID)
	if existing, ok := h.Tickets[key]; ok {
		if existing.Character != character {
			return &matchmakingResponse{
				Status: "conflict",
				Ticket: existing.ID,
				IP:     existing.IP,
				Port:   existing.Port,
			}, http.StatusConflict
		}

		existing.CheckedIn = now
		if existing.MatchedID != "" {
			if match, ok := h.Matches[existing.MatchedID]; ok {
				return &matchmakingResponse{
					Status: "matched",
					Ticket: existing.ID,
					IP:     existing.IP,
					Port:   existing.Port,
					Match:  match.responseFor(existing.ID),
				}, http.StatusOK
			}
		}

		match, other := h.findCompatibleMatchLocked(existing, now)
		if match != nil {
			h.registerMatchLocked(match, existing, other)
			return &matchmakingResponse{
				Status: "matched",
				Ticket: existing.ID,
				IP:     existing.IP,
				Port:   existing.Port,
				Match:  match.responseFor(existing.ID),
			}, http.StatusOK
		}

		return &matchmakingResponse{
			Status: "waiting",
			Ticket: existing.ID,
			IP:     existing.IP,
			Port:   existing.Port,
		}, http.StatusOK
	}

	ticket := &MatchmakingTicket{
		ID:        ticketID,
		Version:   version,
		Queue:     queue,
		IP:        ip,
		Port:      port,
		Character: character,
		CreatedAt: now,
		CheckedIn: now,
	}

	match, other := h.findCompatibleMatchLocked(ticket, now)
	if match != nil {
		h.Tickets[key] = ticket
		h.registerMatchLocked(match, ticket, other)
		return &matchmakingResponse{
			Status: "matched",
			Ticket: ticket.ID,
			IP:     ticket.IP,
			Port:   ticket.Port,
			Match:  match.responseFor(ticket.ID),
		}, http.StatusOK
	}

	h.Tickets[key] = ticket
	return &matchmakingResponse{
		Status: "waiting",
		Ticket: ticket.ID,
		IP:     ticket.IP,
		Port:   ticket.Port,
	}, http.StatusOK
}

func (h *lobbyHandler) findCompatibleMatchLocked(ticket *MatchmakingTicket, now time.Time) (*Match, *MatchmakingTicket) {
	var candidate *MatchmakingTicket
	for _, other := range h.Tickets {
		if other == ticket {
			continue
		}
		if other.Version != ticket.Version || other.Queue != ticket.Queue {
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
	h.Matches[match.ID] = match
}

func (h *lobbyHandler) matchmakingStateLocked(version, queue, ticketID string, now time.Time) (*matchmakingResponse, int) {
	key := matchmakingTicketKey(version, queue, ticketID)
	ticket, ok := h.Tickets[key]
	if !ok {
		return nil, http.StatusNotFound
	}

	if ticket.MatchedID != "" {
		if match, ok := h.Matches[ticket.MatchedID]; ok {
			return &matchmakingResponse{
				Status: "matched",
				Ticket: ticket.ID,
				IP:     ticket.IP,
				Port:   ticket.Port,
				Match:  match.responseFor(ticket.ID),
			}, http.StatusOK
		}
		delete(h.Tickets, key)
		return nil, http.StatusNotFound
	}

	if !ticket.waiting(now) {
		delete(h.Tickets, key)
		return nil, http.StatusNotFound
	}

	return &matchmakingResponse{
		Status: "waiting",
		Ticket: ticket.ID,
		IP:     ticket.IP,
		Port:   ticket.Port,
	}, http.StatusOK
}

func (h *lobbyHandler) cancelMatchmakingLocked(version, queue, ticketID string) (*matchmakingResponse, int) {
	key := matchmakingTicketKey(version, queue, ticketID)
	ticket, ok := h.Tickets[key]
	if !ok {
		return nil, http.StatusNoContent
	}

	resp := &matchmakingResponse{
		Status: "canceled",
		Ticket: ticket.ID,
		IP:     ticket.IP,
		Port:   ticket.Port,
	}

	if ticket.MatchedID != "" {
		if match, ok := h.Matches[ticket.MatchedID]; ok {
			for _, player := range match.Players {
				delete(h.Tickets, matchmakingTicketKey(match.Version, match.Queue, player.TicketID))
			}
			delete(h.Matches, ticket.MatchedID)
			return resp, http.StatusOK
		}
	}
	delete(h.Tickets, key)
	return resp, http.StatusOK
}

func (h *lobbyHandler) serveMatchmaking(w http.ResponseWriter, r *http.Request, ip, version, queue, ticket string, port int) {
	if !validateVersion(version) {
		http.Error(w, "Invalid version", http.StatusBadRequest)
		return
	}
	if !validateMatchmakingQueue(queue) {
		http.Error(w, "Invalid matchmaking queue", http.StatusBadRequest)
		return
	}
	if !validateMatchmakingTicket(ticket) {
		http.Error(w, "Invalid matchmaking ticket", http.StatusBadRequest)
		return
	}
	if !validatePort(port) {
		http.Error(w, "Invalid port", http.StatusBadRequest)
		return
	}

	var request matchmakingRequest
	if r.Method == http.MethodPut {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "Invalid matchmaking request", http.StatusBadRequest)
			return
		}
		if !validateMatchmakingCharacter(request.Character) {
			http.Error(w, "Invalid matchmaking character", http.StatusBadRequest)
			return
		}
	}

	now := time.Now()

	h.Mu.Lock()
	var resp *matchmakingResponse
	status := http.StatusOK

	switch r.Method {
	case http.MethodGet:
		resp, status = h.matchmakingStateLocked(version, queue, ticket, now)
	case http.MethodPut:
		resp, status = h.refreshOrCreateMatchmakingTicketLocked(ticket, version, queue, ip, port, request.Character, now)
	case http.MethodDelete:
		resp, status = h.cancelMatchmakingLocked(version, queue, ticket)
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
			http.Error(w, "Matchmaking ticket not found", status)
			return
		}
		if status == http.StatusMethodNotAllowed {
			http.Error(w, "Method not allowed", status)
			return
		}
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
