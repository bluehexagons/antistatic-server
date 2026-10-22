package main

import (
	_ "embed"
	"net/http"
)

// The landing page and its links are compiled game-specific presentation.
// Forks can replace these assets without changing generic HTTP behavior.
//
//go:embed public/antistatic.html
var antistaticLandingPage []byte

//go:embed public/antistatic.css
var antistaticLandingStyle []byte

//go:embed public/antistatic.png
var antistaticIcon []byte

func registerAntistaticPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/{$}", staticPublicHandler("text/html; charset=utf-8", antistaticLandingPage))
	mux.HandleFunc("/site.css", staticPublicHandler("text/css; charset=utf-8", antistaticLandingStyle))
	// The conventional browser path can serve PNG with its correct media type.
	mux.HandleFunc("/favicon.ico", staticPublicHandler("image/png", antistaticIcon))
}
