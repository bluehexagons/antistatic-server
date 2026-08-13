package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

var host = ""
var tlsHost = ""
var port = 80
var tlsPort = 0
var noHTTP = false
var useTLS = false
var tlsCert = "cert.crt"
var tlsKey = "cert.key"
var autocertDomain = ""
var autocertCacheDir = "certs"
var requestTimeout = 30 * time.Second
var shutdownTimeout = 30 * time.Second
var readTimeout = 15 * time.Second
var writeTimeout = 15 * time.Second
var idleTimeout = 60 * time.Second
var trustProxy = false
var trustedProxyCIDRs = ""
var stunHost = ""
var stunPort = 0
var configPath = ""

func listenAddress(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func validateServerPorts(httpPort, tlsPort, stunPort int) error {
	ports := []struct {
		name string
		port int
	}{
		{name: "HTTP", port: httpPort},
		{name: "TLS", port: tlsPort},
		{name: "STUN", port: stunPort},
	}
	for _, candidate := range ports {
		if !validatePort(candidate.port) {
			return fmt.Errorf("%s port must be between 0 and 65535", candidate.name)
		}
	}
	return nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	serveHealth(handler, w, r)
}

func serveHealth(lobby *lobbyHandler, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(lobby.healthResponse()); err != nil {
		slog.Error("Health JSON response failed", "error", err)
	}
}

var healthHTMLTemplate = template.Must(template.New("health").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>{{.ServiceName}} server health</title>
</head>
<body>
  <h1>{{.ServiceName}} server health</h1>
  <p>Status: <strong>{{.Status}}</strong> · Version: {{.Version}}</p>
  <p>Started: {{.StartTime.UTC}}</p>

  <h2>Live state</h2>
  <table border="1">
    <tr><th>Item</th><th>Count</th></tr>
    <tr><td>Lobbies</td><td>{{.LobbyCount}}</td></tr>
    <tr><td>Tickets</td><td>{{.TicketCount}}</td></tr>
    <tr><td>Matches</td><td>{{.MatchCount}}</td></tr>
    <tr><td>Tag leases</td><td>{{.TagLeaseCount}}</td></tr>
  </table>

  <h2>Totals</h2>
  <table border="1">
    <tr><th>Metric</th><th>Count</th></tr>
    <tr><td>Lobby creations</td><td>{{.LobbiesCreated}}</td></tr>
    <tr><td>Successful matches</td><td>{{.SuccessfulMatches}}</td></tr>
    <tr><td>Queue attempts</td><td>{{.QueueAttemptCount}}</td></tr>
    <tr><td>Match connection successes</td><td>{{.MatchSuccessCount}}</td></tr>
    <tr><td>Match connection failures</td><td>{{.MatchFailureCount}}</td></tr>
    <tr><td>Queue cancellations</td><td>{{.QueueCancelCount}}</td></tr>
    <tr><td>Queue expirations</td><td>{{.QueueExpireCount}}</td></tr>
    <tr><td>HTTP errors</td><td>{{.HTTPErrorCount}}</td></tr>
    <tr><td>Game errors</td><td>{{.GameErrorCount}}</td></tr>
  </table>

  <h2>Queue activity</h2>
  <p>Anonymous aggregate from the last {{.Activity.WindowDays}} days, grouped by UTC hour. Buckets with fewer than three attempts are hidden.</p>
  <table border="1">
    <tr><th>UTC hour</th><th>Attempts</th><th>Matches</th><th>Connected</th><th>Failed</th><th>Average match wait</th></tr>
    {{range .Activity.Hours}}
    <tr><td>{{printf "%02d:00" .HourUTC}}</td><td>{{if .Suppressed}}&lt; 3{{else}}{{.Attempts}}{{end}}</td><td>{{if .Suppressed}}—{{else}}{{.Matches}}{{end}}</td><td>{{if .Suppressed}}—{{else}}{{.MatchSuccesses}}{{end}}</td><td>{{if .Suppressed}}—{{else}}{{.MatchFailures}}{{end}}</td><td>{{if .Suppressed}}—{{else if .AverageMatchWaitMs}}{{.AverageMatchWaitMs}} ms{{else}}—{{end}}</td></tr>
    {{end}}
  </table>

  <h2>Recurring community queues</h2>
  <table border="1">
    <tr><th>Event</th><th>Region</th><th>UTC schedule</th><th>Status</th></tr>
    {{range .Events}}<tr><td>{{.Name}}</td><td>{{.Region}}</td><td>{{.StartsAtUTC}} – {{.EndsAtUTC}}</td><td>{{if .Active}}Active{{else}}Upcoming{{end}}</td></tr>{{end}}
  </table>

  <h2>Recent HTTP errors</h2>
  {{if .RecentHTTPErrors}}<ul>{{range .RecentHTTPErrors}}<li>{{.Time.UTC}} · HTTP {{.Status}} · {{.Code}}</li>{{end}}</ul>{{else}}<p>None recorded.</p>{{end}}

  <h2>Recent game errors</h2>
  {{if .RecentGameErrors}}<ul>{{range .RecentGameErrors}}<li>{{.Time.UTC}} · {{.Code}}</li>{{end}}</ul>{{else}}<p>None recorded.</p>{{end}}
</body>
</html>`))

func healthHTMLHandler(w http.ResponseWriter, r *http.Request) {
	serveHealthHTML(handler, w, r)
}

func serveHealthHTML(lobby *lobbyHandler, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		healthResponse
		ServiceName string
	}{healthResponse: lobby.healthResponse(), ServiceName: lobby.Config.Service.Name}
	if err := healthHTMLTemplate.Execute(w, data); err != nil {
		slog.Error("Health HTML response failed", "error", err)
	}
}

func robotsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func main() {
	slogLevel := &slog.LevelVar{}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slogLevel,
	})))

	flag.StringVar(&host, "host", host, "HTTP host to listen on")
	flag.StringVar(&tlsHost, "tlshost", tlsHost, "TLS host to listen on")
	flag.IntVar(&port, "port", port, "HTTP port to listen on")
	flag.IntVar(&tlsPort, "tlsport", tlsPort, "TLS port to listen on")
	flag.BoolVar(&noHTTP, "nohttp", noHTTP, "Disables HTTP")
	flag.BoolVar(&useTLS, "tls", useTLS, "Enables TLS (sets tlsport to 443 if unspecified)")
	flag.StringVar(&tlsCert, "cert", tlsCert, "File to use as TLS cert")
	flag.StringVar(&tlsKey, "key", tlsKey, "File to use as TLS key")
	flag.StringVar(&autocertDomain, "autocert", autocertDomain, "Domain for automatic TLS (Let's Encrypt)")
	flag.StringVar(&autocertCacheDir, "autocert-cache", autocertCacheDir, "Cache directory for autocert certificates")
	flag.DurationVar(&readTimeout, "read-timeout", readTimeout, "HTTP read timeout")
	flag.DurationVar(&writeTimeout, "write-timeout", writeTimeout, "HTTP write timeout")
	flag.DurationVar(&idleTimeout, "idle-timeout", idleTimeout, "HTTP idle timeout")
	flag.BoolVar(&trustProxy, "trust-proxy", trustProxy, "Trust X-Forwarded-For and X-Real-IP headers")
	flag.StringVar(&trustedProxyCIDRs, "trusted-proxy-cidrs", trustedProxyCIDRs, "Comma-separated CIDR allowlist for trusted reverse proxies")
	flag.StringVar(&stunHost, "stun-host", stunHost, "Host to bind the built-in STUN responder (default: dual-stack any-address)")
	flag.IntVar(&stunPort, "stun-port", stunPort, "UDP port for the built-in STUN responder (0 disables; conventional value is 3478)")
	flag.StringVar(&configPath, "config", configPath, "Optional JSON game profile (defaults to the bundled Antistatic profile)")
	flag.Parse()

	if err := validateServerPorts(port, tlsPort, stunPort); err != nil {
		slog.Error("Invalid server port", "error", err)
		os.Exit(2)
	}
	if err := setTrustedProxyCIDRs(trustedProxyCIDRs); err != nil {
		slog.Error("Invalid trusted proxy CIDRs", "error", err)
		os.Exit(2)
	}
	gameConfig, err := LoadConfig(configPath)
	if err != nil {
		slog.Error("Invalid game configuration", "error", err)
		os.Exit(2)
	}
	config, err := applicationConfigFromEnv(gameConfig)
	if err != nil {
		slog.Error("Invalid application configuration", "error", err)
		os.Exit(2)
	}
	applicationHandler, err := newApplicationHandler(config, handler)
	if err != nil {
		slog.Error("Failed to construct application handler", "error", err)
		os.Exit(2)
	}

	if tlsPort <= 0 && useTLS {
		tlsPort = 443
	}
	useTLS = tlsPort > 0
	if config.AdminUsername != "" && !noHTTP && !trustProxy {
		slog.Error("Admin credentials require -nohttp or a trusted TLS-terminating proxy")
		os.Exit(2)
	}

	if autocertDomain != "" && useTLS {
		slog.Warn("Both autocert and manual TLS are enabled; disabling manual TLS in favor of autocert")
		useTLS = false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	mux := http.NewServeMux()
	mux.Handle("/", applicationHandler)

	var mgr *autocert.Manager
	if autocertDomain != "" {
		mgr = &autocert.Manager{
			Cache:      autocert.DirCache(autocertCacheDir),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(autocertDomain),
		}
		mux.Handle("/.well-known/acme-challenge/", mgr.HTTPHandler(nil))
	}

	rl := newRateLimiter(60, 120, time.Minute)
	rl.metrics = &handler.Metrics
	defer rl.Stop()

	httpHandler := requestIDMiddleware(
		rl.middleware(
			securityHeaders(
				maxBytes(1024 * 10)(
					withTimeout(requestTimeout)(mux),
				),
			),
		),
	)

	var servers []*http.Server

	if autocertDomain != "" {
		acmeTlsPort := tlsPort
		if acmeTlsPort <= 0 {
			acmeTlsPort = 443
		}
		addr := listenAddress("", acmeTlsPort)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			slog.Error("Failed to listen for autocert", "error", err)
		} else {
			slog.Info("HTTPS autocert listening", "addr", addr, "domain", autocertDomain, "cache", autocertCacheDir)
			tlsConfig := mgr.TLSConfig()
			tlsConfig.MinVersion = tls.VersionTLS12
			tlsLn := tls.NewListener(ln, tlsConfig)
			srv := &http.Server{
				Handler:           httpHandler,
				ReadTimeout:       readTimeout,
				WriteTimeout:      writeTimeout,
				IdleTimeout:       idleTimeout,
				ReadHeaderTimeout: 5 * time.Second,
			}
			servers = append(servers, srv)
			wg.Go(func() {
				err := srv.Serve(tlsLn)
				if err != nil && err != http.ErrServerClosed {
					slog.Error("HTTPS autocert error", "error", err)
				}
			})
		}
	}

	if autocertDomain != "" && noHTTP {
		slog.Info("ACME challenge HTTP server listening", "port", 80, "domain", autocertDomain)
		acmeSrv := &http.Server{
			Addr:              listenAddress("", 80),
			Handler:           mgr.HTTPHandler(nil),
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ReadHeaderTimeout: 5 * time.Second,
		}
		servers = append(servers, acmeSrv)
		wg.Go(func() {
			err := acmeSrv.ListenAndServe()
			if err != nil && err != http.ErrServerClosed {
				slog.Error("ACME HTTP server error", "error", err)
			}
		})
	}

	if !noHTTP {
		slog.Info("HTTP listening", "host", host, "port", port)
		srv := &http.Server{
			Addr:              listenAddress(host, port),
			Handler:           httpHandler,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ReadHeaderTimeout: 5 * time.Second,
		}
		servers = append(servers, srv)
		wg.Go(func() {
			err := srv.ListenAndServe()
			if err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP error", "error", err)
			}
		})
	}

	if useTLS {
		slog.Info("TLS listening", "host", tlsHost, "port", tlsPort, "cert", tlsCert, "key", tlsKey)
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		srv := &http.Server{
			Addr:              listenAddress(tlsHost, tlsPort),
			Handler:           httpHandler,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ReadHeaderTimeout: 5 * time.Second,
			TLSConfig:         tlsConfig,
		}
		servers = append(servers, srv)
		wg.Go(func() {
			err := srv.ListenAndServeTLS(tlsCert, tlsKey)
			if err != nil && err != http.ErrServerClosed {
				slog.Error("TLS error", "error", err)
			}
		})
	}

	handler.Maintain()

	var stun *stunServer
	if stunPort > 0 && stunPort <= 65535 {
		s, err := startStunServer(stunHost, stunPort)
		if err != nil {
			slog.Error("Failed to start STUN responder", "error", err, "host", stunHost, "port", stunPort)
		} else {
			stun = s
			slog.Info("STUN responder listening", "addr", stun.localAddr().String())
		}
	}

	<-ctx.Done()
	slog.Info("Shutdown signal received, starting graceful shutdown...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var shutdownWg sync.WaitGroup
	for _, srv := range servers {
		shutdownWg.Go(func() {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				slog.Error("Server shutdown error", "error", err)
			}
		})
	}
	shutdownWg.Wait()

	if stun != nil {
		stun.Close(shutdownCtx)
	}

	handler.Stop()
	applicationHandler.Close()
	if config.Store != nil {
		config.Store.Close()
	}
	wg.Wait()
	slog.Info("Server stopped gracefully")
}
