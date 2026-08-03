package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

type applicationConfig struct {
	Store         *reportStore
	AdminUsername string
	AdminPassword string
}

func applicationConfigFromEnv() (applicationConfig, error) {
	config := applicationConfig{
		AdminUsername: os.Getenv("ANTISTATIC_ADMIN_USERNAME"),
		AdminPassword: os.Getenv("ANTISTATIC_ADMIN_PASSWORD"),
	}
	if (config.AdminUsername == "") != (config.AdminPassword == "") {
		return applicationConfig{}, errors.New("ANTISTATIC_ADMIN_USERNAME and ANTISTATIC_ADMIN_PASSWORD must both be set")
	}
	if dataDir := strings.TrimSpace(os.Getenv("ANTISTATIC_DATA_DIR")); dataDir != "" {
		store, err := newReportStore(dataDir)
		if err != nil {
			return applicationConfig{}, err
		}
		config.Store = store
	}
	return config, nil
}

func newApplicationHandler(config applicationConfig, lobby *lobbyHandler) (http.Handler, error) {
	if (config.AdminUsername == "") != (config.AdminPassword == "") {
		return nil, errors.New("admin username and password must both be configured")
	}
	if lobby == nil {
		return nil, errors.New("lobby handler is required")
	}
	lobby.Store = config.Store
	if config.Store != nil {
		lobby.LastStoreCompaction = time.Now()
	}
	api := reportAPI{store: config.Store, limiter: newRateLimiter(10, 20, time.Minute)}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { serveHealth(lobby, w, r) })
	mux.HandleFunc("/health.html", func(w http.ResponseWriter, r *http.Request) { serveHealthHTML(lobby, w, r) })
	mux.HandleFunc("/events", eventsHandler)
	mux.HandleFunc("/robots.txt", robotsHandler)
	mux.HandleFunc("/{version}/reports/crash", postOnly(api.crash))
	mux.HandleFunc("/{version}/reports/feedback", postOnly(api.feedback))
	mux.HandleFunc("/{version}/metrics/gameplay", postOnly(api.gameplay))
	mux.HandleFunc("/{version}/metrics/performance", postOnly(api.performance))
	if config.AdminUsername != "" {
		admin := newAdminHandler(config.Store, config.AdminUsername, config.AdminPassword)
		mux.Handle("/admin", admin)
		mux.Handle("/admin/", admin)
	}
	mux.Handle("/", lobby)
	return mux, nil
}

func postOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}
