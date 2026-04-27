package main

import (
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

func (l *Lobby) CheckOut(ip string, port int) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	for k, m := range l.Members {
		if m.IP == ip && m.Port == port {
			if len(l.Members) > 1 {
				l.Members[k] = l.Members[len(l.Members)-1]
				l.Members = l.Members[:len(l.Members)-1]
			} else {
				l.Members = nil
			}
			return
		}
	}
}
