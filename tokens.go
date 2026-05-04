package main

import (
	"crypto/rand"
	"encoding/base64"
)

const antistaticTokenHeader = "X-Antistatic-Token"

func generateBearerToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
