package main

import (
	"encoding/json"
	"net/http"
)

const apiPrefix = "/api/v1"

type clientIdentity struct {
	ClientVersion   string `json:"client_version"`
	CompatibilityID string `json:"compatibility_id"`
}

type apiErrorResponse struct {
	Error                   string `json:"error"`
	ExpectedCompatibilityID string `json:"expected_compatibility_id,omitempty"`
}

func writeAPIError(w http.ResponseWriter, status int, code string, expectedCompatibilityID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorResponse{
		Error:                   code,
		ExpectedCompatibilityID: expectedCompatibilityID,
	})
}

func validateClientIdentity(w http.ResponseWriter, config Config, identity clientIdentity) bool {
	if !validateVersion(identity.ClientVersion) {
		writeAPIError(w, http.StatusBadRequest, "invalid_client_version", "")
		return false
	}
	if identity.CompatibilityID != config.Service.CompatibilityID {
		writeAPIError(w, http.StatusUpgradeRequired, "incompatible_client", config.Service.CompatibilityID)
		return false
	}
	return true
}
