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

func TestMatchmakingCodeRecoveryAfterIntroductionEnds(t *testing.T) {
	for _, ending := range []string{"peer-cancellation", "match-expiration"} {
		for _, ownership := range []string{"current", "moved", "expired"} {
			for _, method := range []string{http.MethodPut, http.MethodDelete} {
				t.Run(ending+"/"+ownership+"/"+method, func(t *testing.T) {
					h := newTestLobbyHandler()
					const proof = "persisted-before-registration"
					const target = "/0.9.5/matchmaking/code/TicketA/45860"
					body := matchmakingRequest{Metadata: matchmakingMetadata{Character: "Carbon"}}
					// The client never receives this response, so only its persisted
					// code proof can recover or cancel the retained ticket.
					first := serveMatchmakingRequestWithTokenAndTags(h, http.MethodPut, target,
						"198.51.100.10:32000", body, "", "ALPHA1", "BRAVO2", proof)
					if first.Code != http.StatusOK {
						t.Fatalf("initial registration = %d: %s", first.Code, first.Body.String())
					}
					firstResponse := decodeMatchmakingResponse(t, first)
					second := serveMatchmakingRequestWithTokenAndTags(h, http.MethodPut,
						"/0.9.5/matchmaking/code/TicketB/45861", "198.51.100.20:32000",
						body, "", "BRAVO2", "ALPHA1", "peer-proof")
					if second.Code != http.StatusOK || decodeMatchmakingResponse(t, second).Status != "matched" {
						t.Fatalf("peer match = %d: %s", second.Code, second.Body.String())
					}
					if ending == "peer-cancellation" {
						deleted := serveMatchmakingRequestWithTokenAndTags(h, http.MethodDelete,
							"/0.9.5/matchmaking/code/TicketB/45861", "198.51.100.20:32000",
							nil, "", "BRAVO2", "ALPHA1", "peer-proof")
						if deleted.Code != http.StatusOK {
							t.Fatalf("peer cancellation = %d: %s", deleted.Code, deleted.Body.String())
						}
					} else {
						for _, match := range h.Matches {
							match.CreatedAt = time.Now().Add(-h.Config.Timeouts.MatchmakingMatch.Duration() - time.Second)
						}
						h.cleanupMatchmakingLocked(time.Now())
					}
					key := matchmakingTicketKey("0.9.5", "code.alpha1-bravo2", "TicketA")
					retained := h.Tickets[key]
					if retained == nil || len(retained.Endpoints) != 0 || retained.Metadata.Character != "" ||
						retained.SelfTag != "" || retained.PeerTag != "" || retained.TagToken != "" {
						t.Fatal("terminal ticket must discard player and raw code-claim metadata")
					}
					switch ownership {
					case "moved":
						moved := serveMatchmakingRequestWithTokenAndTags(h, http.MethodPut,
							"/0.9.5/matchmaking/code/NewTicket/45862", "198.51.100.10:32000",
							body, "", "ALPHA1", "CHARL3", proof)
						if moved.Code != http.StatusOK {
							t.Fatalf("lease move = %d: %s", moved.Code, moved.Body.String())
						}
					case "expired":
						lease := h.TagLeases[matchmakingTagLeaseKey("0.9.5", "ALPHA1")]
						lease.CheckedIn = time.Now().Add(-h.Config.Timeouts.MatchmakingTagLease.Duration() - time.Second)
					}
					var requestBody any
					if method == http.MethodPut {
						requestBody = body
					}
					for _, claim := range []matchmakingTagPair{
						{Self: "ALPHA1", Peer: "BRAVO2", SelfToken: "wrong-proof"},
						{Self: "BRAVO2", Peer: "ALPHA1", SelfToken: proof},
					} {
						rejected := serveMatchmakingRequestWithTokenAndTags(h, method, target,
							"198.51.100.10:32000", requestBody, "", claim.Self, claim.Peer, claim.SelfToken)
						if rejected.Code != http.StatusForbidden {
							t.Fatalf("%s with wrong claim = %d, want 403", method, rejected.Code)
						}
					}
					recovered := serveMatchmakingRequestWithTokenAndTags(h, method, target,
						"198.51.100.10:32000", requestBody, "", "ALPHA1", "BRAVO2", proof)
					if ownership != "current" {
						if recovered.Code != http.StatusForbidden || h.Tickets[key] != retained {
							t.Fatalf("%s after lease %s = %d, want 403 and unchanged ticket", method, ownership, recovered.Code)
						}
						return
					}
					if recovered.Code != http.StatusOK {
						t.Fatalf("proof-only %s = %d: %s", method, recovered.Code, recovered.Body.String())
					}
					response := decodeMatchmakingResponse(t, recovered)
					if response.Status != "canceled" || response.Token != firstResponse.Token || len(response.Endpoints) != 0 {
						t.Fatalf("proof-only %s = %#v, want original canceled ticket without endpoints", method, response)
					}
					if ownership == "current" && method == http.MethodDelete && h.Tickets[key] != nil {
						t.Fatal("proof-only cancellation left the retained ticket")
					}
				})
			}
		}
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
			for _, method := range []string{http.MethodPut, http.MethodDelete} {
				var requestBody any
				if method == http.MethodPut {
					requestBody = matchmakingRequest{Metadata: matchmakingMetadata{Character: "Silicon"}}
				}
				recovered := serveMatchmakingRequestWithTokenAndTags(h, method,
					"/0.9.5/matchmaking/code/Shared/45861", "198.51.100.10:32000", requestBody,
					"", "ALPHA1", "BRAVO2", owner.TagToken)
				if recovered.Code != http.StatusForbidden || h.Tickets[key] != peer {
					t.Fatalf("stale-lease %s = %d, want 403 and unchanged ticket", method, recovered.Code)
				}
			}
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
			"default", "198.51.100.10", 45860, matchmakingMetadata{Character: "Carbon"}, "", nil, nil, nil, "", now)
		if status != http.StatusOK {
			t.Fatalf("first registration returned %d", status)
		}
		second, status := h.refreshOrCreateMatchmakingTicketLocked(fmt.Sprintf("Second%d", i), "0.9.5", "0.9.5",
			"default", "198.51.100.20", 45861, matchmakingMetadata{Character: "Silicon"}, "", nil, nil, nil, "", now)
		if status != http.StatusOK || second.Status != "matched" {
			t.Fatalf("second registration returned %d", status)
		}
		if len(h.Tickets) > maxStoredMatchmakingTickets {
			t.Fatalf("ticket storage grew to %d", len(h.Tickets))
		}
		if _, status := h.cancelMatchmakingLocked("0.9.5", "default", first.Ticket, first.Token, nil, "", time.Now()); status != http.StatusOK {
			t.Fatalf("cancellation returned %d", status)
		}
		// The peer keeps its authenticated report after cancellation.
		if _, status := h.matchmakingStateLocked("0.9.5", "default", second.Ticket, second.Token, now); status != http.StatusOK {
			t.Fatalf("recent report ticket was evicted: %d", status)
		}
	}
}
