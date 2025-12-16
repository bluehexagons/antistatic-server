package main

import (
	"log"
	"sync"
	"time"
)

type Lobby struct {
	Key     string       `json:"key"`
	Mu      sync.RWMutex `json:"-"`
	Members []*Member    `json:"members"`
	Version string       `json:"version"`
}

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
