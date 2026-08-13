package main

import "time"

// matchmakingMetadata is the deliberately small game-specific part of the
// otherwise generic peer-introduction contract. Source-level adaptations can
// replace this typed struct and its validator without touching matchmaking,
// ownership, or NAT traversal code.
type matchmakingMetadata struct {
	Character string `json:"character"`
}

func validateMatchmakingCharacter(character string) bool {
	return validMatchmakingCharacter.MatchString(character)
}

// ticketsCompatible contains the game-facing compatibility policy. Keeping it
// separate makes alternative queue, version, or metadata rules inexpensive to
// maintain in a fork.
func ticketsCompatible(first, second *MatchmakingTicket, now time.Time, timeout time.Duration) bool {
	return first != second &&
		first.Version == second.Version &&
		first.Queue == second.Queue &&
		second.MatchedID == "" &&
		second.waiting(now, timeout) &&
		!first.sharesEndpoint(second) &&
		first.tagMatches(second)
}

// assignMatchParticipants contains the bundled Antistatic host/client policy.
func assignMatchParticipants(first, second *MatchmakingTicket) (MatchParticipant, MatchParticipant) {
	host := first.participantForMatch()
	host.Role = "host"
	client := second.participantForMatch()
	client.Role = "client"
	return host, client
}
