package main

import (
	"context"
	"flag"
	"log"
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
var requestTimeout = 30 * time.Second
var shutdownTimeout = 30 * time.Second

// Health check handler
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func main() {
	flag.StringVar(&host, "host", host, "HTTP host to listen on")
	flag.StringVar(&tlsHost, "tlshost", tlsHost, "TLS host to listen on")
	flag.IntVar(&port, "port", port, "HTTP port to listen on")
	flag.IntVar(&tlsPort, "tlsport", tlsPort, "TLS port to listen on")
	flag.BoolVar(&noHTTP, "nohttp", noHTTP, "Disables HTTP")
	flag.BoolVar(&useTLS, "tls", useTLS, "Enables TLS (sets tlsport to 443 if unspecified)")
	flag.StringVar(&tlsCert, "cert", tlsCert, "File to use as TLS cert")
	flag.StringVar(&tlsKey, "key", tlsKey, "File to use as TLS key")
	flag.StringVar(&autocertDomain, "autocert", autocertDomain, "Domain to serve")
	flag.Parse()

	if tlsPort <= 0 && useTLS {
		tlsPort = 443
	}
	useTLS = tlsPort > 0

	// Setup signal handling for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	mux := http.NewServeMux()
	
	// Health check endpoint
	mux.HandleFunc("/health", healthHandler)
	
	// Lobby handlers
	mux.Handle("/lobby/", oldHandler)
	mux.Handle("/", handler)

	// Apply middleware
	rl := newRateLimiter(60, 120, time.Minute) // 60 requests per minute, burst of 120
	httpHandler := requestIDMiddleware(
		rl.middleware(
			securityHeaders(
				maxBytes(1024*10)( // 10KB max request size
					withTimeout(requestTimeout)(mux),
				),
			),
		),
	)

	// Track servers for graceful shutdown
	var servers []*http.Server

	if autocertDomain != "" {
		log.Println("HTTPS autocert listening on", autocertDomain)
		srv := &http.Server{
			Handler:      httpHandler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		servers = append(servers, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := srv.Serve(autocert.NewListener(autocertDomain))
			if err != nil && err != http.ErrServerClosed {
				log.Println("HTTPS autocert error:", err)
			}
		}()
	}

	if !noHTTP {
		log.Printf("HTTP listening on %s:%d", host, port)
		srv := &http.Server{
			Addr:         host + ":" + strconv.Itoa(port),
			Handler:      httpHandler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		servers = append(servers, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := srv.ListenAndServe()
			if err != nil && err != http.ErrServerClosed {
				log.Println("HTTP error:", err)
			}
		}()
	}
	
	if useTLS {
		log.Printf("TLS listening on %s:%d (cert: %s, key: %s)", tlsHost, tlsPort, tlsCert, tlsKey)
		srv := &http.Server{
			Addr:         tlsHost + ":" + strconv.Itoa(tlsPort),
			Handler:      httpHandler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		servers = append(servers, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := srv.ListenAndServeTLS(tlsCert, tlsKey)
			if err != nil && err != http.ErrServerClosed {
				log.Println("TLS error:", err)
			}
		}()
	}

	// Start maintenance routines
	handler.Maintain()
	oldHandler.Maintain()

	// Wait for interrupt signal
	<-ctx.Done()
	log.Println("Shutdown signal received, starting graceful shutdown...")

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Shutdown all servers
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
	}

	// Stop maintenance tickers
	handler.Ticker.Stop()
	oldHandler.Ticker.Stop()

	// Wait for all servers to finish
	wg.Wait()
	log.Println("Server stopped gracefully")
}
