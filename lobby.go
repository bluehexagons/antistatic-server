package main

import (
	"errors"
	"sync"
	"time"
)

type Lobby struct {
	Key     string       `json:"key"`
	Mu      sync.RWMutex `json:"-"`
	Members []*Member    `json:"members"`
	Version string       `json:"version"`
}

type LobbySnapshot struct {
	Key     string       `json:"key"`
	Members []MemberView `json:"members"`
	Version string       `json:"version"`
}

const maxLobbyMembers = 128

var errLobbyMemberTokenMismatch = errors.New("lobby member token mismatch")
var errLobbyFull = errors.New("lobby full")

// SnapshotFor renders the lobby for a request originating from `requesterIP`.
// Member.LocalIPs are only revealed to peers that share the member's public
// IP, which keeps RFC 1918 / ULA addresses from leaking to unrelated WAN
// strangers while still letting same-NAT peers tunnel via the LAN.
func (l *Lobby) SnapshotFor(requesterIP string) *LobbySnapshot {
	l.Mu.RLock()
	defer l.Mu.RUnlock()

	views := make([]MemberView, len(l.Members))
	for i, m := range l.Members {
		views[i] = m.View(requesterIP)
	}

	return &LobbySnapshot{
		Key:     l.Key,
		Members: views,
		Version: l.Version,
	}
}

func (l *Lobby) Clean() {
	l.Mu.Lock()
	defer l.Mu.Unlock()

	if l.Members == nil {
		return
	}

	now := time.Now()
	valid := l.Members[:0]
	for _, m := range l.Members {
		if now.After(m.CheckedIn.Add(memberTimeout)) {
			continue
		}
		valid = append(valid, m)
	}
	l.Members = valid
}

// CheckIn registers (or refreshes) a member endpoint. The two-family flow
// works by the client posting once over IPv4 and once over IPv6 with the
// same lobby key + token: the first PUT (token == "") creates the member
// and returns its bearer token; the second PUT (token from the first
// response) merges its source IP into the same member as an additional
// endpoint. Subsequent refresh PUTs re-merge by family — a port mapping
// shift on one family just updates that family's endpoint without
// disturbing the other.
func (l *Lobby) CheckIn(ip string, port int, token string, localIPs []string, localEndpoints []Endpoint) (string, error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	if token != "" {
		for _, m := range l.Members {
			if m.Token != token {
				continue
			}
			m.MergeEndpoint(ip, port)
			m.CheckedIn = time.Now()
			m.LocalIPs = localIPs
			m.LocalEndpoints = localEndpoints
			return m.Token, nil
		}
		return "", errLobbyMemberTokenMismatch
	}
	for _, m := range l.Members {
		if m.MatchesEndpoint(ip, port) {
			return "", errLobbyMemberTokenMismatch
		}
	}
	if len(l.Members) >= maxLobbyMembers {
		return "", errLobbyFull
	}
	memberToken, err := generateBearerToken()
	if err != nil {
		return "", err
	}
	l.Members = append(l.Members, &Member{
		Endpoints:      []Endpoint{{IP: ip, Port: port}},
		LocalIPs:       localIPs,
		LocalEndpoints: localEndpoints,
		Token:          memberToken,
		CheckedIn:      time.Now(),
	})
	return memberToken, nil
}

func (l *Lobby) CheckOut(ip string, port int, token string) error {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	for k, m := range l.Members {
		if !m.MatchesEndpoint(ip, port) {
			continue
		}
		if token == "" || token != m.Token {
			return errLobbyMemberTokenMismatch
		}
		if len(l.Members) > 1 {
			l.Members[k] = l.Members[len(l.Members)-1]
			l.Members = l.Members[:len(l.Members)-1]
		} else {
			l.Members = nil
		}
		return nil
	}
	return nil
}
