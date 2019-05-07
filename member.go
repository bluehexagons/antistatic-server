package main

import "time"

// Member holds information about a lobby member
type Member struct {
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
	CheckedIn time.Time `json:"-"`
}

var memberTimeout, _ = time.ParseDuration("30s")

// Stale returns if the member has not checked in within the timeout duration
// Assumes the member's lobby is already read-locked.
func (m *Member) Stale() bool {
	now := time.Now()
	return now.After(m.CheckedIn.Add(memberTimeout))
}
