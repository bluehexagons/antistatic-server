package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const configSchemaVersion = 1
const maxConfigBytes = 64 * 1024
const maxConfiguredEvents = 32
const maxServiceNameLength = 80
const maxCompatibilityIDLength = 64
const maxEventIDLength = 64
const maxEventNameLength = 100
const maxEventRegionLength = 64

const defaultLobbyMemberTimeout = 30 * time.Second
const defaultMatchmakingTicketTimeout = 30 * time.Second
const defaultMatchmakingMatchTimeout = 2 * time.Minute
const defaultMatchmakingReportRetention = 20 * time.Minute
const defaultMatchmakingTagLeaseTimeout = time.Hour

const maxLobbyMemberTimeout = 10 * time.Minute
const maxMatchmakingTicketTimeout = 5 * time.Minute
const maxMatchmakingMatchTimeout = 15 * time.Minute
const maxMatchmakingReportRetention = 24 * time.Hour
const maxMatchmakingTagLeaseTimeout = 24 * time.Hour
const maxRecurringEventDuration = 24 * time.Hour

// ConfigDuration keeps profile files readable while leaving handlers with a
// parsed time.Duration. It is decoded once during startup.
type ConfigDuration time.Duration

func (d ConfigDuration) Duration() time.Duration {
	return time.Duration(d)
}

func (d ConfigDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *ConfigDuration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("must be a duration string such as \"30s\" or \"2m\"")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value, err)
	}
	*d = ConfigDuration(parsed)
	return nil
}

// ConfigWeekday is encoded as a full English weekday name in profile files.
type ConfigWeekday time.Weekday

func (d ConfigWeekday) Weekday() time.Weekday {
	return time.Weekday(d)
}

func (d ConfigWeekday) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Weekday(d).String())
}

func (d *ConfigWeekday) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("must be a weekday name")
	}
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		if strings.EqualFold(value, weekday.String()) {
			*d = ConfigWeekday(weekday)
			return nil
		}
	}
	return fmt.Errorf("invalid weekday %q", value)
}

type ServiceConfig struct {
	Name            string `json:"name"`
	CompatibilityID string `json:"compatibility_id"`
}

type FeatureConfig struct {
	Events             bool `json:"events"`
	CrashReports       bool `json:"crash_reports"`
	FeedbackReports    bool `json:"feedback_reports"`
	GameplayMetrics    bool `json:"gameplay_metrics"`
	PerformanceMetrics bool `json:"performance_metrics"`
	MatchmakingReports bool `json:"matchmaking_reports"`
}

type TimeoutConfig struct {
	LobbyMember                ConfigDuration `json:"lobby_member"`
	MatchmakingTicket          ConfigDuration `json:"matchmaking_ticket"`
	MatchmakingMatch           ConfigDuration `json:"matchmaking_match"`
	MatchmakingReportRetention ConfigDuration `json:"matchmaking_report_retention"`
	MatchmakingTagLease        ConfigDuration `json:"matchmaking_tag_lease"`
}

type QueueEventConfig struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Region         string         `json:"region"`
	Weekday        ConfigWeekday  `json:"weekday"`
	StartHourUTC   int            `json:"start_hour_utc"`
	StartMinuteUTC int            `json:"start_minute_utc"`
	Duration       ConfigDuration `json:"duration"`
}

// Config contains game-facing policy. Listener, TLS, storage, proxy, and
// credential settings remain deployment configuration.
type Config struct {
	SchemaVersion int                `json:"schema_version"`
	Service       ServiceConfig      `json:"service"`
	Features      FeatureConfig      `json:"features"`
	Timeouts      TimeoutConfig      `json:"timeouts"`
	Events        []QueueEventConfig `json:"events"`
}

// LoadConfig overlays a profile on the Antistatic defaults. Omitting a field
// keeps its default; an explicit empty events array removes every event.
func LoadConfig(path string) (Config, error) {
	config := DefaultConfig()
	if strings.TrimSpace(path) == "" {
		return config, validateConfig(config)
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	if len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maxConfigBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := validateConfig(config); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return config, nil
}

func validateConfig(config Config) error {
	if config.SchemaVersion != configSchemaVersion {
		return fmt.Errorf("schema_version must be %d", configSchemaVersion)
	}
	trimmedServiceName := strings.TrimSpace(config.Service.Name)
	if trimmedServiceName == "" || len(config.Service.Name) > maxServiceNameLength || config.Service.Name != trimmedServiceName {
		return fmt.Errorf("service.name must contain 1-%d bytes", maxServiceNameLength)
	}
	if len(config.Service.CompatibilityID) > maxCompatibilityIDLength || !coarseIdentifierPattern.MatchString(config.Service.CompatibilityID) {
		return fmt.Errorf("service.compatibility_id must contain 1-%d letters, digits, dots, underscores, or hyphens", maxCompatibilityIDLength)
	}
	durations := []struct {
		name  string
		value time.Duration
		max   time.Duration
	}{
		{name: "timeouts.lobby_member", value: config.Timeouts.LobbyMember.Duration(), max: maxLobbyMemberTimeout},
		{name: "timeouts.matchmaking_ticket", value: config.Timeouts.MatchmakingTicket.Duration(), max: maxMatchmakingTicketTimeout},
		{name: "timeouts.matchmaking_match", value: config.Timeouts.MatchmakingMatch.Duration(), max: maxMatchmakingMatchTimeout},
		{name: "timeouts.matchmaking_report_retention", value: config.Timeouts.MatchmakingReportRetention.Duration(), max: maxMatchmakingReportRetention},
		{name: "timeouts.matchmaking_tag_lease", value: config.Timeouts.MatchmakingTagLease.Duration(), max: maxMatchmakingTagLeaseTimeout},
	}
	for _, duration := range durations {
		if duration.value <= 0 || duration.value > duration.max {
			return fmt.Errorf("%s must be greater than zero and no more than %s", duration.name, duration.max)
		}
	}
	if len(config.Events) > maxConfiguredEvents {
		return fmt.Errorf("events must contain no more than %d entries", maxConfiguredEvents)
	}
	seen := make(map[string]struct{}, len(config.Events))
	for index, event := range config.Events {
		field := fmt.Sprintf("events[%d]", index)
		if event.ID == "" || len(event.ID) > maxEventIDLength {
			return fmt.Errorf("%s.id must contain 1-%d bytes", field, maxEventIDLength)
		}
		if _, exists := seen[event.ID]; exists {
			return fmt.Errorf("%s.id duplicates %q", field, event.ID)
		}
		seen[event.ID] = struct{}{}
		if event.Name == "" || len(event.Name) > maxEventNameLength {
			return fmt.Errorf("%s.name must contain 1-%d bytes", field, maxEventNameLength)
		}
		if event.Region == "" || len(event.Region) > maxEventRegionLength {
			return fmt.Errorf("%s.region must contain 1-%d bytes", field, maxEventRegionLength)
		}
		if event.Weekday.Weekday() < time.Sunday || event.Weekday.Weekday() > time.Saturday {
			return fmt.Errorf("%s.weekday is invalid", field)
		}
		if event.StartHourUTC < 0 || event.StartHourUTC > 23 {
			return fmt.Errorf("%s.start_hour_utc must be between 0 and 23", field)
		}
		if event.StartMinuteUTC < 0 || event.StartMinuteUTC > 59 {
			return fmt.Errorf("%s.start_minute_utc must be between 0 and 59", field)
		}
		if event.Duration.Duration() <= 0 || event.Duration.Duration() > maxRecurringEventDuration {
			return fmt.Errorf("%s.duration must be greater than zero and no more than %s", field, maxRecurringEventDuration)
		}
	}
	return nil
}
