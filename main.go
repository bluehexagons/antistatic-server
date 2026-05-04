package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
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

func healthHandler(w http.ResponseWriter, r *http.Request) {
	resp, _ := json.Marshal(handler.healthResponse())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resp)
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
	flag.Parse()

	if err := setTrustedProxyCIDRs(trustedProxyCIDRs); err != nil {
		slog.Error("Invalid trusted proxy CIDRs", "error", err)
		os.Exit(2)
	}

	if tlsPort <= 0 && useTLS {
		tlsPort = 443
	}
	useTLS = tlsPort > 0

	if autocertDomain != "" && useTLS {
		slog.Warn("Both autocert and manual TLS are enabled; disabling manual TLS in favor of autocert")
		useTLS = false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/", handler)

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
		ln, err := net.Listen("tcp", ":443")
		if err != nil {
			slog.Error("Failed to listen for autocert", "error", err)
		} else {
			slog.Info("HTTPS autocert listening", "domain", autocertDomain, "cache", autocertCacheDir)
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
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := srv.Serve(tlsLn)
				if err != nil && err != http.ErrServerClosed {
					slog.Error("HTTPS autocert error", "error", err)
				}
			}()
		}
	}

	if !noHTTP {
		slog.Info("HTTP listening", "host", host, "port", port)
		srv := &http.Server{
			Addr:              host + ":" + strconv.Itoa(port),
			Handler:           httpHandler,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ReadHeaderTimeout: 5 * time.Second,
		}
		servers = append(servers, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := srv.ListenAndServe()
			if err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP error", "error", err)
			}
		}()
	}

	if useTLS {
		slog.Info("TLS listening", "host", tlsHost, "port", tlsPort, "cert", tlsCert, "key", tlsKey)
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		srv := &http.Server{
			Addr:              tlsHost + ":" + strconv.Itoa(tlsPort),
			Handler:           httpHandler,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ReadHeaderTimeout: 5 * time.Second,
			TLSConfig:         tlsConfig,
		}
		servers = append(servers, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := srv.ListenAndServeTLS(tlsCert, tlsKey)
			if err != nil && err != http.ErrServerClosed {
				slog.Error("TLS error", "error", err)
			}
		}()
	}

	handler.Maintain()

	<-ctx.Done()
	slog.Info("Shutdown signal received, starting graceful shutdown...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var shutdownWg sync.WaitGroup
	for _, srv := range servers {
		shutdownWg.Add(1)
		go func(s *http.Server) {
			defer shutdownWg.Done()
			if err := s.Shutdown(shutdownCtx); err != nil {
				slog.Error("Server shutdown error", "error", err)
			}
		}(srv)
	}
	shutdownWg.Wait()

	handler.Stop()
	wg.Wait()
	slog.Info("Server stopped gracefully")
}
