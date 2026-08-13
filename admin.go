package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strings"
)

const maxAdminRows = 500

const adminStylesheet = `
:root { color-scheme: light; font-family: ui-sans-serif, system-ui, sans-serif; background: #f5f4ef; color: #20231f; }
body { max-width: 1120px; margin: 0 auto; padding: 2rem; }
a { color: #235f4b; }
nav { display: flex; flex-wrap: wrap; gap: 1rem; margin-bottom: 2rem; }
.status { padding: .75rem 1rem; background: #fff4c7; border: 1px solid #d9c56c; }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr)); gap: 1rem; }
.card { background: white; border: 1px solid #d8d8d2; padding: 1rem; }
table { width: 100%; border-collapse: collapse; background: white; font-size: .9rem; }
th, td { text-align: left; vertical-align: top; padding: .65rem; border-bottom: 1px solid #deded8; }
th { background: #e8ebe5; }
pre { white-space: pre-wrap; overflow-wrap: anywhere; background: white; border: 1px solid #d8d8d2; padding: 1rem; }
code { overflow-wrap: anywhere; }
`

var adminOverviewTemplate = template.Must(template.New("overview").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.ServiceName}} reports</title><link rel="stylesheet" href="/admin/style.css"></head>
<body><h1>{{.ServiceName}} reports</h1>{{template "nav" .}}<p>Only privacy-bounded, rounded records are shown here.</p>
{{if not .Available}}<p class="status">Report storage is unavailable. Configure ANTISTATIC_DATA_DIR to enable collection.</p>{{else}}
<div class="cards">{{range .Sections}}<div class="card"><strong>{{.Name}}</strong><br>{{.Count}}</div>{{end}}</div>{{end}}</body></html>
{{define "nav"}}<nav><a href="/admin/">Overview</a>{{range .Sections}}<a href="{{.Path}}">{{.ShortName}}</a>{{end}}</nav>{{end}}`))

var adminCrashTemplate = template.Must(template.New("crash").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Crash reports</title><link rel="stylesheet" href="/admin/style.css"></head><body><h1>Crash reports</h1>{{template "nav" .}}<p>{{.CrashCount}} retained reports; showing up to 500 latest.</p>{{if .Available}}<table><tr><th>Time bucket</th><th>ID</th><th>Version</th><th>Platform</th><th>Reason</th></tr>{{range .Crashes}}<tr><td>{{.ServerTime}}</td><td><a href="/admin/crash/{{.ID}}">{{.ID}}</a></td><td>{{.AppVersion}}</td><td>{{.Platform}} / {{.Arch}}</td><td>{{.ReasonCode}}</td></tr>{{end}}</table>{{else}}<p class="status">Report storage is unavailable.</p>{{end}}</body></html>{{define "nav"}}<nav><a href="/admin/">Overview</a>{{range .Sections}}<a href="{{.Path}}">{{.ShortName}}</a>{{end}}</nav>{{end}}`))

var adminCrashDetailTemplate = template.Must(template.New("crash-detail").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Crash {{.ID}}</title><link rel="stylesheet" href="/admin/style.css"></head><body><p><a href="/admin/crash">Back to crashes</a></p><h1>Crash {{.ID}}</h1><dl><dt>Time bucket</dt><dd>{{.ServerTime}}</dd><dt>Version</dt><dd>{{.AppVersion}}</dd><dt>Platform</dt><dd>{{.Platform}} / {{.Arch}}</dd><dt>Reason</dt><dd>{{.ReasonCode}}</dd><dt>Symbols</dt><dd>{{if .Symbols}}<ol>{{range .Symbols}}<li><code>{{.}}</code></li>{{end}}</ol>{{else}}None{{end}}</dd></dl></body></html>`))

var adminFeedbackTemplate = template.Must(template.New("feedback").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Feedback</title><link rel="stylesheet" href="/admin/style.css"></head><body><h1>Feedback</h1>{{template "nav" .}}<p>{{.FeedbackCount}} retained messages; showing up to 500 latest.</p>{{if .Available}}<table><tr><th>Time bucket</th><th>ID</th><th>Category</th><th>Subject</th><th>Version</th></tr>{{range .Feedback}}<tr><td>{{.ServerTime}}</td><td><a href="/admin/feedback/{{.ID}}">{{.ID}}</a></td><td>{{.Category}}</td><td>{{.Subject}}</td><td>{{.AppVersion}}</td></tr>{{end}}</table>{{else}}<p class="status">Report storage is unavailable.</p>{{end}}</body></html>{{define "nav"}}<nav><a href="/admin/">Overview</a>{{range .Sections}}<a href="{{.Path}}">{{.ShortName}}</a>{{end}}</nav>{{end}}`))

var adminFeedbackDetailTemplate = template.Must(template.New("feedback-detail").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Feedback {{.ID}}</title><link rel="stylesheet" href="/admin/style.css"></head><body><p><a href="/admin/feedback">Back to feedback</a></p><h1>{{.Subject}}</h1><p>{{.Category}} · {{.AppVersion}} · {{.ServerTime}}</p>{{if .RelatedReportID}}<p>Related report: <code>{{.RelatedReportID}}</code></p>{{end}}<pre>{{.Body}}</pre></body></html>`))

var adminPerformanceTemplate = template.Must(template.New("performance").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Performance metrics</title><link rel="stylesheet" href="/admin/style.css"></head><body><h1>Performance metrics</h1>{{template "nav" .}}<p>{{.PerformanceCount}} retained coarse samples; showing up to 500 latest.</p>{{if .Available}}<table><tr><th>Time bucket</th><th>Version</th><th>Platform</th><th>Renderer / vendor</th><th>Hardware buckets</th><th>Frames</th><th>Frame ms avg / p95</th></tr>{{range .Performance}}<tr><td>{{.ServerTime}}</td><td>{{.AppVersion}}</td><td>{{.Platform}} / {{.Arch}}</td><td>{{.RendererFamily}} / {{.GPUVendor}}</td><td>{{.MemoryGiBBucket}} GiB · {{.CPUCoresBucket}} cores · {{.ResolutionBucket}}</td><td>{{.SampleFrames}}</td><td>{{printf "%.2f / %.2f" .FrameMsAvg .FrameMsP95}}</td></tr>{{end}}</table>{{else}}<p class="status">Report storage is unavailable.</p>{{end}}</body></html>{{define "nav"}}<nav><a href="/admin/">Overview</a>{{range .Sections}}<a href="{{.Path}}">{{.ShortName}}</a>{{end}}</nav>{{end}}`))

var adminNetplayTemplate = template.Must(template.New("netplay").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Netplay reports</title><link rel="stylesheet" href="/admin/style.css"></head><body><h1>Netplay reports</h1>{{template "nav" .}}<p>{{.NetplayCount}} retained reports; showing up to 500 latest.</p>{{if .Available}}<table><tr><th>Time bucket</th><th>Report ID</th><th>Version</th><th>Event</th></tr>{{range .Netplay}}<tr><td>{{.ServerTime}}</td><td>{{.ID}}</td><td>{{.AppVersion}}</td><td>{{.Event}}</td></tr>{{end}}</table>{{else}}<p class="status">Report storage is unavailable.</p>{{end}}</body></html>{{define "nav"}}<nav><a href="/admin/">Overview</a>{{range .Sections}}<a href="{{.Path}}">{{.ShortName}}</a>{{end}}</nav>{{end}}`))

type adminPageData struct {
	ServiceName      string
	Available        bool
	CrashCount       int
	FeedbackCount    int
	GameplayCount    int
	PerformanceCount int
	NetplayCount     int
	Crashes          []crashRecord
	Feedback         []feedbackRecord
	Gameplay         []gameplayRecord
	Performance      []performanceRecord
	Netplay          []netplayRecord
	Sections         []adminSection
}

type adminSection struct {
	Name      string
	ShortName string
	Path      string
	Count     int
}

type adminServer struct {
	store        *reportStore
	serviceName  string
	features     FeatureConfig
	usernameHash [32]byte
	passwordHash [32]byte
	mux          *http.ServeMux
}

func newAdminHandler(store *reportStore, username, password, serviceName string, features FeatureConfig) http.Handler {
	admin := &adminServer{
		store: store, usernameHash: sha256.Sum256([]byte(username)), passwordHash: sha256.Sum256([]byte(password)),
		serviceName: serviceName, features: features, mux: http.NewServeMux(),
	}
	admin.mux.HandleFunc("GET /admin/", admin.overview)
	admin.mux.HandleFunc("GET /admin/style.css", admin.style)
	if features.CrashReports {
		admin.mux.HandleFunc("GET /admin/crash", admin.crashes)
		admin.mux.HandleFunc("GET /admin/crash/{id}", admin.crashDetail)
	}
	if features.FeedbackReports {
		admin.mux.HandleFunc("GET /admin/feedback", admin.feedback)
		admin.mux.HandleFunc("GET /admin/feedback/{id}", admin.feedbackDetail)
	}
	if features.GameplayMetrics {
		admin.mux.HandleFunc("GET /admin/gameplay", admin.gameplay)
	}
	if features.PerformanceMetrics {
		admin.mux.HandleFunc("GET /admin/performance", admin.performance)
	}
	if features.MatchmakingReports {
		admin.mux.HandleFunc("GET /admin/netplay", admin.netplay)
	}
	return admin
}

func (admin *adminServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if requestIsHTTPS(r) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if !requestIsHTTPS(r) {
		http.Error(w, "Admin requires HTTPS", http.StatusBadRequest)
		return
	}
	username, password, ok := r.BasicAuth()
	usernameHash := sha256.Sum256([]byte(username))
	passwordHash := sha256.Sum256([]byte(password))
	credentialsMatch := subtle.ConstantTimeCompare(usernameHash[:], admin.usernameHash[:]) & subtle.ConstantTimeCompare(passwordHash[:], admin.passwordHash[:])
	if !ok || credentialsMatch != 1 {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic realm=%q, charset=\"UTF-8\"", admin.serviceName+" admin"))
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	admin.mux.ServeHTTP(w, r)
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return trustProxy && isTrustedProxy(remoteIP(r)) && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func latestRecords[T any](records []T) []T {
	start := max(0, len(records)-maxAdminRows)
	result := append([]T(nil), records[start:]...)
	slices.Reverse(result)
	return result
}

func (admin *adminServer) pageData() adminPageData {
	data := adminPageData{ServiceName: admin.serviceName, Available: admin.store != nil}
	if admin.features.CrashReports {
		data.Sections = append(data.Sections, adminSection{Name: "Crash reports", ShortName: "Crashes", Path: "/admin/crash"})
	}
	if admin.features.FeedbackReports {
		data.Sections = append(data.Sections, adminSection{Name: "Feedback", ShortName: "Feedback", Path: "/admin/feedback"})
	}
	if admin.features.GameplayMetrics {
		data.Sections = append(data.Sections, adminSection{Name: "Gameplay samples", ShortName: "Gameplay", Path: "/admin/gameplay"})
	}
	if admin.features.PerformanceMetrics {
		data.Sections = append(data.Sections, adminSection{Name: "Performance samples", ShortName: "Performance", Path: "/admin/performance"})
	}
	if admin.features.MatchmakingReports {
		data.Sections = append(data.Sections, adminSection{Name: "Netplay reports", ShortName: "Netplay", Path: "/admin/netplay"})
	}
	return data
}

func setAdminSectionCounts(data *adminPageData) {
	for index := range data.Sections {
		switch data.Sections[index].Path {
		case "/admin/crash":
			data.Sections[index].Count = data.CrashCount
		case "/admin/feedback":
			data.Sections[index].Count = data.FeedbackCount
		case "/admin/gameplay":
			data.Sections[index].Count = data.GameplayCount
		case "/admin/performance":
			data.Sections[index].Count = data.PerformanceCount
		case "/admin/netplay":
			data.Sections[index].Count = data.NetplayCount
		}
	}
}

func (admin *adminServer) overviewData() (adminPageData, error) {
	data := admin.pageData()
	if admin.store == nil {
		return data, nil
	}
	var err error
	if admin.features.CrashReports {
		data.CrashCount, err = admin.store.crashCount()
		if err != nil {
			return data, err
		}
	}
	if admin.features.FeedbackReports {
		data.FeedbackCount, err = admin.store.feedbackCount()
		if err != nil {
			return data, err
		}
	}
	if admin.features.GameplayMetrics {
		data.GameplayCount, err = admin.store.gameplayCount()
		if err != nil {
			return data, err
		}
	}
	if admin.features.PerformanceMetrics {
		data.PerformanceCount, err = admin.store.performanceCount()
		if err != nil {
			return data, err
		}
	}
	if admin.features.MatchmakingReports {
		data.NetplayCount, err = admin.store.netplayCount()
		if err != nil {
			return data, err
		}
	}
	setAdminSectionCounts(&data)
	return data, nil
}

func executeAdminTemplate(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Admin page unavailable", http.StatusInternalServerError)
	}
}

func (admin *adminServer) overview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	data, err := admin.overviewData()
	if err != nil {
		adminReadError(w)
		return
	}
	executeAdminTemplate(w, adminOverviewTemplate, data)
}

func (admin *adminServer) style(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(adminStylesheet))
}

func (admin *adminServer) crashes(w http.ResponseWriter, _ *http.Request) {
	data := admin.pageData()
	if admin.store != nil {
		records, err := admin.store.crashes()
		if err != nil {
			adminReadError(w)
			return
		}
		data.CrashCount = len(records)
		data.Crashes = latestRecords(records)
	}
	executeAdminTemplate(w, adminCrashTemplate, data)
}

func (admin *adminServer) crashDetail(w http.ResponseWriter, r *http.Request) {
	if admin.store == nil {
		http.NotFound(w, r)
		return
	}
	records, err := admin.store.crashes()
	if err != nil {
		adminReadError(w)
		return
	}
	for _, record := range records {
		if record.ID == r.PathValue("id") {
			executeAdminTemplate(w, adminCrashDetailTemplate, record)
			return
		}
	}
	http.NotFound(w, r)
}

func (admin *adminServer) feedback(w http.ResponseWriter, _ *http.Request) {
	data := admin.pageData()
	if admin.store != nil {
		records, err := admin.store.feedback()
		if err != nil {
			adminReadError(w)
			return
		}
		data.FeedbackCount = len(records)
		data.Feedback = latestRecords(records)
	}
	executeAdminTemplate(w, adminFeedbackTemplate, data)
}

func (admin *adminServer) feedbackDetail(w http.ResponseWriter, r *http.Request) {
	if admin.store == nil {
		http.NotFound(w, r)
		return
	}
	records, err := admin.store.feedback()
	if err != nil {
		adminReadError(w)
		return
	}
	for _, record := range records {
		if record.ID == r.PathValue("id") {
			executeAdminTemplate(w, adminFeedbackDetailTemplate, record)
			return
		}
	}
	http.NotFound(w, r)
}

func (admin *adminServer) performance(w http.ResponseWriter, _ *http.Request) {
	data := admin.pageData()
	if admin.store != nil {
		records, err := admin.store.performance()
		if err != nil {
			adminReadError(w)
			return
		}
		data.PerformanceCount = len(records)
		data.Performance = latestRecords(records)
	}
	executeAdminTemplate(w, adminPerformanceTemplate, data)
}

func (admin *adminServer) netplay(w http.ResponseWriter, _ *http.Request) {
	data := admin.pageData()
	if admin.store != nil {
		records, err := admin.store.netplay()
		if err != nil {
			adminReadError(w)
			return
		}
		data.NetplayCount = len(records)
		data.Netplay = latestRecords(records)
	}
	executeAdminTemplate(w, adminNetplayTemplate, data)
}

func adminReadError(w http.ResponseWriter) {
	http.Error(w, "Report storage unavailable", http.StatusServiceUnavailable)
}
