package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"

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

func init() {
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
}

func main() {
	if tlsPort <= 0 && useTLS {
		tlsPort = 443
	}
	useTLS = tlsPort > 0

	var wg sync.WaitGroup
	mux := http.NewServeMux()
	mux.Handle("/lobby/", oldHandler)
	mux.Handle("/", handler)

	if autocertDomain != "" {
		log.Println("HTTPS autocert listening on", autocertDomain)
		wg.Add(1)
		go func() {
			err := http.Serve(autocert.NewListener(autocertDomain), mux)
			if err != nil {
				log.Println("HTTPS autocert listening error:", err)
			}
			wg.Done()
		}()
	}

	if !noHTTP {
		log.Println("HTTP listening on port", port)
		wg.Add(1)
		go func() {
			err := http.ListenAndServe(host+":"+strconv.Itoa(port), mux)
			if err != nil {
				log.Println("HTTP listening error:", err)
			}
			wg.Done()
		}()
	}
	if useTLS {
		log.Printf("TLS listening on port %d (cert: %s, key: %s)", tlsPort, tlsCert, tlsKey)
		wg.Add(1)
		go func() {
			err := http.ListenAndServeTLS(tlsHost+":"+strconv.Itoa(tlsPort), tlsCert, tlsKey, mux)
			if err != nil {
				log.Println("TLS listening error:", err)
			}
			wg.Done()
		}()
	}

	handler.Maintain()
	oldHandler.Maintain()

	wg.Wait()
	handler.Ticker.Stop()
	oldHandler.Ticker.Stop()
	fmt.Println("Done - exiting")
}
