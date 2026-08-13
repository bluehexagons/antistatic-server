package main

import (
	"html/template"
	"net/http"
)

// adminGameplayTemplate and gameplay are the Antistatic-specific presentation
// for the gameplay metric schema in antistatic_reports.go.
var adminGameplayTemplate = template.Must(template.New("gameplay").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Gameplay metrics</title><link rel="stylesheet" href="/admin/style.css"></head><body><h1>Gameplay metrics</h1>{{template "nav" .}}<p>{{.GameplayCount}} retained coarse samples; showing up to 500 latest.</p>{{if .Available}}<table><tr><th>Time bucket</th><th>Version</th><th>Mode</th><th>Stage</th><th>Characters</th><th>Result</th><th>Frames</th></tr>{{range .Gameplay}}<tr><td>{{.ServerTime}}</td><td>{{.AppVersion}}</td><td>{{.Mode}}{{if .Online}} (online){{end}}</td><td>{{.Stage}}</td><td>{{.Character}} / {{.OpponentCharacter}}</td><td>{{.Result}}</td><td>{{.DurationFrames}}</td></tr>{{end}}</table>{{else}}<p class="status">Report storage is unavailable.</p>{{end}}</body></html>{{define "nav"}}<nav><a href="/admin/">Overview</a>{{range .Sections}}<a href="{{.Path}}">{{.ShortName}}</a>{{end}}</nav>{{end}}`))

func (admin *adminServer) gameplay(w http.ResponseWriter, _ *http.Request) {
	data := admin.pageData()
	if admin.store != nil {
		records, err := admin.store.gameplay()
		if err != nil {
			adminReadError(w)
			return
		}
		data.GameplayCount = len(records)
		data.Gameplay = latestRecords(records)
	}
	executeAdminTemplate(w, adminGameplayTemplate, data)
}
