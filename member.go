package main

import "time"

// Member holds information about a lobby member
type Member struct {
	IP        string    `json:"ip"`   // IP address of the member
	Port      int       `json:"port"` // Port number the member is listening on
	CheckedIn time.Time `json:"-"`    // Last check-in time (not sent to clients)
}

// memberTimeout is the duration after which a member is considered stale
var memberTimeout, _ = time.ParseDuration("30s")

// Stale returns if the member has not checked in within the timeout duration
// Assumes the member's lobby is already read-locked.
func (m *Member) Stale() bool {
	now := time.Now()
	return now.After(m.CheckedIn.Add(memberTimeout))
}
