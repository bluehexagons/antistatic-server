package main

import (
	"log"
	"sync"
	"time"
)

// Lobby holds information about a lobby
type Lobby struct {
	Key     string       `json:"key"`
	Mu      sync.RWMutex `json:"-"` // guards self and members
	Members []*Member    `json:"members"`
	Version string       `json:"version"`
}

// Clean will check if any members are stale, then recreate or nils its Members list
func (l *Lobby) Clean() {
	l.Mu.RLock()
	if l.Members == nil {
		l.Mu.RUnlock()
		return
	}
	stale := 0
	for _, m := range l.Members {
		if m.Stale() {
			stale++
		}
	}

	l.Mu.RUnlock()
	if stale == 0 {
		return
	}

	l.Mu.Lock()
	defer l.Mu.Unlock()
	if stale >= len(l.Members) {
		l.Members = nil
		return
	}

	members := make([]*Member, 0, len(l.Members)-stale)

	for _, m := range l.Members {
		if !m.Stale() {
			members = append(members, m)
		}
	}

	l.Members = members
}

// CheckIn checks a member in, either adding the member or updating its CheckedIn time.
func (l *Lobby) CheckIn(ip string, port int) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	for _, m := range l.Members {
		if m.IP == ip && m.Port == port {
			m.CheckedIn = time.Now()
			return
		}
	}
	l.Members = append(l.Members, &Member{
		IP:        ip,
		Port:      port,
		CheckedIn: time.Now(),
	})
}

// CheckOut checks a member out, removing the member
// Assumes that h.Mu is already locked for writing
func (l *Lobby) CheckOut(h *lobbyHandler, ip string, port int) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	for k, m := range l.Members {
		if m.IP == ip && m.Port == port {
			if len(l.Members) > 1 {
				l.Members[k] = l.Members[len(l.Members)-1]
				l.Members = l.Members[:len(l.Members)-1]
			} else {
				delete(h.Lobbies, l.Key)
				log.Printf("Lobby emptied: %s\n", l.Key)
			}
			return
		}
	}
}
