package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecurringQueueEventsUseStableUTCSchedule(t *testing.T) {
	now := time.Date(2026, time.July, 18, 21, 30, 0, 0, time.UTC) // Saturday
	events := recurringQueueEvents(DefaultConfig().Events, now)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if !events[0].Active || !events[0].StartsAtUTC.Equal(time.Date(2026, time.July, 18, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("Americas event = %#v, want active Saturday 21:00 UTC", events[0])
	}
	if events[1].Active || !events[1].StartsAtUTC.Equal(time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("Eurasia event = %#v, want next Sunday 18:00 UTC", events[1])
	}

	after := recurringQueueEvents(DefaultConfig().Events, time.Date(2026, time.July, 18, 22, 0, 0, 0, time.UTC))
	if after[0].Active || !after[0].StartsAtUTC.Equal(time.Date(2026, time.July, 25, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("event after end = %#v, want next Saturday", after[0])
	}
}

func TestRecurringQueueEventRemainsActiveAcrossUTCMidnight(t *testing.T) {
	definition := QueueEventConfig{
		ID: "late-saturday", Name: "Late Saturday", Region: "global",
		Weekday: ConfigWeekday(time.Saturday), StartHourUTC: 23, StartMinuteUTC: 30,
		Duration: ConfigDuration(2 * time.Hour),
	}
	now := time.Date(2026, time.July, 19, 0, 30, 0, 0, time.UTC) // Sunday

	event := nextRecurringEvent(definition, now)
	wantStart := time.Date(2026, time.July, 18, 23, 30, 0, 0, time.UTC)
	if !event.Active || !event.StartsAtUTC.Equal(wantStart) || !event.EndsAtUTC.Equal(wantStart.Add(2*time.Hour)) {
		t.Fatalf("cross-midnight event = %#v, want active occurrence starting %v", event, wantStart)
	}
}

func TestEventsHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	eventsHandler(DefaultConfig().Events)(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("events response = status %d, cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var response eventsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(response.Events) != 2 {
		t.Fatalf("events response = %#v, want two events", response.Events)
	}
}
