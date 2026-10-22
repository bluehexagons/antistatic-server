package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func publicReadMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	return false
}

func staticPublicHandler(contentType string, content []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !publicReadMethod(w, r) {
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if r.Method == http.MethodGet {
			_, _ = w.Write(content)
		}
	}
}

// Let crawlers read the landing page's noindex directive while keeping service
// routes out of the crawl. A blanket disallow would hide that directive too.
var robotsHandler = staticPublicHandler("text/plain; charset=utf-8", []byte("User-agent: *\nDisallow: /\nAllow: /$\n"))

// Security contact details belong to the operator, rather than the game profile.
type securityTextConfig struct {
	Contact string
	Expires time.Time
}

func parseSecurityTextConfig(contact, expires string) (securityTextConfig, error) {
	config := securityTextConfig{Contact: strings.TrimSpace(contact)}
	if strings.TrimSpace(expires) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(expires))
		if err != nil {
			return config, errors.New("ANTISTATIC_SECURITY_EXPIRES must be an RFC 3339 timestamp")
		}
		config.Expires = parsed.UTC()
	}
	return config, config.validate(time.Now())
}

func (config securityTextConfig) validate(now time.Time) error {
	if config.Contact == "" && config.Expires.IsZero() {
		return nil
	}
	if config.Contact == "" || config.Expires.IsZero() {
		return errors.New("ANTISTATIC_SECURITY_CONTACT and ANTISTATIC_SECURITY_EXPIRES must both be configured")
	}
	if !config.Expires.After(now) {
		return errors.New("ANTISTATIC_SECURITY_EXPIRES must be in the future")
	}
	if len(config.Contact) > 2048 || strings.IndexFunc(config.Contact, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return errors.New("ANTISTATIC_SECURITY_CONTACT must be a single URI of at most 2048 bytes")
	}
	contact, err := url.ParseRequestURI(config.Contact)
	if err == nil {
		// ParseRequestURI leaves RawQuery escapes unchecked.
		_, err = url.QueryUnescape(contact.RawQuery)
	}
	if err == nil {
		switch contact.Scheme {
		case "https":
			if contact.Hostname() != "" && contact.User == nil {
				return nil
			}
		case "mailto":
			address, decodeErr := url.PathUnescape(contact.Opaque)
			if parsed, parseErr := mail.ParseAddress(address); decodeErr == nil && parseErr == nil && parsed.Address == address {
				return nil
			}
		}
	}
	return errors.New("ANTISTATIC_SECURITY_CONTACT must be an HTTPS contact URL or mailto address")
}

func (config securityTextConfig) serveHTTP(w http.ResponseWriter, r *http.Request) {
	// Never cache a redirect or a document beyond its operator-selected expiry.
	w.Header().Set("Cache-Control", "no-store")
	if !publicReadMethod(w, r) {
		return
	}
	if config.Contact == "" || !config.Expires.After(time.Now()) {
		http.NotFound(w, r)
		return
	}
	if !requestIsHTTPS(r) {
		http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
		return
	}
	if r.URL.Path == "/security.txt" {
		http.Redirect(w, r, "/.well-known/security.txt", http.StatusTemporaryRedirect)
		return
	}
	content := fmt.Sprintf("Contact: %s\nExpires: %s\n", config.Contact, config.Expires.UTC().Format(time.RFC3339))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(content))
	}
}
