package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

const maxBearerTokenLength = 512

func generateBearerToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if value == "" {
		return "", true
	}
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	token := strings.TrimSpace(value[len(prefix):])
	if token == "" || len(token) > maxBearerTokenLength || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}
