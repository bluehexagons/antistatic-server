package main

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPublicRoutes(t *testing.T) {
	lobby := newTestLobbyHandler()
	app, err := newApplicationHandler(securityTextConfig{}, lobby)
	if err != nil {
		t.Fatal(err)
	}
	handler := securityHeaders(app)
	for _, route := range []struct {
		path        string
		contentType string
	}{
		{"/", "text/html; charset=utf-8"},
		{"/site.css", "text/css; charset=utf-8"},
		{"/favicon.ico", "image/png"},
		{"/robots.txt", "text/plain; charset=utf-8"},
	} {
		t.Run(route.path, func(t *testing.T) {
			get := httptest.NewRecorder()
			handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, route.path, nil))
			if get.Code != http.StatusOK || get.Header().Get("Content-Type") != route.contentType {
				t.Fatalf("GET %s = %d, %q", route.path, get.Code, get.Header().Get("Content-Type"))
			}
			if get.Header().Get("X-Robots-Tag") != "noindex, nofollow" || get.Header().Get("Cache-Control") == "" {
				t.Fatalf("missing indexing or cache headers: %v", get.Header())
			}
			if route.path == "/robots.txt" && get.Body.String() != "User-agent: *\nDisallow: /\nAllow: /$\n" {
				t.Fatalf("robots.txt must allow reading noindex at the root while disallowing service paths: %q", get.Body.String())
			}
			head := httptest.NewRecorder()
			handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, route.path, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(get.Body.Len()) {
				t.Fatalf("HEAD %s = %d, length %q, body length %d", route.path, head.Code, head.Header().Get("Content-Length"), head.Body.Len())
			}
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
				rejected := httptest.NewRecorder()
				handler.ServeHTTP(rejected, httptest.NewRequest(method, route.path, nil))
				if rejected.Code != http.StatusMethodNotAllowed || rejected.Header().Get("Allow") != "GET, HEAD" {
					t.Fatalf("%s %s = %d, Allow %q", method, route.path, rejected.Code, rejected.Header().Get("Allow"))
				}
			}
		})
	}
	if lobby.Metrics.clientErrors.Load() != 0 {
		t.Fatal("public route requests were counted as API errors")
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/unrecognized", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404 rather than the landing page", unknown.Code)
	}
}

func TestLandingPageLinksAndPrivacy(t *testing.T) {
	app, err := newApplicationHandler(securityTextConfig{}, newTestLobbyHandler())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	securityHeaders(app).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`href="https://bluehexagons.com/antistatic"`,
		`href="https://store.steampowered.com/app/944500/Antistatic/"`,
		`href="https://bluehexagons.itch.io/antistatic"`,
		`name="robots" content="noindex, nofollow"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page missing %s", want)
		}
	}
	if recorder.Header().Get("Location") != "" || strings.Contains(body, "/health") || strings.Contains(body, "/admin") {
		t.Fatal("landing page redirects or advertises operational pages")
	}
	csp := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self'") || !strings.Contains(csp, "img-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("landing page CSP does not allow its local assets safely: %q", csp)
	}
	icon, err := png.Decode(bytes.NewReader(antistaticIcon))
	if err != nil || icon.Bounds().Empty() {
		t.Fatalf("favicon is not a valid PNG: %v", err)
	}
}

func TestReleasedClientThroughPublicRouter(t *testing.T) {
	lobby := newTestLobbyHandler()
	app, err := newApplicationHandler(securityTextConfig{}, lobby)
	if err != nil {
		t.Fatal(err)
	}
	handler := securityHeaders(app)
	send := func(method, path, remote, body, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.RemoteAddr = remote
		request.Header.Set(antistaticTokenHeader, token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("released route %s %s = %d: %s", method, path, recorder.Code, recorder.Body.String())
		}
		return recorder
	}

	const lobbyPath = "/0.9.125/lobby/RELEASED/45860"
	created := send(http.MethodPut, lobbyPath, "198.51.100.10:32000", `{"local_ips":[]}`, "")
	owner := decodeLobbyResponse(t, created).Token
	if owner == "" {
		t.Fatal("released lobby route did not return an ownership token")
	}
	send(http.MethodDelete, lobbyPath, "[2001:db8::1]:32000", "", owner)
	if len(lobby.Lobbies) != 0 {
		t.Fatal("released lobby cancellation did not reach the lobby handler")
	}

	const ticketPath = "/0.9.125/matchmaking/default/ReleasedA/45860"
	first := send(http.MethodPut, ticketPath, "198.51.100.10:32000", `{"character":"Carbon"}`, "")
	owner = decodeMatchmakingResponse(t, first).Token
	if owner == "" {
		t.Fatal("released matchmaking route did not return an ownership token")
	}
	send(http.MethodPut, "/0.9.125/matchmaking/default/ReleasedB/45861", "198.51.100.20:32000", `{"character":"Silicon"}`, "")
	matched := decodeMatchmakingResponse(t, send(http.MethodGet, ticketPath, "198.51.100.10:32000", "", owner))
	if matched.Status != "matched" || matched.Match == nil || matched.Match.Self.Character != "Carbon" || matched.Match.Peer.Character != "Silicon" {
		t.Fatalf("released matchmaking response changed: %#v", matched)
	}
	canceled := decodeMatchmakingResponse(t, send(http.MethodDelete, ticketPath, "198.51.100.10:32000", "", owner))
	if canceled.Status != "canceled" {
		t.Fatalf("released matchmaking cancellation changed: %#v", canceled)
	}

	health := send(http.MethodGet, "/health", "198.51.100.10:32000", "", "")
	if health.Header().Get("Cache-Control") != "no-store" || health.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("health view lacks cache/indexing controls: %v", health.Header())
	}
}

func TestSecurityTextConfiguration(t *testing.T) {
	future := time.Now().Add(180 * 24 * time.Hour).UTC().Format(time.RFC3339)
	for _, contact := range []string{
		"mailto:security@example.com",
		"mailto:security%2Bserver@example.com",
		"mailto:security@example.com?subject=Server%20security",
		"https://example.com/security",
		"https://example.com/security?team=Server%20security",
	} {
		config, err := parseSecurityTextConfig(contact, future)
		if err != nil || config.Contact != contact || config.Expires.IsZero() {
			t.Fatalf("valid security configuration = %#v, %v", config, err)
		}
	}
	for _, test := range []struct{ contact, expires string }{
		{"mailto:security@example.com", ""},
		{"", future},
		{"mailto:security@example.com", "tomorrow"},
		{"mailto:security@example.com", time.Now().Add(-time.Hour).Format(time.RFC3339)},
		{"http://example.com/security", future},
		{"mailto:invalid", future},
		{"https://", future},
		{"https://user:password@example.com", future},
		{"https://example.com/security?team=%zz", future},
		{"mailto:security@example.com?subject=%zz", future},
		{"mailto:security@example.com\nPolicy: https://example.com", future},
		{"https://example.com/" + strings.Repeat("a", 2048), future},
	} {
		if _, err := parseSecurityTextConfig(test.contact, test.expires); err == nil {
			t.Errorf("invalid security configuration accepted: %#v", test)
		}
	}
	if _, err := parseSecurityTextConfig("", ""); err != nil {
		t.Fatalf("disabled security configuration rejected: %v", err)
	}
	t.Setenv("ANTISTATIC_SECURITY_CONTACT", "https://example.com/security")
	t.Setenv("ANTISTATIC_SECURITY_EXPIRES", future)
	config, err := securityTextConfigFromEnv()
	if err != nil || config.Contact != "https://example.com/security" {
		t.Fatalf("security environment configuration = %#v, %v", config, err)
	}
}

func TestSecurityTextRoutes(t *testing.T) {
	config := securityTextConfig{Contact: "mailto:security@example.com", Expires: time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()}
	app, err := newApplicationHandler(config, newTestLobbyHandler())
	if err != nil {
		t.Fatal(err)
	}
	canonical := "https://example.com/.well-known/security.txt"
	get := httptest.NewRecorder()
	app.ServeHTTP(get, httptest.NewRequest(http.MethodGet, canonical, nil))
	want := "Contact: mailto:security@example.com\nExpires: " + config.Expires.Format(time.RFC3339) + "\n"
	if get.Code != http.StatusOK || get.Body.String() != want || get.Header().Get("Content-Type") != "text/plain; charset=utf-8" || get.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("security.txt = %d, %v, %q", get.Code, get.Header(), get.Body.String())
	}
	head := httptest.NewRecorder()
	app.ServeHTTP(head, httptest.NewRequest(http.MethodHead, canonical, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Fatalf("HEAD security.txt = %d, %v, %q", head.Code, head.Header(), head.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		legacy := httptest.NewRecorder()
		app.ServeHTTP(legacy, httptest.NewRequest(method, "https://example.com/security.txt", nil))
		if legacy.Code != http.StatusTemporaryRedirect || legacy.Header().Get("Location") != "/.well-known/security.txt" || legacy.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("legacy security.txt = %d, %v", legacy.Code, legacy.Header())
		}
	}
	for _, path := range []string{"/security.txt", "/.well-known/security.txt"} {
		insecure := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		request.Header.Set("X-Forwarded-Proto", "https")
		app.ServeHTTP(insecure, request)
		if insecure.Code != http.StatusUpgradeRequired || strings.Contains(insecure.Body.String(), config.Contact) {
			t.Fatalf("insecure security.txt = %d, %q", insecure.Code, insecure.Body.String())
		}
		post := httptest.NewRecorder()
		app.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "https://example.com"+path, nil))
		if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("POST security.txt = %d, %v", post.Code, post.Header())
		}
	}
}

func TestSecurityTextUnavailableWhenDisabledOrExpired(t *testing.T) {
	app, err := newApplicationHandler(securityTextConfig{}, newTestLobbyHandler())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/security.txt", "/.well-known/security.txt"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "https://example.com"+path, nil)
		app.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("disabled %s = %d", path, recorder.Code)
		}
		expired := securityTextConfig{Contact: "mailto:security@example.com", Expires: time.Now().Add(-time.Second)}
		recorder = httptest.NewRecorder()
		expired.serveHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Location") != "" || strings.Contains(recorder.Body.String(), expired.Contact) {
			t.Fatalf("expired %s = %d, %v, %q", path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
}

func TestSecurityTextBehindTrustedHTTPSProxy(t *testing.T) {
	previousTrustProxy, previousRanges := trustProxy, trustedProxyRanges
	t.Cleanup(func() { trustProxy, trustedProxyRanges = previousTrustProxy, previousRanges })
	trustProxy = true
	if err := setTrustedProxyCIDRs("10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	config := securityTextConfig{Contact: "https://example.com/security", Expires: time.Now().Add(time.Hour)}
	app, err := newApplicationHandler(config, newTestLobbyHandler())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		remote, proto string
		want          int
	}{
		{"10.1.2.3:1234", "https", http.StatusOK},
		{"10.1.2.3:1234", "http", http.StatusUpgradeRequired},
		{"198.51.100.10:1234", "https", http.StatusUpgradeRequired},
	} {
		request := httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-Proto", test.proto)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("remote=%s proto=%s status=%d, want %d", test.remote, test.proto, recorder.Code, test.want)
		}
	}
}
