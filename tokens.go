package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

func generateBearerToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	token := strings.TrimSpace(value[len(prefix):])
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return ""
	}
	return token
}
