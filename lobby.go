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

func (l *Lobby) CheckIn(ip string, port int, token string, localIPs []string) (string, error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	for _, m := range l.Members {
		if m.IP == ip && m.Port == port {
			if token == "" || token != m.Token {
				return "", errLobbyMemberTokenMismatch
			}
			m.CheckedIn = time.Now()
			m.LocalIPs = localIPs
			return m.Token, nil
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
		IP:        ip,
		Port:      port,
		LocalIPs:  localIPs,
		Token:     memberToken,
		CheckedIn: time.Now(),
	})
	return memberToken, nil
}

func (l *Lobby) CheckOut(ip string, port int, token string) error {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	for k, m := range l.Members {
		if m.IP == ip && m.Port == port {
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
	}
	return nil
}
