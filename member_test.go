package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeLocalIPsAcceptsPrivateAndLoopbackAddresses(t *testing.T) {
	in := []string{
		"192.168.1.5",
		"10.0.0.5",
		"172.16.0.5",
		"127.0.0.1",
		"169.254.1.5",
		"::1",
		"fe80::1",
		"fc00::1",
	}
	out := sanitizeLocalIPs(in)
	if len(out) != len(in) {
		t.Fatalf("len(out) = %d, want %d (out=%v)", len(out), len(in), out)
	}
}

func TestSanitizeLocalIPsRejectsPublicAndMalformedAddresses(t *testing.T) {
	in := []string{
		"8.8.8.8",
		"74.167.71.212",
		"2001:db8::1",
		"not-an-ip",
		"",
		strings.Repeat("a", maxLocalIPLength+1),
		"192.168.1.5/24",
	}
	out := sanitizeLocalIPs(in)
	if out != nil {
		t.Fatalf("out = %v, want nil for fully-rejected input", out)
	}
}

func TestSanitizeLocalIPsDeduplicatesAndCanonicalizes(t *testing.T) {
	in := []string{"192.168.1.5", "192.168.001.005", "FE80::1", "fe80::1"}
	out := sanitizeLocalIPs(in)
	if !reflect.DeepEqual(out, []string{"192.168.1.5", "fe80::1"}) {
		t.Fatalf("out = %v, want canonical deduped list", out)
	}
}

func TestSanitizeLocalIPsCapsAtLimit(t *testing.T) {
	in := make([]string, 0, maxLocalIPsPerMember+5)
	for i := 0; i < maxLocalIPsPerMember+5; i++ {
		in = append(in, "10.0.0."+itoaForTest(i))
	}
	out := sanitizeLocalIPs(in)
	if len(out) != maxLocalIPsPerMember {
		t.Fatalf("len(out) = %d, want %d", len(out), maxLocalIPsPerMember)
	}
}

func TestMemberViewExposesLocalIPsOnlyToSamePublicIP(t *testing.T) {
	m := &Member{Endpoints: []Endpoint{{IP: "203.0.113.5", Port: 4444}}, LocalIPs: []string{"192.168.1.5"}}

	sameNAT := m.View("203.0.113.5")
	if !reflect.DeepEqual(sameNAT.LocalIPs, []string{"192.168.1.5"}) {
		t.Fatalf("same-NAT view local IPs = %v, want them visible", sameNAT.LocalIPs)
	}

	stranger := m.View("198.51.100.10")
	if stranger.LocalIPs != nil {
		t.Fatalf("stranger view local IPs = %v, want nil", stranger.LocalIPs)
	}

	noRequester := m.View("")
	if noRequester.LocalIPs != nil {
		t.Fatalf("empty-requester view local IPs = %v, want nil", noRequester.LocalIPs)
	}
}

func itoaForTest(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
