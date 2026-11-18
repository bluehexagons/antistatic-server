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
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 || !strings.HasPrefix(values[0], prefix) {
		return "", false
	}
	token := strings.TrimSpace(values[0][len(prefix):])
	if token == "" || len(token) > maxBearerTokenLength || strings.ContainsAny(token, ", \t\r\n") {
		return "", false
	}
	return token, true
}
