package main

import "time"

type Member struct {
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
	CheckedIn time.Time `json:"-"`
}

var memberTimeout, _ = time.ParseDuration("30s")

func (m *Member) Stale() bool {
	now := time.Now()
	return now.After(m.CheckedIn.Add(memberTimeout))
}
