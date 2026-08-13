package main

import "time"

// DefaultConfig returns the bundled Antistatic profile. This file is the
// intended source-level customization point for games that want to ship their
// own defaults while retaining the generic server machinery.
//
// Callers receive their own event slice so one adaptation cannot mutate
// another config.
func DefaultConfig() Config {
	return Config{
		SchemaVersion: configSchemaVersion,
		Service: ServiceConfig{
			Name:            "Antistatic",
			CompatibilityID: "antistatic-v1",
		},
		Features: FeatureConfig{
			Events:             true,
			CrashReports:       true,
			FeedbackReports:    true,
			GameplayMetrics:    true,
			PerformanceMetrics: true,
			MatchmakingReports: true,
		},
		Timeouts: TimeoutConfig{
			LobbyMember:                ConfigDuration(defaultLobbyMemberTimeout),
			MatchmakingTicket:          ConfigDuration(defaultMatchmakingTicketTimeout),
			MatchmakingMatch:           ConfigDuration(defaultMatchmakingMatchTimeout),
			MatchmakingReportRetention: ConfigDuration(defaultMatchmakingReportRetention),
			MatchmakingTagLease:        ConfigDuration(defaultMatchmakingTagLeaseTimeout),
		},
		Events: []QueueEventConfig{
			{
				ID:             "americas-community-queue",
				Name:           "Americas community queue",
				Region:         "Americas",
				Weekday:        ConfigWeekday(time.Saturday),
				StartHourUTC:   21,
				StartMinuteUTC: 0,
				Duration:       ConfigDuration(time.Hour),
			},
			{
				ID:             "eurasia-community-queue",
				Name:           "Eurasia community queue",
				Region:         "Eurasia",
				Weekday:        ConfigWeekday(time.Sunday),
				StartHourUTC:   18,
				StartMinuteUTC: 0,
				Duration:       ConfigDuration(time.Hour),
			},
		},
	}
}
