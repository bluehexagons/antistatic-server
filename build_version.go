package main

// buildVersion is supplied by release and deployment builds with -X main.buildVersion.
var buildVersion string

func init() {
	if buildVersion != "" {
		serverVersion = normalizeServerVersion(buildVersion)
	}
}
