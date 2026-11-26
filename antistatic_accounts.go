package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

const accountMatchmakingQueue = "account-quickplay"

func secureAccountRequest(r *http.Request, clientIP string) bool {
	return requestIsHTTPS(r) || net.ParseIP(clientIP).IsLoopback()
}

var (
	accountGrantPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	accountRelayTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
	accountBindingPattern    = regexp.MustCompile(`^qg_[a-f0-9]{32}$`)
	accountMatchIDPattern    = regexp.MustCompile(`^rm_[a-f0-9]{32}$`)
)

type accountAPI struct {
	origin   string
	token    string
	client   *http.Client
	inflight chan struct{}
}

func newAccountAPI(origin, token string) (*accountAPI, error) {
	if origin == "" && token == "" {
		return nil, nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") ||
		parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && net.ParseIP(parsed.Hostname()).IsLoopback())) ||
		!accountRelayTokenPattern.MatchString(token) {
		return nil, errors.New("configure ANTISTATIC_ACCOUNT_API_ORIGIN as an HTTPS origin (HTTP only on loopback) and ANTISTATIC_RELAY_TOKEN as 32 to 128 base64url characters")
	}
	return &accountAPI{
		origin: parsed.Scheme + "://" + parsed.Host, token: token,
		client: &http.Client{
			Timeout:       3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		inflight: make(chan struct{}, 8),
	}, nil
}

type accountBinding struct {
	ID        string    `json:"binding_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type accountParticipant struct {
	BindingID string `json:"binding_id"`
	Character string `json:"character"`
}

type accountAssignment struct {
	ID              string             `json:"relay_match_id"`
	Queue           string             `json:"queue"`
	CompatibilityID string             `json:"compatibility_id"`
	Version         string             `json:"version"`
	Host            accountParticipant `json:"host"`
	Client          accountParticipant `json:"client"`
}

type accountPairing struct {
	assignment      accountAssignment
	bindingDeadline time.Time
	retryAt         time.Time
	registering     bool
	matchID         string
}

func (a *accountAPI) post(ctx context.Context, path string, input, output any) (int, time.Duration) {
	select {
	case a.inflight <- struct{}{}:
		defer func() { <-a.inflight }()
	default:
		return http.StatusServiceUnavailable, 0
	}
	body, err := json.Marshal(input)
	if err != nil {
		return http.StatusServiceUnavailable, 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.origin+path, bytes.NewReader(body))
	if err != nil {
		return http.StatusServiceUnavailable, 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return http.StatusServiceUnavailable, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		decoder := json.NewDecoder(io.LimitReader(resp.Body, 8<<10))
		if err := decoder.Decode(output); err != nil {
			return http.StatusServiceUnavailable, 0
		}
	}
	var retry time.Duration
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		retry = time.Duration(min(seconds, 600)) * time.Second
	}
	return resp.StatusCode, retry
}

func (h *lobbyHandler) prepareAccountTicket(ctx context.Context, ticketID, token string, req matchmakingRequest) (accountBinding, int) {
	if h.Accounts == nil {
		return accountBinding{}, http.StatusServiceUnavailable
	}
	if (token == "" && !accountGrantPattern.MatchString(req.RegistrationToken)) ||
		(req.QueueGrant != "" && !accountGrantPattern.MatchString(req.QueueGrant)) {
		return accountBinding{}, http.StatusBadRequest
	}
	key := matchmakingTicketKey(req.CompatibilityID, req.Queue, ticketID)
	h.Mu.RLock()
	ticket := h.Tickets[key]
	exists := ticket != nil && (ticket.MatchedID != "" || ticket.waiting(time.Now(), h.Config.Timeouts.MatchmakingTicket.Duration()))
	h.Mu.RUnlock()
	if exists || token != "" {
		return accountBinding{}, http.StatusOK
	}
	if req.QueueGrant == "" {
		return accountBinding{}, http.StatusBadRequest
	}

	// The DB sees a proof-scoped digest. A retry can recover the binding
	// after a lost response or relay restart with the same ticket proof.
	bindingKey := fmt.Sprintf("%x", matchmakingRegistrationOwnership(key, req.RegistrationToken))
	var binding accountBinding
	status, _ := h.Accounts.post(ctx, "/v1/relay/queue-grants/redeem", map[string]string{
		"grant": req.QueueGrant, "ticket_id": bindingKey, "queue": req.Queue,
		"compatibility_id": req.CompatibilityID,
	}, &binding)
	if status == http.StatusConflict {
		return accountBinding{}, status
	}
	if status != http.StatusOK || !accountBindingPattern.MatchString(binding.ID) || !time.Now().Before(binding.ExpiresAt) {
		return accountBinding{}, http.StatusServiceUnavailable
	}
	return binding, http.StatusOK
}

func newAccountPairing(host, client *MatchmakingTicket) *accountPairing {
	digest := sha256.Sum256([]byte(host.account.ID + client.account.ID))
	return &accountPairing{
		assignment: accountAssignment{
			ID: fmt.Sprintf("%x", digest), Queue: host.Queue,
			CompatibilityID: host.Version, Version: host.ClientVersion,
			Host:   accountParticipant{BindingID: host.account.ID, Character: host.Metadata.Character},
			Client: accountParticipant{BindingID: client.account.ID, Character: client.Metadata.Character},
		},
		bindingDeadline: minTime(host.account.ExpiresAt, client.account.ExpiresAt),
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (h *lobbyHandler) confirmAccountMatch(ctx context.Context, version, ticketID, token string) {
	h.Mu.Lock()
	ticket := h.Tickets[matchmakingTicketKey(version, accountMatchmakingQueue, ticketID)]
	if ticket == nil || ticket.Token != token {
		h.Mu.Unlock()
		return
	}
	match := h.Matches[ticket.MatchedID]
	if match == nil || match.account == nil {
		h.Mu.Unlock()
		return
	}
	pair := match.account
	now := time.Now()
	if pair.matchID != "" || pair.registering || now.Before(pair.retryAt) {
		h.Mu.Unlock()
		return
	}
	if !now.Before(pair.bindingDeadline) {
		h.retireMatchLocked(match.ID, match)
		h.Mu.Unlock()
		return
	}
	pair.registering = true
	h.Mu.Unlock()

	var confirmation struct {
		MatchID   string    `json:"match_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	status, retry := h.Accounts.post(ctx, "/v1/relay/matches", pair.assignment, &confirmation)
	h.Mu.Lock()
	defer h.Mu.Unlock()
	if h.Matches[match.ID] != match {
		return
	}
	pair.registering = false
	now = time.Now()
	if status == http.StatusConflict {
		h.retireMatchLocked(match.ID, match)
		return
	}
	if (status != http.StatusOK && status != http.StatusCreated) || !accountMatchIDPattern.MatchString(confirmation.MatchID) ||
		!now.Before(confirmation.ExpiresAt) {
		if !now.Before(pair.bindingDeadline) {
			h.retireMatchLocked(match.ID, match)
		} else {
			pair.retryAt = now.Add(max(time.Second, retry))
		}
		return
	}
	pair.matchID = confirmation.MatchID
	h.publishMatchLocked(match, now)
}
