package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCodeRegistrationRecoversLostResponse(t *testing.T) {
	h := newTestLobbyHandler()
	const proof = "client-persisted-ownership-secret"
	put := func(ticket, bearer, self, peer, secret string) *matchmakingResponse {
		t.Helper()
		rec := serveMatchmakingRequestWithTokenAndTags(h, http.MethodPut,
			"/0.9.5/matchmaking/code/"+ticket+"/45860", "198.51.100.10:32000",
			matchmakingRequest{Metadata: matchmakingMetadata{Character: "Carbon"}}, bearer, self, peer, secret)
		if rec.Code != http.StatusOK {
			t.Fatalf("registration returned %d: %s", rec.Code, rec.Body.String())
		}
		response := decodeMatchmakingResponse(t, rec)
		return &response
	}
	first := put("TicketA", "", "ALPHA1", "BRAVO2", proof)
	// Retry with only the preexisting proof: neither response token is used.
	recovered := put("TicketA", "", "ALPHA1", "BRAVO2", proof)
	if recovered.Token != first.Token || recovered.TagToken != proof || len(h.Tickets) != 1 || len(h.TagLeases) != 1 {
		t.Fatal("retry did not recover the original ticket and lease")
	}
	for _, claim := range []matchmakingTagPair{
		{Self: "ALPHA1", Peer: "BRAVO2"},
		{Self: "ALPHA1", Peer: "BRAVO2", SelfToken: "wrong-proof"},
		{Self: "BRAVO2", Peer: "ALPHA1", SelfToken: proof},
	} {
		rec := serveMatchmakingRequestWithTokenAndTags(h, http.MethodPut,
			"/0.9.5/matchmaking/code/TicketA/45860", "198.51.100.10:32000",
			matchmakingRequest{Metadata: matchmakingMetadata{Character: "Carbon"}}, "", claim.Self, claim.Peer, claim.SelfToken)
		if rec.Code == http.StatusOK {
			t.Fatalf("unauthorized claim recovered ticket: self=%s peer=%s", claim.Self, claim.Peer)
		}
	}
	// A restart may choose a new ticket ID while retaining its persisted proof.
	put("TicketB", "", "ALPHA1", "BRAVO2", proof)
	if len(h.Tickets) != 1 {
		t.Fatal("replacement left the orphaned waiting ticket behind")
	}
	deleted := serveMatchmakingRequestWithTokenAndTags(h, http.MethodDelete,
		"/0.9.5/matchmaking/code/TicketB/45860", "198.51.100.10:32000", nil,
		"", "ALPHA1", "BRAVO2", proof)
	if deleted.Code != http.StatusOK || len(h.Tickets) != 0 {
		t.Fatalf("lost-response cancellation returned %d, tickets=%d", deleted.Code, len(h.Tickets))
	}
}

func TestStaleCodeLeaseCannotDeleteReusedTicketKey(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			h := newTestLobbyHandler()
			first := serveMatchmakingRequestWithTokenAndTags(h, http.MethodPut,
				"/0.9.5/matchmaking/code/Shared/45860", "198.51.100.10:32000",
				matchmakingRequest{Metadata: matchmakingMetadata{Character: "Carbon"}}, "", "ALPHA1", "BRAVO2", "")
			if first.Code != http.StatusOK {
				t.Fatal(first.Body.String())
			}
			owner := decodeMatchmakingResponse(t, first)
			deleted := serveMatchmakingRequestWithTokenAndTags(h, http.MethodDelete,
				"/0.9.5/matchmaking/code/Shared/45860", "198.51.100.10:32000", nil,
				owner.Token, "ALPHA1", "BRAVO2", owner.TagToken)
			if deleted.Code != http.StatusOK {
				t.Fatal(deleted.Body.String())
			}
			lease := h.TagLeases[matchmakingTagLeaseKey("0.9.5", "ALPHA1")]
			if lease.TicketKey != "" {
				t.Fatal("deletion retained a stale lease reference")
			}
			second := serveMatchmakingRequestWithTokenAndTags(h, http.MethodPut,
				"/0.9.5/matchmaking/code/Shared/45861", "198.51.100.20:32000",
				matchmakingRequest{Metadata: matchmakingMetadata{Character: "Silicon"}}, "", "BRAVO2", "ALPHA1", "")
			if second.Code != http.StatusOK {
				t.Fatal(second.Body.String())
			}
			key := matchmakingTicketKey("0.9.5", "code.alpha1-bravo2", "Shared")
			peer := h.Tickets[key]
			// Exercise defense against stale references as well as detachment.
			lease.TicketKey = key
			if expire {
				lease.CheckedIn = time.Now().Add(-h.Config.Timeouts.MatchmakingTagLease.Duration() - time.Second)
				h.cleanupMatchmakingTagLeasesLocked(time.Now())
			} else {
				_, status := h.reserveMatchmakingTagLocked("0.9.5", "ALPHA1", owner.TagToken,
					"different-ticket", "198.51.100.10", time.Now())
				if status != http.StatusOK {
					t.Fatalf("lease move returned %d", status)
				}
			}
			if h.Tickets[key] != peer {
				t.Fatal("old lease deleted the new owner's search")
			}
		})
	}
}

func TestReportTicketStorageRemainsBounded(t *testing.T) {
	h := newTestLobbyHandler()
	now := time.Now()
	// Start close to the global bound, with valid unexpired report tickets.
	for i := range maxStoredMatchmakingTickets - 1 {
		ticket := &MatchmakingTicket{ID: fmt.Sprintf("Retained%d", i), Version: "0.9.5", Queue: "default",
			Token: "report-token", MatchedID: "finished-match", CheckedIn: now.Add(-time.Second)}
		h.Tickets[matchmakingTicketKey(ticket.Version, ticket.Queue, ticket.ID)] = ticket
	}
	for i := range 4 {
		first, status := h.refreshOrCreateMatchmakingTicketLocked(fmt.Sprintf("First%d", i), "0.9.5", "0.9.5",
			"default", "198.51.100.10", 45860, matchmakingMetadata{Character: "Carbon"}, "", nil, nil, nil, now)
		if status != http.StatusOK {
			t.Fatalf("first registration returned %d", status)
		}
		second, status := h.refreshOrCreateMatchmakingTicketLocked(fmt.Sprintf("Second%d", i), "0.9.5", "0.9.5",
			"default", "198.51.100.20", 45861, matchmakingMetadata{Character: "Silicon"}, "", nil, nil, nil, now)
		if status != http.StatusOK || second.Status != "matched" {
			t.Fatalf("second registration returned %d", status)
		}
		if len(h.Tickets) > maxStoredMatchmakingTickets {
			t.Fatalf("ticket storage grew to %d", len(h.Tickets))
		}
		if _, status := h.cancelMatchmakingLocked("0.9.5", "default", first.Ticket, first.Token, nil); status != http.StatusOK {
			t.Fatalf("cancellation returned %d", status)
		}
		// The peer keeps its authenticated report after cancellation.
		if _, status := h.matchmakingStateLocked("0.9.5", "default", second.Ticket, second.Token, now); status != http.StatusOK {
			t.Fatalf("recent report ticket was evicted: %d", status)
		}
	}
}
