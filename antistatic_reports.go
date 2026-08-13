package main

import (
	"log/slog"
	"net/http"
)

// gameplayRequest is Antistatic's game-specific telemetry schema. Generic
// crash, feedback, and performance reports remain in reports.go.
type gameplayRequest struct {
	clientIdentity
	EventID           string `json:"event_id"`
	Mode              string `json:"mode"`
	Stage             string `json:"stage"`
	Character         string `json:"character"`
	OpponentCharacter string `json:"opponent_character"`
	Online            *bool  `json:"online"`
	Completed         *bool  `json:"completed"`
	DurationFrames    int    `json:"duration_frames"`
	LocalPlayers      int    `json:"local_players"`
	CPUPlayers        int    `json:"cpu_players"`
	Result            string `json:"result"`
}

func (request gameplayRequest) valid() bool {
	return validEventID(request.EventID) &&
		coarseIdentifierPattern.MatchString(request.Mode) &&
		coarseIdentifierPattern.MatchString(request.Stage) &&
		coarseIdentifierPattern.MatchString(request.Character) &&
		coarseIdentifierPattern.MatchString(request.OpponentCharacter) &&
		request.Online != nil && request.Completed != nil &&
		request.DurationFrames >= 0 && request.DurationFrames <= 10000000 &&
		request.LocalPlayers >= 0 && request.LocalPlayers <= 8 &&
		request.CPUPlayers >= 0 && request.CPUPlayers <= 8 &&
		oneOf(request.Result, "win", "loss", "draw", "unknown", "quit")
}

func (api reportAPI) gameplay(w http.ResponseWriter, r *http.Request) {
	if !api.validateRequest(w, r) {
		return
	}
	var request gameplayRequest
	if status := decodeStrictJSON(w, r, &request); status != 0 {
		writeIngestError(w, status)
		return
	}
	if !validateClientIdentity(w, api.config, request.clientIdentity) {
		return
	}
	if !request.valid() {
		writeIngestError(w, http.StatusBadRequest)
		return
	}
	if _, err := api.store.appendGameplay(request, request.ClientVersion); err != nil {
		slog.Error("Gameplay metric storage failed", "error", err)
		writeIngestError(w, http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
