package main

import "testing"

func TestValidateLobbyKey(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		valid bool
	}{
		{"Valid alphanumeric", "lobby123", true},
		{"Valid with hyphen", "lobby-123", true},
		{"Valid with underscore", "lobby_123", true},
		{"Valid with dot", "lobby.123", true},
		{"Valid mixed", "Lobby_Test-123.v1", true},
		{"Empty string", "", false},
		{"Too long", "this_is_a_very_long_lobby_key_that_exceeds_the_maximum_length_of_64_characters", false},
		{"Directory traversal", "../secret", false},
		{"Path with slash", "lobby/test", false},
		{"Path with backslash", "lobby\\test", false},
		{"Double dots", "lobby..test", false},
		{"Special chars", "lobby@test", false},
		{"Spaces", "lobby test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateLobbyKey(tt.key)
			if result != tt.valid {
				t.Errorf("validateLobbyKey(%q) = %v, want %v", tt.key, result, tt.valid)
			}
		})
	}
}

func TestValidatePort(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"Valid port 80", 80, true},
		{"Valid port 443", 443, true},
		{"Valid port 8080", 8080, true},
		{"Valid port 0", 0, true},
		{"Valid port 65535", 65535, true},
		{"Invalid negative", -1, false},
		{"Invalid too high", 65536, false},
		{"Invalid way too high", 100000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validatePort(tt.port)
			if result != tt.valid {
				t.Errorf("validatePort(%d) = %v, want %v", tt.port, result, tt.valid)
			}
		})
	}
}
