package main

import "regexp"

var validLobbyKey = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)

func validateLobbyKey(key string) bool {
	return key != "" && len(key) <= 64 && validLobbyKey.MatchString(key)
}

func validatePort(port int) bool {
	return port >= 0 && port <= 65535
}
