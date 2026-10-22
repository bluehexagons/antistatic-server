package main

import (
	"errors"
	"net/http"
	"os"
	"time"
)

func securityTextConfigFromEnv() (securityTextConfig, error) {
	return parseSecurityTextConfig(os.Getenv("ANTISTATIC_SECURITY_CONTACT"), os.Getenv("ANTISTATIC_SECURITY_EXPIRES"))
}

// Keep the released client's fallback routes separate from public site paths.
func newApplicationHandler(security securityTextConfig, lobby *lobbyHandler) (*http.ServeMux, error) {
	if lobby == nil {
		return nil, errors.New("lobby handler is required")
	}
	if err := security.validate(time.Now()); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	registerAntistaticPublicRoutes(mux)
	mux.HandleFunc("/.well-known/security.txt", security.serveHTTP)
	mux.HandleFunc("/security.txt", security.serveHTTP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { serveHealth(lobby, w, r) })
	mux.HandleFunc("/robots.txt", robotsHandler)
	mux.Handle("/", lobby)
	return mux, nil
}
