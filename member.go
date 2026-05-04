package main

import "time"

type Member struct {
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
	Token     string    `json:"-"`
	CheckedIn time.Time `json:"-"`
}

const memberTimeout = 30 * time.Second

func (m *Member) Stale() bool {
	now := time.Now()
	return now.After(m.CheckedIn.Add(memberTimeout))
}
