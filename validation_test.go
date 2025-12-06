package main

import "testing"

func TestValidateLobbyKey(t *testing.T) {
	tests := []struct {
		key   string
		valid bool
	}{
		{"lobby123", true},
		{"lobby-123", true},
		{"lobby_123", true},
		{"lobby.123", true},
		{"Lobby_Test-123.v1", true},
		{"", false},
		{"this_is_a_very_long_lobby_key_that_exceeds_the_maximum_length_of_64_characters", false},
		{"lobby@test", false},
		{"lobby test", false},
	}

	for _, tt := range tests {
		if got := validateLobbyKey(tt.key); got != tt.valid {
			t.Errorf("validateLobbyKey(%q) = %v, want %v", tt.key, got, tt.valid)
		}
	}
}

func TestValidatePort(t *testing.T) {
	tests := []struct {
		port  int
		valid bool
	}{
		{80, true},
		{443, true},
		{8080, true},
		{0, true},
		{65535, true},
		{-1, false},
		{65536, false},
	}

	for _, tt := range tests {
		if got := validatePort(tt.port); got != tt.valid {
			t.Errorf("validatePort(%d) = %v, want %v", tt.port, got, tt.valid)
		}
	}
}
