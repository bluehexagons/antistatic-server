package main

import (
	"encoding/json"
	"net/http"
	"time"
)

type recurringQueueEvent struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Region          string    `json:"region"`
	Weekday         string    `json:"weekday"`
	StartHourUTC    int       `json:"start_hour_utc"`
	StartMinuteUTC  int       `json:"start_minute_utc"`
	DurationMinutes int       `json:"duration_minutes"`
	Active          bool      `json:"active"`
	StartsAtUTC     time.Time `json:"starts_at_utc"`
	EndsAtUTC       time.Time `json:"ends_at_utc"`
}

type eventsResponse struct {
	Events []recurringQueueEvent `json:"events"`
}

func eventStartOnDate(def QueueEventConfig, date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), def.StartHourUTC, def.StartMinuteUTC, 0, 0, time.UTC)
}

func nextRecurringEvent(def QueueEventConfig, now time.Time) recurringQueueEvent {
	now = now.UTC()
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	duration := def.Duration.Duration()
	var start time.Time
	for offset := 0; offset <= 7; offset++ {
		candidateDate := date.AddDate(0, 0, offset)
		if candidateDate.Weekday() != def.Weekday.Weekday() {
			continue
		}
		candidate := eventStartOnDate(def, candidateDate)
		end := candidate.Add(duration)
		if (now.Equal(candidate) || now.After(candidate)) && now.Before(end) {
			start = candidate
			break
		}
		if candidate.After(now) {
			start = candidate
			break
		}
	}
	if start.IsZero() {
		start = eventStartOnDate(def, date.AddDate(0, 0, 7))
	}
	return recurringQueueEvent{
		ID:              def.ID,
		Name:            def.Name,
		Region:          def.Region,
		Weekday:         def.Weekday.Weekday().String(),
		StartHourUTC:    def.StartHourUTC,
		StartMinuteUTC:  def.StartMinuteUTC,
		DurationMinutes: int(duration / time.Minute),
		Active:          !now.Before(start) && now.Before(start.Add(duration)),
		StartsAtUTC:     start,
		EndsAtUTC:       start.Add(duration),
	}
}

func recurringQueueEvents(definitions []QueueEventConfig, now time.Time) []recurringQueueEvent {
	events := make([]recurringQueueEvent, 0, len(definitions))
	for _, def := range definitions {
		events = append(events, nextRecurringEvent(def, now))
	}
	return events
}

func eventsHandler(definitions []QueueEventConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(eventsResponse{Events: recurringQueueEvents(definitions, time.Now())})
	}
}
