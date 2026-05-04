package main

import (
	"net"
	"time"
)

type Member struct {
	IP        string    `json:"ip"`
	Port      int       `json:"port"`
	LocalIPs  []string  `json:"local_ips,omitempty"`
	Token     string    `json:"-"`
	CheckedIn time.Time `json:"-"`
}

// MemberView is the per-request projection of a Member. LocalIPs are only
// included for peers behind the same public IP as the requesting client; this
// keeps RFC 1918 / ULA addresses from leaking to unrelated WAN strangers in the
// lobby while still letting same-NAT peers discover each other for tunneling.
type MemberView struct {
	IP       string   `json:"ip"`
	Port     int      `json:"port"`
	LocalIPs []string `json:"local_ips,omitempty"`
}

const memberTimeout = 30 * time.Second

// maxLocalIPsPerMember caps how many LAN candidates a peer may publish; eight
// covers the common multi-NIC / docker / WSL setups without giving a hostile
// client room to flood the lobby with garbage.
const maxLocalIPsPerMember = 8

// maxLocalIPLength is a defensive cap on each entry; an IPv6 textual address
// with a zone identifier comfortably fits under this bound.
const maxLocalIPLength = 64

func (m *Member) Stale() bool {
	now := time.Now()
	return now.After(m.CheckedIn.Add(memberTimeout))
}

// View renders this member for a request from `requesterIP`. LAN addresses are
// only exposed to peers sharing this member's public IP.
func (m *Member) View(requesterIP string) MemberView {
	v := MemberView{IP: m.IP, Port: m.Port}
	if requesterIP != "" && m.IP == requesterIP && len(m.LocalIPs) > 0 {
		v.LocalIPs = append(v.LocalIPs, m.LocalIPs...)
	}
	return v
}

// sanitizeLocalIPs filters a caller-supplied list down to syntactically valid,
// non-public IP literals: RFC 1918 IPv4, link-local IPv4, IPv4 loopback,
// unique-local IPv6 (fc00::/7), link-local IPv6, and IPv6 loopback. Anything
// else (public addresses, malformed entries, duplicates, oversized strings) is
// dropped silently. The list is also capped at maxLocalIPsPerMember.
func sanitizeLocalIPs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		if len(raw) == 0 || len(raw) > maxLocalIPLength {
			continue
		}
		parsed := net.ParseIP(raw)
		if parsed == nil {
			continue
		}
		if !isLocalScopeIP(parsed) {
			continue
		}
		canonical := parsed.String()
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
		if len(out) >= maxLocalIPsPerMember {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isLocalScopeIP reports whether ip is in a non-globally-routable range that
// is meaningful to advertise to same-NAT peers for hole-punching fallback.
func isLocalScopeIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}
