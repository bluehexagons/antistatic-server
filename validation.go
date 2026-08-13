package main

import "regexp"

var validLobbyKey = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)
var validMatchmakingTicket = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)
var validMatchmakingQueue = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)
var validMatchmakingTag = regexp.MustCompile(`^[A-Z0-9]{4,8}$`)
var validMatchmakingCharacter = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9 _\-\.]{0,63}$`)
var validVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.]{0,31}$`)

func validateLobbyKey(key string) bool {
	return validLobbyKey.MatchString(key)
}

func validateMatchmakingTicket(ticket string) bool {
	return validMatchmakingTicket.MatchString(ticket)
}

func validateMatchmakingQueue(queue string) bool {
	return validMatchmakingQueue.MatchString(queue)
}

func validateMatchmakingTag(tag string) bool {
	return validMatchmakingTag.MatchString(tag)
}

func validateVersion(version string) bool {
	return validVersion.MatchString(version)
}

func validatePort(port int) bool {
	return port >= 0 && port <= 65535
}

// validatePeerPort excludes the deployment-only ephemeral listener port. A
// coordination response must always advertise a usable remote endpoint.
func validatePeerPort(port int) bool {
	return port >= 1 && port <= 65535
}
