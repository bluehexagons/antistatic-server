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
	events := recurringQueueEvents(now)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if !events[0].Active || !events[0].StartsAtUTC.Equal(time.Date(2026, time.July, 18, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("Americas event = %#v, want active Saturday 21:00 UTC", events[0])
	}
	if events[1].Active || !events[1].StartsAtUTC.Equal(time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("Eurasia event = %#v, want next Sunday 18:00 UTC", events[1])
	}

	after := recurringQueueEvents(time.Date(2026, time.July, 18, 22, 0, 0, 0, time.UTC))
	if after[0].Active || !after[0].StartsAtUTC.Equal(time.Date(2026, time.July, 25, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("event after end = %#v, want next Saturday", after[0])
	}
}

func TestEventsHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	eventsHandler(rec, req)
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
