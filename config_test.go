package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "game.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBundledAntistaticConfigMatchesDefaults(t *testing.T) {
	config, err := LoadConfig("config/antistatic.json")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config, DefaultConfig()) {
		t.Fatalf("bundled config differs from defaults:\nloaded:  %#v\ndefault: %#v", config, DefaultConfig())
	}
}

func TestLoadConfigOverlaysDefaults(t *testing.T) {
	path := writeTestConfig(t, `{
  "service": {"name": "Another Game"},
  "features": {"events": false},
  "timeouts": {"matchmaking_ticket": "45s"},
  "events": []
}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Service.Name != "Another Game" || config.Features.Events || !config.Features.CrashReports {
		t.Fatalf("profile overlay = %#v", config)
	}
	if config.Timeouts.MatchmakingTicket.Duration() != 45*time.Second {
		t.Fatalf("ticket timeout = %s, want 45s", config.Timeouts.MatchmakingTicket.Duration())
	}
	if len(config.Events) != 0 {
		t.Fatalf("events = %#v, want explicit empty list", config.Events)
	}
}

func TestLoadConfigReportsSpecificInvalidField(t *testing.T) {
	tests := []struct {
		contents string
		want     string
	}{
		{contents: `{"unknown": true}`, want: "unknown field"},
		{contents: `{"timeouts":{"matchmaking_ticket":"6m"}}`, want: "timeouts.matchmaking_ticket"},
		{contents: `{"events":[{"id":"weekly","name":"Weekly","region":"Global","weekday":"Funday","start_hour_utc":1,"start_minute_utc":0,"duration":"1h"}]}`, want: "invalid weekday"},
		{contents: `{"events":[{"id":"weekly","name":"Weekly","region":"Global","weekday":"Sunday","start_hour_utc":1,"start_minute_utc":0,"duration":"30s"}]}`, want: "whole number of minutes"},
		{contents: `{"events":[{"id":"weekly","name":"Weekly","region":"Global","weekday":"Sunday","start_hour_utc":1,"start_minute_utc":0,"duration":"90s"}]}`, want: "whole number of minutes"},
		{contents: `{"service":{"name":"  "}}`, want: "service.name"},
	}
	for _, test := range tests {
		_, err := LoadConfig(writeTestConfig(t, test.contents))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("LoadConfig(%s) error = %v, want containing %q", test.contents, err, test.want)
		}
	}
}

func TestConfiguredTimeoutsControlMaintenance(t *testing.T) {
	now := time.Now()
	config := DefaultConfig()
	config.Timeouts.LobbyMember = ConfigDuration(time.Second)
	config.Timeouts.MatchmakingTicket = ConfigDuration(time.Second)
	config.Timeouts.MatchmakingMatch = ConfigDuration(time.Second)
	config.Timeouts.MatchmakingTagLease = ConfigDuration(time.Second)
	handler := newTestLobbyHandler()
	handler.Config = config
	handler.Lobbies["1|room"] = &Lobby{
		Key: "room", Members: []*Member{{CheckedIn: now.Add(-2 * time.Second)}},
	}
	ticket := &MatchmakingTicket{
		ID: "ticket", Version: "1", Queue: "default", CheckedIn: now.Add(-2 * time.Second),
	}
	handler.Tickets[matchmakingTicketKey(ticket.Version, ticket.Queue, ticket.ID)] = ticket
	handler.Matches["match"] = &Match{ID: "match", CreatedAt: now.Add(-2 * time.Second)}
	handler.TagLeases["1|TAG"] = &MatchmakingTagLease{CheckedIn: now.Add(-2 * time.Second)}

	handler.maintainAt(now)
	if len(handler.Lobbies) != 0 || len(handler.Tickets) != 0 || len(handler.Matches) != 0 || len(handler.TagLeases) != 0 {
		t.Fatalf(
			"configured cleanup retained lobbies/tickets/matches/leases = %d/%d/%d/%d",
			len(handler.Lobbies), len(handler.Tickets), len(handler.Matches), len(handler.TagLeases),
		)
	}
}
