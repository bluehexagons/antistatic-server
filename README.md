# antistatic-server
Lobby coordination server for [Antistatic](https://antistaticgame.com/), the uncompromising platform fighter by bluehexagons.

Built on [bluehexagons/gomoose](https://github.com/bluehexagons/gomoose)

## Features
- IPv4 and IPv6 support
- Keyed lobby and random matchmaking endpoints
- Match-by-code tag leases for private quick matches
- Structured JSON logging with `log/slog`
- Configurable HTTP timeouts
- Automatic TLS with Let's Encrypt or custom certificates
- Rate limiting to prevent abuse
- Bounded in-memory lobby, matchmaking, and rate-limit state
- Docker support
- Health endpoint with lobby and matchmaking statistics

## Basic use
By default, running `antistatic-server` will run on port 80 without enabling HTTPS.

Run with `antistatic-server -help` to view all command line options.

By default, HTTPS support looks for `cert.key` and `cert.crt` in the working directory.
Use `-cert path` and `-key path` to specify custom locations.
Specifying a port using -tlsport will implicitly enable TLS.

### CLI Flags
| Flag | Default | Description |
|------|---------|-------------|
| `-host` | "" | HTTP host to listen on |
| `-port` | 80 | HTTP port to listen on |
| `-tls` | false | Enables TLS (sets tlsport to 443 if unspecified) |
| `-tlshost` | "" | TLS host to listen on |
| `-tlsport` | 0 | TLS port to listen on |
| `-cert` | cert.crt | File to use as TLS cert |
| `-key` | cert.key | File to use as TLS key |
| `-autocert` | "" | Domain for automatic TLS (Let's Encrypt) |
| `-autocert-cache` | certs | Cache directory for autocert certificates |
| `-nohttp` | false | Disables HTTP server |
| `-read-timeout` | 15s | HTTP read timeout |
| `-write-timeout` | 15s | HTTP write timeout |
| `-idle-timeout` | 60s | HTTP idle timeout |
| `-trust-proxy` | false | Trust X-Forwarded-For and X-Real-IP headers |
| `-trusted-proxy-cidrs` | "" | Comma-separated CIDR allowlist for trusted reverse proxies |
| `-stun-host` | "" | Bind address for the built-in STUN responder (default: dual-stack any-address) |
| `-stun-port` | 0 | UDP port for the built-in STUN responder (0 disables; conventional value is 3478) |

### Operational limits

To keep memory and CPU bounded under abusive traffic, the server enforces fixed in-memory limits:

| Limit | Value |
|-------|-------|
| URL path length | 512 bytes |
| Request body size | 10 KiB |
| Tracked rate-limit clients | 65,536 |
| Active lobbies | 10,000 |
| Members per lobby | 128 |
| Matchmaking tickets | 20,000 |
| Active matchmaking matches | 10,000 |
| Match-by-code tag leases | 20,000 |

When a capacity limit is reached, new state-creating requests return `503 Service Unavailable`; existing tickets and lobby members can continue to refresh until they expire or are deleted.

### Examples
* `antistatic-server -tls -cert /etc/tls/server.crt -key /etc/tls/server.key` - Custom cert/key locations
* `antistatic-server -tls -nohttp` - HTTPS only, no HTTP
* `antistatic-server -port 8080` - Custom HTTP port
* `antistatic-server -autocert example.com` - Automatic TLS with Let's Encrypt
* `antistatic-server -autocert example.com -autocert-cache /var/cache/certs` - Custom cache directory
* `antistatic-server -read-timeout 30s -write-timeout 30s` - Custom timeouts
* `antistatic-server -trust-proxy -trusted-proxy-cidrs 127.0.0.1/32` - Trust proxy headers from a local reverse proxy

### Built-in STUN responder

The server can answer RFC 5389 Binding Requests on a UDP port so the
matchmaking client can discover its externally-mapped UDP endpoint without
relying on a third-party STUN service. Enable it with `-stun-port 3478` (and
optionally `-stun-host` to bind a specific address). The UDP port must be
reachable directly from the public internet; UDP traffic is not forwarded by
HTTP reverse proxies, so route 3478/udp through the host firewall to the
server process.

The responder only emits Binding Success replies with a single
XOR-MAPPED-ADDRESS attribute — no auth, no relay, no TURN — and discards
anything that isn't a well-formed Binding Request, so it has the same minimal
attack surface as the existing HTTP listener.

### Reverse proxy setup

`-trust-proxy` is still opt-in. When enabled, forwarded headers are only honored if the immediate TCP peer is in `-trusted-proxy-cidrs`.

For example, when nginx runs on the same host and proxies to the Go server over loopback, use `-trust-proxy -trusted-proxy-cidrs 127.0.0.1/32`.
If nginx connects over IPv6 loopback, include `::1/128` as well.

Quick command to generate a self-signed certificate:
```bash
openssl req -newkey rsa:2048 -nodes -keyout cert.key -x509 -days 36525 -out cert.crt
```

## Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Health check (returns status, live counts, startup lobby creation total, successful game estimate, error count, version) |
| `PUT` | `/{version}/lobby/{key}/{port}` | Register/update a lobby member |
| `DELETE` | `/{version}/lobby/{key}/{port}` | Remove a lobby member |
| `GET` | `/lobby/{key}/{port}` | Legacy endpoint (no version) |
| `PUT` | `/{version}/matchmaking/{queue}/{ticket}/{port}` | Register or refresh a matchmaking ticket |
| `GET` | `/{version}/matchmaking/{queue}/{ticket}/{port}` | Poll matchmaking ticket status |
| `DELETE` | `/{version}/matchmaking/{queue}/{ticket}/{port}` | Cancel a matchmaking ticket |

Lobby and matchmaking ownership is protected with an `X-Antistatic-Token` header. The first successful `PUT` for a lobby member or matchmaking ticket returns a `token`; clients must send that token in `X-Antistatic-Token` when refreshing, polling, or deleting the same member/ticket. Tokens are bearer credentials and should not be logged or shared.

### Health Endpoint Response
```json
{
  "status": "ok",
  "lobby_count": 3,
  "ticket_count": 2,
  "match_count": 1,
  "tag_lease_count": 1,
  "lobbies_created": 12,
  "successful_games_estimate": 8,
  "error_count": 1,
  "version": "0.6.4"
}
```

### Lobby Check-In PUT Body
```json
{
  "local_ips": ["192.168.1.20", "10.0.0.20"]
}
```

`local_ips` is optional. When present, entries are sanitized to private-scope addresses and only reflected to lobby peers seen from the same public IP.

### Lobby Check-In Response
```json
{
  "lobby": {
    "key": "ABC123",
    "members": [
      {
        "ip": "198.51.100.10",
        "port": 45860,
        "local_ips": ["192.168.1.20"]
      }
    ],
    "version": "0.9.5"
  },
  "ip": "198.51.100.10",
  "port": 45860,
  "token": "member-owner-token"
}
```

### Matchmaking PUT Body
```json
{
  "character": "Carbon",
  "local_ips": ["192.168.1.20", "10.0.0.20"]
}
```

`local_ips` is optional for matchmaking too. Entries are sanitized to private-scope addresses and only reflected to matched peers seen from the same public IP, allowing same-NAT or same-host clients to try LAN/loopback tunnel candidates without exposing LAN addresses to unrelated WAN peers.

For Match by Code queues (`code.<tag>-<tag>`), clients also send:

| Header | Description |
|--------|-------------|
| `X-Antistatic-Match-Self-Tag` | The caller's normalized uppercase code |
| `X-Antistatic-Match-Peer-Tag` | The peer code the caller is searching for |

The server validates that those two tags derive the requested `code.*` queue, leases the self tag to the returned matchmaking token while the ticket is waiting, and only matches reciprocal claims (`A -> B` with `B -> A`). Code queue matching is case-insensitive; the canonical stored queue uses lowercase tags.

### Matchmaking Queue Measurements
Waiting, matched, and canceled matchmaking responses include aggregate `queue` measurements for the same game version and queue:

```json
{
  "status": "waiting",
  "ticket": "ticket-id",
  "ip": "198.51.100.10",
  "port": 45860,
  "token": "ticket-owner-token",
  "queue": {
    "players_waiting": 1,
    "own_wait_ms": 12000,
    "oldest_wait_ms": 12000,
    "match_count": 4,
    "average_match_wait_ms": 22000
  }
}
```

The queue data is privacy-preserving aggregate state only. It does not include other players' tickets, IPs, characters, or tokens.

### Matchmaking Matched Response
```json
{
  "status": "matched",
  "ticket": "ticket-id",
  "ip": "198.51.100.10",
  "port": 45860,
  "token": "ticket-owner-token",
  "match": {
    "id": "0.9.5|default|TicketA|TicketB",
    "role": "host",
    "peer": {
      "ip": "198.51.100.20",
      "port": 45861,
      "character": "Silicon"
    },
    "self": {
      "ip": "198.51.100.10",
      "port": 45860,
      "character": "Carbon"
    }
  }
}
```

## Client setup
Antistatic checks `config.server` for the lobby and matchmaking server URL.

Set this using the `config` command; e.g. `config server \"http://example.com:8080\"` (quotes must be escaped until strings are better supported).

The change can be persisted by editing the `asconfig` JSON file (e.g. `nano ~/asconfig` from the in-game terminal, or sifting through the `fs.json` save game file)
and adding/changing the `server` property there. This config is loaded when the game starts.

## Logging
The server uses structured JSON logging via `log/slog`. Example log output:
```json
{"time":"2026-04-27T09:17:56.123Z","level":"INFO","msg":"Lobby request","requestID":"abc123","method":"PUT","ip":"198.51.100.10","port":45860,"key":"ABC123","version":"0.9.5"}
```

## Docker
Build and run with Docker:
```bash
docker build -t antistatic-server .
docker run -p 80:80 -p 443:443 antistatic-server
```

Or with custom flags:
```bash
docker run -p 8080:8080 antistatic-server -port 8080 -tls -autocert example.com
```

## Building
Requires Go 1.24 or later.

```bash
go build -o antistatic-server .
```

For static binary (recommended for production):
```bash
CGO_ENABLED=0 go build -o antistatic-server .
```

## Testing
Run tests with:
```bash
go test -v ./...
```
