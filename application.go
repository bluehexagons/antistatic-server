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
	Game          Config
}

type application struct {
	handler       http.Handler
	reportLimiter *rateLimiter
}

func (app *application) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	app.handler.ServeHTTP(w, r)
}

func (app *application) Close() {
	app.reportLimiter.Stop()
}

func applicationConfigFromEnv(game Config) (applicationConfig, error) {
	config := applicationConfig{
		AdminUsername: os.Getenv("ANTISTATIC_ADMIN_USERNAME"),
		AdminPassword: os.Getenv("ANTISTATIC_ADMIN_PASSWORD"),
		Game:          game,
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

func newApplicationHandler(config applicationConfig, lobby *lobbyHandler) (*application, error) {
	if (config.AdminUsername == "") != (config.AdminPassword == "") {
		return nil, errors.New("admin username and password must both be configured")
	}
	if lobby == nil {
		return nil, errors.New("lobby handler is required")
	}
	if config.Game.SchemaVersion == 0 {
		config.Game = DefaultConfig()
	}
	if err := validateConfig(config.Game); err != nil {
		return nil, err
	}
	lobby.Config = config.Game
	lobby.Store = config.Store
	if config.Store != nil {
		lobby.LastStoreCompaction = time.Now()
	}
	reportLimiter := newRateLimiter(10, 20, time.Minute)
	api := reportAPI{store: config.Store, limiter: reportLimiter, config: config.Game}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { serveHealth(lobby, w, r) })
	mux.HandleFunc("/health.html", func(w http.ResponseWriter, r *http.Request) { serveHealthHTML(lobby, w, r) })
	if config.Game.Features.Events {
		mux.HandleFunc("/events", eventsHandler(config.Game.Events))
	}
	mux.HandleFunc("/robots.txt", robotsHandler)
	if config.Game.Features.CrashReports {
		mux.HandleFunc(apiPrefix+"/reports/crash", postOnly(api.crash))
	}
	if config.Game.Features.FeedbackReports {
		mux.HandleFunc(apiPrefix+"/reports/feedback", postOnly(api.feedback))
	}
	if config.Game.Features.GameplayMetrics {
		mux.HandleFunc(apiPrefix+"/metrics/gameplay", postOnly(api.gameplay))
	}
	if config.Game.Features.PerformanceMetrics {
		mux.HandleFunc(apiPrefix+"/metrics/performance", postOnly(api.performance))
	}
	if config.AdminUsername != "" {
		admin := newAdminHandler(config.Store, config.AdminUsername, config.AdminPassword, config.Game.Service.Name)
		mux.Handle("/admin", admin)
		mux.Handle("/admin/", admin)
	}
	mux.Handle("/", lobby)
	return &application{handler: mux, reportLimiter: reportLimiter}, nil
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
