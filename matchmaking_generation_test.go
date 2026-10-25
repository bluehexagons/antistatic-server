package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func registerGeneration(h *lobbyHandler, id, queue, bearer, proof string, tags *matchmakingTagPair, now time.Time) (*matchmakingResponse, int) {
	return h.refreshOrCreateMatchmakingTicketLocked(id, "0.9.5", "0.9.5", queue,
		"198.51.100.10", 45860, matchmakingMetadata{Character: "Carbon"}, bearer, tags, nil, nil, proof, now)
}

func TestOrdinaryRegistrationRecoversLostResponse(t *testing.T) {
	for _, state := range []string{"waiting", "matched", "canceled"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			t.Run(state+"/"+method, func(t *testing.T) {
				h := newTestLobbyHandler()
				now := time.Now()
				first, status := registerGeneration(h, "First", "default", "", "persisted-proof", nil, now)
				if status != http.StatusOK {
					t.Fatal(status)
				}
				if state != "waiting" {
					peer, status := h.refreshOrCreateMatchmakingTicketLocked("Peer", "0.9.5", "0.9.5", "default",
						"198.51.100.20", 45861, matchmakingMetadata{Character: "Silicon"}, "", nil, nil, nil, "peer-proof", now)
					if status != http.StatusOK || peer.Status != "matched" {
						t.Fatalf("peer: %d %#v", status, peer)
					}
					if state == "canceled" {
						if _, status := h.cancelMatchmakingLocked("0.9.5", "default", "Peer", peer.Token, nil, "", now); status != http.StatusOK {
							t.Fatal(status)
						}
					}
				}
				attempt := func(bearer, proof string) (*matchmakingResponse, int) {
					if method == http.MethodDelete {
						return h.cancelMatchmakingLocked("0.9.5", "default", "First", bearer, nil, proof, now)
					}
					return registerGeneration(h, "First", "default", bearer, proof, nil, now)
				}
				for _, credentials := range [][2]string{{"", ""}, {"", "wrong"}, {"wrong-bearer", "persisted-proof"}} {
					if _, status := attempt(credentials[0], credentials[1]); status != http.StatusForbidden {
						t.Fatalf("wrong credentials: %d", status)
					}
				}
				recovered, status := attempt("", "persisted-proof")
				if status != http.StatusOK || recovered.Token != first.Token {
					t.Fatalf("recovery: %d %#v", status, recovered)
				}
				if method == http.MethodPut && recovered.Status != state {
					t.Fatalf("state = %s, want %s", recovered.Status, state)
				}
				if method == http.MethodDelete && h.Tickets[matchmakingTicketKey("0.9.5", "default", "First")] != nil {
					t.Fatal("ticket survived cancellation")
				}
			})
		}
	}
}

func TestCanceledGenerationRejectsDelayedRegistration(t *testing.T) {
	for _, code := range []bool{false, true} {
		for _, beforePut := range []bool{false, true} {
			t.Run(fmt.Sprintf("code=%t/delete-before-put=%t", code, beforePut), func(t *testing.T) {
				h := newTestLobbyHandler()
				now := time.Now()
				queue, proof := "default", "ordinary-proof"
				var tags *matchmakingTagPair
				if code {
					queue = "code.alpha1-bravo2"
					tags = &matchmakingTagPair{Self: "ALPHA1", Peer: "BRAVO2", SelfToken: "code-proof"}
				}
				if !beforePut {
					if _, status := registerGeneration(h, "Old", queue, "", proof, tags, now); status != http.StatusOK {
						t.Fatal(status)
					}
				}
				_, status := h.cancelMatchmakingLocked("0.9.5", queue, "Old", "", tags, proof, now)
				if status != http.StatusOK && status != http.StatusNoContent {
					t.Fatal(status)
				}
				for i := 0; i < 2; i++ {
					if _, status := registerGeneration(h, "Old", queue, "", proof, tags, now); status != http.StatusConflict {
						t.Fatalf("delayed registration: %d", status)
					}
					if _, status := h.cancelMatchmakingLocked("0.9.5", queue, "Old", "", tags, proof, now); status != http.StatusNoContent {
						t.Fatal(status)
					}
				}
				if len(h.Tickets) != 0 {
					t.Fatal("canceled ticket recreated")
				}
				if _, status := registerGeneration(h, "New", queue, "", proof, tags, now); status != http.StatusOK {
					t.Fatalf("new generation: %d", status)
				}
				if _, status := registerGeneration(h, "Old", queue, "", proof, tags, now); status != http.StatusConflict {
					t.Fatalf("old generation reclaimed lease: %d", status)
				}
				if h.Tickets[matchmakingTicketKey("0.9.5", queue, "New")] == nil {
					t.Fatal("new search displaced")
				}
			})
		}
	}
}

func TestReplacedCodeGenerationCannotReclaimLease(t *testing.T) {
	h := newTestLobbyHandler()
	now := time.Now()
	tags := &matchmakingTagPair{Self: "ALPHA1", Peer: "BRAVO2", SelfToken: "persisted-proof"}
	for _, id := range []string{"Old", "New"} {
		if _, status := registerGeneration(h, id, "code.alpha1-bravo2", "", "", tags, now); status != http.StatusOK {
			t.Fatal(status)
		}
	}
	if _, status := registerGeneration(h, "Old", "code.alpha1-bravo2", "", "", tags, now); status != http.StatusConflict {
		t.Fatal(status)
	}
	if len(h.Tickets) != 1 {
		t.Fatal("old search survived replacement")
	}
	// The reciprocal owner may reuse the canonical key with its own proof.
	peerTags := &matchmakingTagPair{Self: "BRAVO2", Peer: "ALPHA1", SelfToken: "peer-proof"}
	if _, status := registerGeneration(h, "Old", "code.alpha1-bravo2", "", "", peerTags, now); status != http.StatusOK {
		t.Fatalf("reciprocal reuse: %d", status)
	}
}

func TestGenerationCapacityReservesCancellation(t *testing.T) {
	h := newTestLobbyHandler()
	now := time.Now()
	tags := &matchmakingTagPair{Self: "ALPHA1", Peer: "BRAVO2", SelfToken: "persisted-proof"}
	first, status := registerGeneration(h, "First", "code.alpha1-bravo2", "", "", tags, now)
	if status != http.StatusOK {
		t.Fatal(status)
	}
	for i := len(h.Generations); i < maxMatchmakingGenerations; i++ {
		key := sha256.Sum256([]byte(fmt.Sprint(i)))
		h.Generations[key] = &matchmakingGeneration{Canceled: true, ExpiresAt: now.Add(time.Hour)}
	}
	if _, status := registerGeneration(h, "New", "code.alpha1-bravo2", "", "", tags, now); status != http.StatusServiceUnavailable {
		t.Fatal(status)
	}
	if h.Tickets[matchmakingTicketKey("0.9.5", "code.alpha1-bravo2", "First")] == nil {
		t.Fatal("failed admission removed preceding search")
	}
	if _, status := h.cancelMatchmakingLocked("0.9.5", "code.alpha1-bravo2", "First", first.Token, tags, "", now); status != http.StatusOK {
		t.Fatalf("reserved cancellation: %d", status)
	}
	if _, status := registerGeneration(h, "First", "code.alpha1-bravo2", "", "", tags, now); status != http.StatusConflict {
		t.Fatal(status)
	}
	if len(h.Generations) != maxMatchmakingGenerations {
		t.Fatal("ledger exceeded cap or evicted protected entries")
	}
	if _, status := h.cancelMatchmakingLocked("0.9.5", "default", "Absent", "", nil, "unknown-proof", now); status != http.StatusServiceUnavailable {
		t.Fatal(status)
	}
}

func TestGenerationRetentionBoundaries(t *testing.T) {
	for _, code := range []bool{false, true} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h := newTestLobbyHandler()
			now := time.Now()
			queue := "default"
			proof := "ordinary-proof"
			var tags *matchmakingTagPair
			if code {
				queue = "code.alpha1-bravo2"
				tags = &matchmakingTagPair{Self: "ALPHA1", Peer: "BRAVO2", SelfToken: "code-proof"}
			}
			if _, status := registerGeneration(h, "Old", queue, "", proof, tags, now); status != http.StatusOK {
				t.Fatal(status)
			}
			if _, status := h.cancelMatchmakingLocked("0.9.5", queue, "Old", "", tags, proof, now); status != http.StatusOK {
				t.Fatal(status)
			}
			key := matchmakingGenerationKey(matchmakingTicketKey("0.9.5", queue, "Old"), tags, proof)
			expiry := now.Add(h.Config.Timeouts.MatchmakingReportRetention.Duration())
			if code {
				lease := h.TagLeases[matchmakingTagLeaseKey("0.9.5", "ALPHA1")]
				lease.CheckedIn = now.Add(time.Minute) // Renewals retain old cancellation protection.
				expiry = lease.CheckedIn.Add(h.Config.Timeouts.MatchmakingTagLease.Duration())
			}
			h.cleanupMatchmakingGenerationsLocked(expiry)
			if h.Generations[key] == nil {
				t.Fatal("protection expired early")
			}
			// Repeated DELETE must not extend the recorded deadline.
			if _, status := h.cancelMatchmakingLocked("0.9.5", queue, "Old", "", tags, proof, expiry); status != http.StatusNoContent {
				t.Fatal(status)
			}
			h.cleanupMatchmakingGenerationsLocked(expiry.Add(time.Nanosecond))
			if h.Generations[key] != nil {
				t.Fatal("protection outlived documented window")
			}
		})
	}
}
