package main

import (
	"net"
	"strconv"
	"time"
)

// Endpoint is a single (IP, Port) pair observed for a member or matchmaking
// ticket. Each public family (v4 / v6) gets its own entry so peers can pick
// whichever family they can route on. The IP is whatever the server saw the
// PUT come from (X-Forwarded-For via the trusted proxy header, or the raw
// remote address otherwise); the port is the externally observed UDP port
// supplied by the client after running STUN over that family.
type Endpoint struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type Member struct {
	// Endpoints lists every (IP, Port) the member has checked in from. The
	// first entry is the original / primary endpoint (kept stable so that
	// older clients reading the flat top-level IP/Port fields keep seeing
	// the same value across refresh cycles). Additional entries are added
	// when the member checks in over a second address family.
	Endpoints      []Endpoint
	LocalIPs       []string
	LocalEndpoints []Endpoint
	Token          string
	CheckedIn      time.Time
}

// MemberView is the per-request projection of a Member. Endpoints lists
// every family the member is reachable on (always at least one entry).
// LocalIPs are only included for peers behind the same public IP as the
// requesting client.
type MemberView struct {
	Endpoints      []Endpoint `json:"endpoints"`
	LocalIPs       []string   `json:"local_ips,omitempty"`
	LocalEndpoints []Endpoint `json:"local_endpoints,omitempty"`
}

// maxLocalIPsPerMember caps how many LAN candidates a peer may publish; eight
// covers the common multi-NIC / docker / WSL setups without giving a hostile
// client room to flood the lobby with garbage.
const maxLocalIPsPerMember = 8

// maxLocalIPLength is a defensive cap on each entry; an IPv6 textual address
// with a zone identifier comfortably fits under this bound.
const maxLocalIPLength = 64

// maxEndpointsPerMember caps the per-family endpoint list. Two is sufficient
// (one IPv4 + one IPv6) but we leave a little headroom for clients that
// might publish multiple v6 sources (e.g. distinct prefixes) before the
// design needs to change.
const maxEndpointsPerMember = 4

// MatchesEndpoint reports whether the member already lists exactly this
// (ip, port). Used by lobby check-in to detect duplicate keepalives.
func (m *Member) MatchesEndpoint(ip string, port int) bool {
	for _, e := range m.Endpoints {
		if e.IP == ip && e.Port == port {
			return true
		}
	}
	return false
}

// MergeEndpoint records a new (ip, port) for the member. If the member
// already has an endpoint in the same address family, that entry is
// replaced (port mapping shifts are common on consumer NATs). Otherwise
// the endpoint is appended, up to maxEndpointsPerMember.
func (m *Member) MergeEndpoint(ip string, port int) {
	family := ipFamily(ip)
	for i := range m.Endpoints {
		if ipFamily(m.Endpoints[i].IP) == family {
			m.Endpoints[i] = Endpoint{IP: ip, Port: port}
			return
		}
	}
	if len(m.Endpoints) >= maxEndpointsPerMember {
		return
	}
	m.Endpoints = append(m.Endpoints, Endpoint{IP: ip, Port: port})
}

// View renders this member for a request from `requesterIP`. LAN addresses
// are only exposed to peers sharing any of this member's public IPs.
func (m *Member) View(requesterIP string) MemberView {
	v := MemberView{}
	if len(m.Endpoints) > 0 {
		v.Endpoints = append(v.Endpoints, m.Endpoints...)
	}
	if requesterIP != "" && (len(m.LocalIPs) > 0 || len(m.LocalEndpoints) > 0) {
		for _, e := range m.Endpoints {
			if e.IP == requesterIP {
				v.LocalIPs = append(v.LocalIPs, m.LocalIPs...)
				v.LocalEndpoints = append(v.LocalEndpoints, m.LocalEndpoints...)
				break
			}
		}
	}
	return v
}

// ipFamily returns 4 for IPv4 textual addresses, 6 for IPv6, and 0 for
// unparseable inputs. IPv4-mapped IPv6 ("::ffff:1.2.3.4") is reported as
// IPv4 since the server canonicalizes those before storage.
func ipFamily(ip string) int {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return 0
	}
	if parsed.To4() != nil {
		return 4
	}
	return 6
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

func sanitizeLocalEndpoints(in []Endpoint, localIPs []string) []Endpoint {
	if len(in) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(localIPs))
	for _, ip := range localIPs {
		allowed[ip] = struct{}{}
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]Endpoint, 0, len(in))
	for _, raw := range in {
		if !validatePort(raw.Port) || raw.Port == 0 || len(raw.IP) == 0 || len(raw.IP) > maxLocalIPLength {
			continue
		}
		parsed := net.ParseIP(raw.IP)
		if parsed == nil {
			continue
		}
		if !isLocalScopeIP(parsed) {
			continue
		}
		canonical := parsed.String()
		if !parsed.IsLoopback() {
			if _, ok := allowed[canonical]; !ok {
				continue
			}
		}
		key := canonical + ":" + strconv.Itoa(raw.Port)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Endpoint{IP: canonical, Port: raw.Port})
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
