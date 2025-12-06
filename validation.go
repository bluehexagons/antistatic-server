package main

import (
	"regexp"
	"strings"
)

var (
	// Allow alphanumeric, hyphens, underscores, and dots
	validLobbyKey = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)
)

// validateLobbyKey checks if a lobby key is valid
func validateLobbyKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	// Prevent directory traversal
	if strings.Contains(key, "..") || strings.Contains(key, "/") || strings.Contains(key, "\\") {
		return false
	}
	return validLobbyKey.MatchString(key)
}

// validatePort checks if a port number is valid
func validatePort(port int) bool {
	return port >= 0 && port <= 65535
}
