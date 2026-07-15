package main

import (
	"encoding/json"
	"net/http"
	"time"
)

const recurringEventDuration = 60 * time.Minute

type queueEventDefinition struct {
	ID             string
	Name           string
	Region         string
	Weekday        time.Weekday
	StartHourUTC   int
	StartMinuteUTC int
}

// These are deliberately public, recurring invitations rather than tracked
// sessions. The UTC schedule is stable through daylight-saving changes; the
// client converts StartsAtUTC/EndsAtUTC to the player's local time.
var recurringQueueEventDefinitions = []queueEventDefinition{
	{
		ID:             "americas-community-queue",
		Name:           "Americas community queue",
		Region:         "Americas",
		Weekday:        time.Saturday,
		StartHourUTC:   21,
		StartMinuteUTC: 0,
	},
	{
		ID:             "eurasia-community-queue",
		Name:           "Eurasia community queue",
		Region:         "Eurasia",
		Weekday:        time.Sunday,
		StartHourUTC:   18,
		StartMinuteUTC: 0,
	},
}

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

func eventStartOnDate(def queueEventDefinition, date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), def.StartHourUTC, def.StartMinuteUTC, 0, 0, time.UTC)
}

func nextRecurringEvent(def queueEventDefinition, now time.Time) recurringQueueEvent {
	now = now.UTC()
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var start time.Time
	for offset := 0; offset <= 7; offset++ {
		candidateDate := date.AddDate(0, 0, offset)
		if candidateDate.Weekday() != def.Weekday {
			continue
		}
		candidate := eventStartOnDate(def, candidateDate)
		end := candidate.Add(recurringEventDuration)
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
		Weekday:         def.Weekday.String(),
		StartHourUTC:    def.StartHourUTC,
		StartMinuteUTC:  def.StartMinuteUTC,
		DurationMinutes: int(recurringEventDuration / time.Minute),
		Active:          !now.Before(start) && now.Before(start.Add(recurringEventDuration)),
		StartsAtUTC:     start,
		EndsAtUTC:       start.Add(recurringEventDuration),
	}
}

func recurringQueueEvents(now time.Time) []recurringQueueEvent {
	events := make([]recurringQueueEvent, 0, len(recurringQueueEventDefinitions))
	for _, def := range recurringQueueEventDefinitions {
		events = append(events, nextRecurringEvent(def, now))
	}
	return events
}

func eventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(eventsResponse{Events: recurringQueueEvents(time.Now())}); err != nil {
		return
	}
}
