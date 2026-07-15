package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const matchmakingTicketTimeout = 30 * time.Second
const matchmakingMatchTimeout = 2 * time.Minute
const matchmakingTagLeaseTimeout = time.Hour
const maxMatchmakingTickets = 20000
const maxMatchmakingMatches = 10000
const maxMatchmakingQueues = 10000
const maxMatchmakingTagLeases = maxMatchmakingTickets
const maxMatchmakingTagLeasesPerIP = 8
const matchCodeQueuePrefix = "code."
const antistaticMatchSelfTagHeader = "X-Antistatic-Match-Self-Tag"
const antistaticMatchPeerTagHeader = "X-Antistatic-Match-Peer-Tag"
const antistaticMatchSelfTagTokenHeader = "X-Antistatic-Match-Self-Tag-Token"

// Long-poll bound for PUT requests that opt in via the ?wait= query
// parameter. Clients use this to learn of a match without burning the
// full client-side polling interval.
const maxMatchmakingLongPoll = 10 * time.Second

type MatchmakingTicket struct {
	ID             string     `json:"id"`
	Version        string     `json:"version"`
	Queue          string     `json:"queue"`
	Endpoints      []Endpoint `json:"endpoints"`
	Token          string     `json:"-"`
	TagToken       string     `json:"-"`
	SelfTag        string     `json:"-"`
	PeerTag        string     `json:"-"`
	Character      string     `json:"character"`
	LocalIPs       []string   `json:"local_ips,omitempty"`
	LocalEndpoints []Endpoint `json:"local_endpoints,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	CheckedIn      time.Time  `json:"checked_in"`
	MatchedID      string     `json:"matched_id"`
	stateChanged   chan struct{}
	changeNotified bool
	reportedEvents uint8
}

type MatchmakingTagLease struct {
	Version   string
	Tag       string
	Token     string
	TicketKey string
	OwnerIP   string
	CheckedIn time.Time
}

type matchmakingTagPair struct {
	Self      string
	Peer      string
	SelfToken string
}

// mergeEndpoint adds (or replaces in-family) a checked-in endpoint, capped
// at maxEndpointsPerMember.
func (t *MatchmakingTicket) mergeEndpoint(ip string, port int) {
	family := ipFamily(ip)
	for i := range t.Endpoints {
		if ipFamily(t.Endpoints[i].IP) == family {
			t.Endpoints[i] = Endpoint{IP: ip, Port: port}
			return
		}
	}
	if len(t.Endpoints) >= maxEndpointsPerMember {
		return
	}
	t.Endpoints = append(t.Endpoints, Endpoint{IP: ip, Port: port})
}

// sharesEndpoint reports whether the two tickets advertise overlapping
// (IP, Port) pairs on any family. Used by the matchmaker to avoid pairing
// a ticket with itself when a client is connected via more than one path.
func (t *MatchmakingTicket) sharesEndpoint(other *MatchmakingTicket) bool {
	for _, a := range t.Endpoints {
		for _, b := range other.Endpoints {
			if a.IP == b.IP && a.Port == b.Port {
				return true
			}
		}
	}
	return false
}

func (t *MatchmakingTicket) tagMatches(other *MatchmakingTicket) bool {
	if t.SelfTag == "" && t.PeerTag == "" && other.SelfTag == "" && other.PeerTag == "" {
		return true
	}
	return t.SelfTag != "" && t.PeerTag != "" && t.SelfTag == other.PeerTag && t.PeerTag == other.SelfTag
}

// notifyStateChangedLocked broadcasts a terminal state transition to every
// long-poll request waiting on this ticket. The handler lock must be held.
func (t *MatchmakingTicket) notifyStateChangedLocked() {
	if t.changeNotified {
		return
	}
	t.changeNotified = true
	if t.stateChanged != nil {
		close(t.stateChanged)
	}
}

type MatchParticipant struct {
	TicketID       string
	Endpoints      []Endpoint
	Character      string
	LocalIPs       []string
	LocalEndpoints []Endpoint
	Role           string
}

type Match struct {
	ID        string
	Version   string
	Queue     string
	CreatedAt time.Time
	Players   [2]MatchParticipant
}

type MatchmakingQueue struct {
	Attempts              int64
	Matches               int64
	AverageMatchWaitMs    int64
	SuccessfulConnections int64
	FailedConnections     int64
	Cancellations         int64
	Expirations           int64
}

type matchmakingPeer struct {
	Endpoints      []Endpoint `json:"endpoints"`
	Character      string     `json:"character"`
	LocalIPs       []string   `json:"local_ips,omitempty"`
	LocalEndpoints []Endpoint `json:"local_endpoints,omitempty"`
}

type matchmakingMatchResponse struct {
	ID   string          `json:"id"`
	Role string          `json:"role"`
	Peer matchmakingPeer `json:"peer"`
	Self matchmakingPeer `json:"self"`
	// MatchedAtMs is the server-clock Unix-millisecond timestamp at which
	// the match was registered. Both peers receive the same value so they
	// can anchor their first hole-punch probe to a shared instant rather
	// than to whenever their own poll happened to learn of the match.
	MatchedAtMs int64 `json:"matched_at_ms,omitempty"`
}

type matchmakingQueueResponse struct {
	PlayersWaiting         int   `json:"players_waiting"`
	OwnWaitMs              int64 `json:"own_wait_ms,omitempty"`
	OldestWaitMs           int64 `json:"oldest_wait_ms,omitempty"`
	QueueAttemptCount      int64 `json:"queue_attempt_count,omitempty"`
	MatchCount             int64 `json:"match_count,omitempty"`
	AverageMatchWaitMs     int64 `json:"average_match_wait_ms,omitempty"`
	MatchSuccessCount      int64 `json:"match_connection_success_count,omitempty"`
	MatchFailureCount      int64 `json:"match_connection_failure_count,omitempty"`
	QueueCancellationCount int64 `json:"queue_cancellation_count,omitempty"`
	QueueExpirationCount   int64 `json:"queue_expiration_count,omitempty"`
}

type matchmakingResponse struct {
	Status    string                    `json:"status"`
	Ticket    string                    `json:"ticket"`
	Endpoints []Endpoint                `json:"endpoints"`
	Token     string                    `json:"token,omitempty"`
	TagToken  string                    `json:"tag_token,omitempty"`
	Match     *matchmakingMatchResponse `json:"match,omitempty"`
	Queue     *matchmakingQueueResponse `json:"queue,omitempty"`
	Events    []recurringQueueEvent     `json:"events,omitempty"`
}

type matchmakingRequest struct {
	Character      string     `json:"character"`
	LocalIPs       []string   `json:"local_ips,omitempty"`
	LocalEndpoints []Endpoint `json:"local_endpoints,omitempty"`
}

type gameReportRequest struct {
	Event string `json:"event"`
}

func validGameReportEvent(event string) bool {
	switch event {
	case "match_connected", "match_connect_failed", "match_handshake_failed", "match_runtime_error":
		return true
	default:
		return false
	}
}

func gameReportEventBit(event string) uint8 {
	switch event {
	case "match_connected":
		return 1 << 0
	case "match_connect_failed":
		return 1 << 1
	case "match_handshake_failed":
		return 1 << 2
	case "match_runtime_error":
		return 1 << 3
	default:
		return 0
	}
}

func matchmakingTicketKey(version, queue, ticket string) string {
	return strings.Join([]string{version, queue, ticket}, "|")
}

func matchmakingQueueKey(version, queue string) string {
	return strings.Join([]string{version, queue}, "|")
}

func matchmakingTagLeaseKey(version, tag string) string {
	return strings.Join([]string{version, strings.ToUpper(tag)}, "|")
}

func normalizeMatchmakingTag(tag string) string {
	return strings.ToUpper(strings.TrimSpace(tag))
}

func canonicalMatchCodeQueue(selfTag, peerTag string) string {
	a := strings.ToLower(selfTag)
	b := strings.ToLower(peerTag)
	if b < a {
		a, b = b, a
	}
	return matchCodeQueuePrefix + a + "-" + b
}

func parseMatchCodeQueue(queue string) (matchmakingTagPair, bool) {
	lower := strings.ToLower(queue)
	if !strings.HasPrefix(lower, matchCodeQueuePrefix) {
		return matchmakingTagPair{}, false
	}
	parts := strings.Split(lower[len(matchCodeQueuePrefix):], "-")
	if len(parts) != 2 {
		return matchmakingTagPair{}, false
	}
	first := normalizeMatchmakingTag(parts[0])
	second := normalizeMatchmakingTag(parts[1])
	if !validateMatchmakingTag(first) || !validateMatchmakingTag(second) {
		return matchmakingTagPair{}, false
	}
	return matchmakingTagPair{Self: first, Peer: second}, true
}

func normalizeMatchmakingQueue(queue string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(queue), matchCodeQueuePrefix) {
		return queue, true
	}
	tags, ok := parseMatchCodeQueue(queue)
	if !ok {
		return "", false
	}
	return canonicalMatchCodeQueue(tags.Self, tags.Peer), true
}

func parseMatchmakingTagHeaders(r *http.Request, queue string) (*matchmakingTagPair, bool) {
	if !strings.HasPrefix(strings.ToLower(queue), matchCodeQueuePrefix) {
		return nil, true
	}
	self := normalizeMatchmakingTag(r.Header.Get(antistaticMatchSelfTagHeader))
	peer := normalizeMatchmakingTag(r.Header.Get(antistaticMatchPeerTagHeader))
	if !validateMatchmakingTag(self) || !validateMatchmakingTag(peer) || self == peer {
		return nil, false
	}
	if canonicalMatchCodeQueue(self, peer) != strings.ToLower(queue) {
		return nil, false
	}
	return &matchmakingTagPair{
		Self:      self,
		Peer:      peer,
		SelfToken: strings.TrimSpace(r.Header.Get(antistaticMatchSelfTagTokenHeader)),
	}, true
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

	response := &matchmakingMatchResponse{
		ID:   m.ID,
		Role: self.Role,
		Self: matchmakingPeer{
			Endpoints: append([]Endpoint(nil), self.Endpoints...),
			Character: self.Character,
		},
		Peer: matchmakingPeer{
			Endpoints: append([]Endpoint(nil), peer.Endpoints...),
			Character: peer.Character,
		},
		MatchedAtMs: m.CreatedAt.UnixMilli(),
	}
	// LAN candidates are only safe to reveal to a peer that shares one of
	// our public IPs (i.e. is plausibly behind the same NAT). With two
	// families per side we check for any overlap; if at least one family
	// matches between self and peer they likely share at least that NAT
	// path.
	if endpointsShareAnyIP(self.Endpoints, peer.Endpoints) {
		if len(peer.LocalIPs) > 0 {
			response.Peer.LocalIPs = append(response.Peer.LocalIPs, peer.LocalIPs...)
		}
		if len(peer.LocalEndpoints) > 0 {
			response.Peer.LocalEndpoints = append(response.Peer.LocalEndpoints, peer.LocalEndpoints...)
		}
	}
	return response
}

func endpointsShareAnyIP(a, b []Endpoint) bool {
	for _, x := range a {
		if x.IP == "" {
			continue
		}
		for _, y := range b {
			if x.IP == y.IP {
				return true
			}
		}
	}
	return false
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

	for _, ticket := range h.Tickets {
		if ticket.MatchedID != "" {
			if _, ok := h.Matches[ticket.MatchedID]; !ok {
				h.deleteTicketLocked(ticket.Version, ticket.Queue, ticket.ID)
			}
			continue
		}

		if !ticket.waiting(now) {
			h.recordQueueExpirationLocked(ticket, now)
			h.deleteTicketLocked(ticket.Version, ticket.Queue, ticket.ID)
		}
	}

	h.cleanupMatchmakingTagLeasesLocked(now)
}

func (h *lobbyHandler) cleanupMatchmakingTagLeasesLocked(now time.Time) {
	h.ensureMatchmakingIndexesLocked()
	for key, lease := range h.TagLeases {
		if now.After(lease.CheckedIn.Add(matchmakingTagLeaseTimeout)) {
			delete(h.TagLeases, key)
		}
	}
}

func (h *lobbyHandler) matchmakingTagLeaseCountForIPLocked(ownerIP string) int {
	if ownerIP == "" {
		return 0
	}
	count := 0
	for _, lease := range h.TagLeases {
		if lease.OwnerIP == ownerIP {
			count++
		}
	}
	return count
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
		response.QueueAttemptCount = stats.Attempts
		response.MatchCount = stats.Matches
		response.AverageMatchWaitMs = stats.AverageMatchWaitMs
		response.MatchSuccessCount = stats.SuccessfulConnections
		response.MatchFailureCount = stats.FailedConnections
		response.QueueCancellationCount = stats.Cancellations
		response.QueueExpirationCount = stats.Expirations
	}

	return response
}

func (h *lobbyHandler) matchmakingTicketResponseLocked(status string, ticket *MatchmakingTicket, now time.Time) *matchmakingResponse {
	return &matchmakingResponse{
		Status:    status,
		Ticket:    ticket.ID,
		Endpoints: append([]Endpoint(nil), ticket.Endpoints...),
		Token:     ticket.Token,
		TagToken:  ticket.TagToken,
		Queue:     h.matchmakingQueueResponseLocked(ticket, now),
		Events:    recurringQueueEvents(now),
	}
}

func (h *lobbyHandler) reserveMatchmakingTagLocked(version, tag, token, ticketKey, ownerIP string, now time.Time) (string, int) {
	if tag == "" {
		return "", http.StatusOK
	}
	h.cleanupMatchmakingTagLeasesLocked(now)
	leaseKey := matchmakingTagLeaseKey(version, tag)
	if existing := h.TagLeases[leaseKey]; existing != nil {
		if token == "" || existing.Token != token {
			return "", http.StatusConflict
		}
		if existing.TicketKey != "" && existing.TicketKey != ticketKey {
			if oldTicket := h.Tickets[existing.TicketKey]; oldTicket != nil && oldTicket.MatchedID == "" {
				h.deleteTicketLocked(oldTicket.Version, oldTicket.Queue, oldTicket.ID)
			}
		}
		existing.TicketKey = ticketKey
		existing.OwnerIP = ownerIP
		existing.CheckedIn = now
		return existing.Token, http.StatusOK
	}
	if h.TagLeases[leaseKey] == nil && len(h.TagLeases) >= maxMatchmakingTagLeases {
		return "", http.StatusServiceUnavailable
	}
	if h.matchmakingTagLeaseCountForIPLocked(ownerIP) >= maxMatchmakingTagLeasesPerIP {
		return "", http.StatusTooManyRequests
	}
	leaseToken, err := generateBearerToken()
	if err != nil {
		return "", http.StatusInternalServerError
	}
	h.TagLeases[leaseKey] = &MatchmakingTagLease{
		Version:   version,
		Tag:       tag,
		Token:     leaseToken,
		TicketKey: ticketKey,
		OwnerIP:   ownerIP,
		CheckedIn: now,
	}
	return leaseToken, http.StatusOK
}

func (h *lobbyHandler) refreshMatchmakingTagLeaseLocked(ticket *MatchmakingTicket, ownerIP string, now time.Time) int {
	if ticket.SelfTag == "" {
		return http.StatusOK
	}
	tagToken, status := h.reserveMatchmakingTagLocked(
		ticket.Version,
		ticket.SelfTag,
		ticket.TagToken,
		matchmakingTicketKey(ticket.Version, ticket.Queue, ticket.ID),
		ownerIP,
		now,
	)
	if status == http.StatusOK {
		ticket.TagToken = tagToken
	}
	return status
}

func (h *lobbyHandler) refreshOrCreateMatchmakingTicketLocked(ticketID, version, queue, ip string, port int, character, token string, tags *matchmakingTagPair, localIPs []string, localEndpoints []Endpoint, now time.Time) (*matchmakingResponse, int) {
	h.ensureMatchmakingIndexesLocked()
	key := matchmakingTicketKey(version, queue, ticketID)
	if existing, ok := h.Tickets[key]; ok {
		if token == "" || token != existing.Token {
			return nil, http.StatusForbidden
		}
		if existing.Character != character {
			return h.matchmakingTicketResponseLocked("conflict", existing, now), http.StatusConflict
		}
		if tags != nil && (existing.SelfTag != tags.Self || existing.PeerTag != tags.Peer) {
			return h.matchmakingTicketResponseLocked("conflict", existing, now), http.StatusConflict
		}
		if status := h.refreshMatchmakingTagLeaseLocked(existing, ip, now); status != http.StatusOK {
			return nil, status
		}

		existing.mergeEndpoint(ip, port)
		existing.LocalIPs = localIPs
		existing.LocalEndpoints = localEndpoints
		existing.CheckedIn = now
		if existing.MatchedID != "" {
			if match, ok := h.Matches[existing.MatchedID]; ok {
				for i := range match.Players {
					if match.Players[i].TicketID == existing.ID {
						match.Players[i].Endpoints = append([]Endpoint(nil), existing.Endpoints...)
						match.Players[i].LocalIPs = append([]string(nil), existing.LocalIPs...)
						match.Players[i].LocalEndpoints = append([]Endpoint(nil), existing.LocalEndpoints...)
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
	if tags != nil {
		tagToken, status := h.reserveMatchmakingTagLocked(version, tags.Self, tags.SelfToken, key, ip, now)
		if status != http.StatusOK {
			return nil, status
		}
		tags.SelfToken = tagToken
	}

	ticket := &MatchmakingTicket{
		ID:             ticketID,
		Version:        version,
		Queue:          queue,
		Endpoints:      []Endpoint{{IP: ip, Port: port}},
		Token:          ticketToken,
		TagToken:       "",
		SelfTag:        "",
		PeerTag:        "",
		Character:      character,
		LocalIPs:       localIPs,
		LocalEndpoints: localEndpoints,
		CreatedAt:      now,
		CheckedIn:      now,
		stateChanged:   make(chan struct{}),
	}
	if tags != nil {
		ticket.TagToken = tags.SelfToken
		ticket.SelfTag = tags.Self
		ticket.PeerTag = tags.Peer
	}

	match, other := h.findCompatibleMatchLocked(ticket, now)
	if match != nil {
		h.Tickets[key] = ticket
		h.addWaitingTicketLocked(ticket)
		h.recordMatchmakingAttemptLocked(version, queue, now)
		h.registerMatchLocked(match, ticket, other)
		resp := h.matchmakingTicketResponseLocked("matched", ticket, now)
		resp.Match = match.responseFor(ticket.ID)
		return resp, http.StatusOK
	}

	h.Tickets[key] = ticket
	h.addWaitingTicketLocked(ticket)
	h.recordMatchmakingAttemptLocked(version, queue, now)
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
		if ticket.sharesEndpoint(other) {
			continue
		}
		if !ticket.tagMatches(other) {
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

	firstParticipant := first.participantForMatch()
	firstParticipant.Role = "host"
	secondParticipant := second.participantForMatch()
	secondParticipant.Role = "client"

	match := &Match{
		ID:        matchmakingMatchID(ticket.Version, ticket.Queue, firstParticipant, secondParticipant),
		Version:   ticket.Version,
		Queue:     ticket.Queue,
		CreatedAt: now,
		Players:   [2]MatchParticipant{firstParticipant, secondParticipant},
	}

	return match, candidate
}

func (t *MatchmakingTicket) participantForMatch() MatchParticipant {
	return MatchParticipant{
		TicketID:       t.ID,
		Endpoints:      append([]Endpoint(nil), t.Endpoints...),
		Character:      t.Character,
		LocalIPs:       append([]string(nil), t.LocalIPs...),
		LocalEndpoints: append([]Endpoint(nil), t.LocalEndpoints...),
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
	waitMs := maxInt64(0, (match.CreatedAt.Sub(first.CreatedAt).Milliseconds()+match.CreatedAt.Sub(second.CreatedAt).Milliseconds())/2)
	h.Metrics.recordMatchmakingMatch(match.CreatedAt, time.Duration(waitMs)*time.Millisecond)
	h.Matches[match.ID] = match
	first.notifyStateChangedLocked()
	second.notifyStateChangedLocked()
	h.Metrics.recordSuccessfulMatch()
}

func (h *lobbyHandler) recordMatchmakingQueueWaitLocked(match *Match, first, second *MatchmakingTicket) {
	stats := h.queueStatsLocked(match.Version, match.Queue)
	if stats == nil {
		return
	}
	waitMs := maxInt64(0, (match.CreatedAt.Sub(first.CreatedAt).Milliseconds()+match.CreatedAt.Sub(second.CreatedAt).Milliseconds())/2)
	stats.Matches++
	stats.AverageMatchWaitMs += (waitMs - stats.AverageMatchWaitMs) / stats.Matches
}

func (h *lobbyHandler) queueStatsLocked(version, queue string) *MatchmakingQueue {
	h.ensureMatchmakingIndexesLocked()
	queueKey := matchmakingQueueKey(version, queue)
	if h.Queues[queueKey] == nil {
		if len(h.Queues) >= maxMatchmakingQueues {
			return nil
		}
		h.Queues[queueKey] = &MatchmakingQueue{}
	}
	return h.Queues[queueKey]
}

func (h *lobbyHandler) recordMatchmakingAttemptLocked(version, queue string, now time.Time) {
	h.Metrics.recordMatchmakingAttempt(now)
	if stats := h.queueStatsLocked(version, queue); stats != nil {
		stats.Attempts++
	}
}

func (h *lobbyHandler) recordQueueCancellationLocked(ticket *MatchmakingTicket, now time.Time) {
	h.Metrics.recordQueueCancellation(now)
	if stats := h.queueStatsLocked(ticket.Version, ticket.Queue); stats != nil {
		stats.Cancellations++
	}
}

func (h *lobbyHandler) recordQueueExpirationLocked(ticket *MatchmakingTicket, now time.Time) {
	h.Metrics.recordQueueExpiration(now)
	if stats := h.queueStatsLocked(ticket.Version, ticket.Queue); stats != nil {
		stats.Expirations++
	}
}

func (h *lobbyHandler) recordMatchmakingOutcomeLocked(ticket *MatchmakingTicket, event string, now time.Time) {
	h.Metrics.recordMatchmakingOutcome(now, event)
	if stats := h.queueStatsLocked(ticket.Version, ticket.Queue); stats != nil {
		if event == "match_connected" {
			stats.SuccessfulConnections++
		} else {
			stats.FailedConnections++
		}
	}
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
		h.recordQueueExpirationLocked(ticket, now)
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
	h.recordQueueCancellationLocked(ticket, time.Now())

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
	if h.TagLeases == nil {
		h.TagLeases = make(map[string]*MatchmakingTagLease)
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
		ticket.notifyStateChangedLocked()
	}
	delete(h.Tickets, key)
}

// parseLongPollWait extracts the ?wait= duration (in seconds) from the
// request query, clamped to [0, maxMatchmakingLongPoll]. Malformed or
// missing values disable long-polling and return 0.
func parseLongPollWait(r *http.Request) time.Duration {
	raw := r.URL.Query().Get("wait")
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0
	}
	wait := time.Duration(seconds) * time.Second
	if wait > maxMatchmakingLongPoll {
		wait = maxMatchmakingLongPoll
	}
	return wait
}

// waitForMatchmakingResult waits for a ticket state notification, the
// deadline, or client disconnect. It rechecks state while holding the lock
// before and after the wait so a match cannot be lost to a notification race.
// The caller's existing (resp, status) is returned when the ticket is removed
// or no match is observed before the deadline.
func (h *lobbyHandler) waitForMatchmakingResult(
	ctx context.Context,
	version, queue, ticketID, token string,
	wait time.Duration,
	fallbackResp *matchmakingResponse,
	fallbackStatus int,
) (*matchmakingResponse, int) {
	h.Mu.Lock()
	resp, status := h.matchmakingStateLocked(version, queue, ticketID, token, time.Now())
	var stateChanged <-chan struct{}
	if resp != nil && resp.Match == nil {
		if ticket := h.Tickets[matchmakingTicketKey(version, queue, ticketID)]; ticket != nil {
			stateChanged = ticket.stateChanged
		}
	}
	h.Mu.Unlock()
	if resp != nil && resp.Match != nil {
		return resp, status
	}
	if stateChanged == nil {
		return fallbackResp, fallbackStatus
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	case <-stateChanged:
	}

	h.Mu.Lock()
	resp, status = h.matchmakingStateLocked(version, queue, ticketID, token, time.Now())
	h.Mu.Unlock()
	if resp != nil && resp.Match != nil {
		return resp, status
	}
	return fallbackResp, fallbackStatus
}

// serveGameReport accepts only a small, authenticated vocabulary of client
// game failures. The event code is aggregated in memory by hour; the ticket,
// address, character, queue, and any client-supplied message are discarded.
func (h *lobbyHandler) serveGameReport(w http.ResponseWriter, r *http.Request, version, queue, ticketID string, port int) {
	if !validateVersion(version) {
		h.respondError(w, "Invalid version", http.StatusBadRequest)
		return
	}
	if !validateMatchmakingQueue(queue) {
		h.respondError(w, "Invalid matchmaking queue", http.StatusBadRequest)
		return
	}
	queue, ok := normalizeMatchmakingQueue(queue)
	if !ok {
		h.respondError(w, "Invalid matchmaking queue", http.StatusBadRequest)
		return
	}
	if !validateMatchmakingTicket(ticketID) {
		h.respondError(w, "Invalid matchmaking ticket", http.StatusBadRequest)
		return
	}
	if !validatePort(port) {
		h.respondError(w, "Invalid port", http.StatusBadRequest)
		return
	}

	if r.Body == nil {
		h.respondError(w, "Invalid game report", http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var report gameReportRequest
	if err := decoder.Decode(&report); err != nil {
		h.respondError(w, "Invalid game report", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || !validGameReportEvent(report.Event) {
		h.respondError(w, "Invalid game report", http.StatusBadRequest)
		return
	}

	token := r.Header.Get(antistaticTokenHeader)
	key := matchmakingTicketKey(version, queue, ticketID)
	h.Mu.Lock()
	t := h.Tickets[key]
	if t == nil {
		h.Mu.Unlock()
		h.respondError(w, "Matchmaking ticket not found", http.StatusNotFound)
		return
	}
	if token == "" || token != t.Token {
		h.Mu.Unlock()
		h.respondError(w, "Invalid matchmaking ticket token", http.StatusForbidden)
		return
	}
	if t.MatchedID == "" {
		h.Mu.Unlock()
		h.respondError(w, "Matchmaking ticket is not matched", http.StatusConflict)
		return
	}
	bit := gameReportEventBit(report.Event)
	if t.reportedEvents&bit != 0 {
		h.Mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	t.reportedEvents |= bit
	h.recordMatchmakingOutcomeLocked(t, report.Event, time.Now())
	h.Mu.Unlock()

	if report.Event != "match_connected" {
		h.Metrics.recordGameError(report.Event)
	}
	w.WriteHeader(http.StatusNoContent)
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
	normalizedQueue, ok := normalizeMatchmakingQueue(queue)
	if !ok {
		h.respondError(w, "Invalid matchmaking queue", http.StatusBadRequest)
		return
	}
	queue = normalizedQueue
	if !validateMatchmakingTicket(ticket) {
		h.respondError(w, "Invalid matchmaking ticket", http.StatusBadRequest)
		return
	}
	if !validatePort(port) {
		h.respondError(w, "Invalid port", http.StatusBadRequest)
		return
	}

	var request matchmakingRequest
	var tags *matchmakingTagPair
	if r.Method == http.MethodPut {
		var ok bool
		tags, ok = parseMatchmakingTagHeaders(r, queue)
		if !ok {
			h.respondError(w, "Invalid matchmaking tag headers", http.StatusBadRequest)
			return
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			h.respondError(w, "Invalid matchmaking request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			h.respondError(w, "Invalid matchmaking request", http.StatusBadRequest)
			return
		}
		if !validateMatchmakingCharacter(request.Character) {
			h.respondError(w, "Invalid matchmaking character", http.StatusBadRequest)
			return
		}
		request.LocalIPs = sanitizeLocalIPs(request.LocalIPs)
		request.LocalEndpoints = sanitizeLocalEndpoints(request.LocalEndpoints, request.LocalIPs)
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
		resp, status = h.refreshOrCreateMatchmakingTicketLocked(ticket, version, queue, ip, port, request.Character, token, tags, request.LocalIPs, request.LocalEndpoints, now)
	case http.MethodDelete:
		resp, status = h.cancelMatchmakingLocked(version, queue, ticket, token)
	default:
		status = http.StatusMethodNotAllowed
	}

	h.Mu.Unlock()

	// Long-poll on PUT: if the ticket is still waiting and the client asked
	// us to wait, block until a match appears, the deadline elapses, or the
	// client disconnects. Use the response token so a ticket's initial PUT,
	// which has no token in the request, can wait too.
	if r.Method == http.MethodPut && status == http.StatusOK && resp != nil && resp.Status == "waiting" {
		if wait := parseLongPollWait(r); wait > 0 {
			resp, status = h.waitForMatchmakingResult(r.Context(), version, queue, ticket, resp.Token, wait, resp, status)
		}
	}

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
		if status == http.StatusConflict {
			h.respondError(w, "Matchmaking tag is already in use", status)
			return
		}
		if status == http.StatusTooManyRequests {
			h.respondError(w, "Too many active match code leases from this address", status)
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
