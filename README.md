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
- JSON and HTML health views with privacy-preserving lobby and matchmaking statistics

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
| `-nohttp` | false | Disables the application HTTP listener (autocert still serves ACME challenges on port 80) |
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
| Match-by-code tag leases per client IP | 8 |

When a capacity limit is reached, new state-creating requests return `503 Service Unavailable`; existing tickets and lobby members can continue to refresh until they expire or are deleted.

### Examples
* `antistatic-server -tls -cert /etc/tls/server.crt -key /etc/tls/server.key` - Custom cert/key locations
* `antistatic-server -tls -nohttp` - HTTPS only, no HTTP
* `antistatic-server -port 8080` - Custom HTTP port
* `antistatic-server -autocert example.com` - Automatic TLS with Let's Encrypt
* `antistatic-server -autocert example.com -autocert-cache /var/cache/certs` - Custom cache directory
* `antistatic-server -read-timeout 30s -write-timeout 30s` - Custom timeouts
* `antistatic-server -trust-proxy -trusted-proxy-cidrs 127.0.0.1/32` - Trust proxy headers from a local reverse proxy

Automatic TLS listens on all interfaces at `-tlsport` (port 443 when omitted). With `-nohttp`, the application HTTP listener stays disabled, but autocert still opens port 80 on all interfaces for ACME HTTP-01 challenges. Both TCP ports must be publicly reachable for certificate issuance and HTTPS service.

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
| `GET` | `/health` | JSON health check with live counts, split HTTP/game error logs, and aggregate activity |
| `GET` | `/health.html` | Human-readable HTML health view with the same data |
| `GET` | `/events` | Upcoming recurring community queue events |
| `PUT` | `/{version}/lobby/{key}/{port}` | Register/update a lobby member |
| `DELETE` | `/{version}/lobby/{key}/{port}` | Remove a lobby member |
| `GET` | `/lobby/{key}/{port}` | Legacy endpoint (no version) |
| `PUT` | `/{version}/matchmaking/{queue}/{ticket}/{port}` | Register or refresh a matchmaking ticket |
| `GET` | `/{version}/matchmaking/{queue}/{ticket}/{port}` | Poll matchmaking ticket status |
| `DELETE` | `/{version}/matchmaking/{queue}/{ticket}/{port}` | Cancel a matchmaking ticket |
| `POST` | `/{version}/matchmaking/{queue}/{ticket}/{port}/report` | Submit an authenticated coarse game-failure report |

Lobby and matchmaking ownership is protected with an `X-Antistatic-Token` header. The first successful `PUT` for a lobby member or matchmaking ticket returns a `token`; clients must send that token in `X-Antistatic-Token` when refreshing, polling, deleting, or reporting on the same member/ticket. Tokens are bearer credentials and should not be logged or shared.

### Health Endpoint Response
```json
{
  "status": "ok",
  "start_time": "2026-04-27T09:17:56.123Z",
  "lobby_count": 3,
  "ticket_count": 2,
  "match_count": 1,
  "tag_lease_count": 1,
  "lobbies_created": 12,
  "successful_matches": 8,
  "match_created_count": 8,
  "queue_attempt_count": 23,
  "match_connection_success_count": 6,
  "match_connection_failure_count": 2,
  "queue_cancellation_count": 9,
  "queue_expiration_count": 4,
  "http_error_count": 1,
  "game_error_count": 0,
  "activity": {
    "window_days": 14,
    "timezone": "UTC",
    "hours": [
      {"hour_utc": 0, "attempts": 12, "matches": 4, "average_match_wait_ms": 22000}
    ]
  },
  "events": [
    {
      "id": "americas-community-queue",
      "name": "Americas community queue",
      "region": "Americas",
      "weekday": "Saturday",
      "start_hour_utc": 21,
      "start_minute_utc": 0,
      "duration_minutes": 60,
      "active": false,
      "starts_at_utc": "2026-08-01T21:00:00Z",
      "ends_at_utc": "2026-08-01T22:00:00Z"
    }
  ],
  "version": "0.6.4"
}
```

The `activity.hours` values are aggregated across all queues for the last 14
days and grouped only by UTC hour. It records successful matchmaking-ticket
creations and matches, not IP addresses, ticket IDs, queue names, characters,
tags, or tokens. Hour buckets with fewer than three attempts are suppressed.
Visible buckets also include client connection successes/failures and queue
cancellations/expirations. Post-connect runtime diagnostics do not inflate the
connection-failure counter.
The counters are in-memory and reset on restart; they are intended as a rough
guide for when queueing is likely to be quieter, not as a player census.

`match_created_count` counts pairs formed by the server. The connection success
and failure counters come from the client report contract below; a match with
no report is intentionally left as unreported rather than guessed as a
failure. Queue cancellations and expirations count tickets that were canceled
or waited past their timeout.

The `http_errors` and `game_errors` arrays contain only a short, bounded list
of coarse error codes, alongside the aggregate error counters. HTTP errors are
protocol or request failures such as an invalid path. Game errors are server
failures and authenticated client reports of a small, fixed set of
connection/handshake/runtime failures. Client reports include an anonymous
report ID so an operator can correlate a player's bug report with the coarse
event. Timestamps are rounded to 15 minutes and request paths, addresses,
tokens, and free-form client messages are never included in the health
response.

### Lobby Check-In PUT Body
```json
{
  "local_ips": ["192.168.1.20", "10.0.0.20"],
  "local_endpoints": [
    {"ip": "192.168.1.20", "port": 45860}
  ]
}
```

`local_ips` and `local_endpoints` are optional. Entries are sanitized to private-scope addresses, and non-loopback endpoint IPs must also appear in `local_ips`. They are only reflected to lobby peers seen from the same public IP.

### Lobby Check-In Response
```json
{
  "lobby": {
    "key": "ABC123",
    "members": [
      {
        "endpoints": [
          {"ip": "198.51.100.10", "port": 45860}
        ],
        "local_ips": ["192.168.1.20"]
      }
    ],
    "version": "0.9.5"
  },
  "endpoint": {
    "ip": "198.51.100.10",
    "port": 45860
  },
  "token": "member-owner-token"
}
```

### Matchmaking PUT Body
```json
{
  "character": "Carbon",
  "local_ips": ["192.168.1.20", "10.0.0.20"],
  "local_endpoints": [
    {"ip": "192.168.1.20", "port": 45860}
  ]
}
```

The local fields follow the same sanitization and visibility rules as lobby check-ins. They let same-NAT or same-host clients try LAN/loopback tunnel candidates without exposing local addresses to unrelated WAN peers.

For Match by Code queues (`code.<tag>-<tag>`), clients also send:

| Header | Description |
|--------|-------------|
| `X-Antistatic-Match-Self-Tag` | The caller's normalized uppercase code |
| `X-Antistatic-Match-Peer-Tag` | The peer code the caller is searching for |
| `X-Antistatic-Match-Self-Tag-Token` | Optional owner token from an earlier `tag_token` response |

The server validates that those two tags derive the requested `code.*` queue, leases the self tag to one owner token for 1 hour, returns that owner token as `tag_token`, and only matches reciprocal claims (`A -> B` with `B -> A`). Code queue matching is case-insensitive; the canonical stored queue uses lowercase tags. A client refreshes the lease by sending the returned owner token when it searches again. To limit abuse, a single client IP can hold at most 8 active match-code leases at a time.

### Matchmaking Queue Measurements
Waiting, matched, and canceled matchmaking responses include aggregate `queue` measurements for the same game version and queue:

```json
{
  "status": "waiting",
  "ticket": "ticket-id",
  "endpoints": [
    {"ip": "198.51.100.10", "port": 45860}
  ],
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

Queue responses may also include cumulative `queue_attempt_count`,
`match_connection_success_count`, `match_connection_failure_count`,
`queue_cancellation_count`, and `queue_expiration_count` for that queue. These
are aggregate counters and do not identify other players.

A matchmaking `PUT` may include `?wait=N` to wait up to `N` seconds for a match before returning a waiting response. Values are clamped to 10 seconds, and the request returns early when the ticket is matched or the client disconnects.

### Client game reports

After a matchmaking ticket has been matched, the client may report a game
failure with an authenticated `POST`:

`POST /{version}/matchmaking/{queue}/{ticket}/{port}/report`

The request must include the ticket's `X-Antistatic-Token` and one strict JSON
event code. The server keeps only the fixed event code in its bounded recent
log and aggregate counter:

```json
{"event":"match_connect_failed"}
```

Supported event codes are `match_connected`, `match_connect_failed`,
`match_handshake_failed`, `match_runtime_error`, `match_sim_desync`,
`match_rollback_refused`, and `match_peer_timeout`. Reports are authenticated
and idempotent per event and ticket. A successful response includes a stable
`X-Antistatic-Report-ID`; clients can show that anonymous ID to the player for
inclusion in a bug report.

The peer-introduction match and its addresses are discarded after two minutes.
The server retains a scrubbed ticket containing only routing/lifecycle fields,
its bearer credential, and report deduplication state for 20 minutes so
failures later in a normal match remain reportable. The health log stores only
the fixed event, report ID, and coarse time; it does not store request paths,
addresses, queue, character, free-form text, or stack traces.
`match_connected` should be sent once the game connection/handshake is usable.
Runtime diagnostic events are tracked separately and do not count as
connection failures.

### Recurring community queue events

The server advertises the next occurrence of two public one-hour invitations
through `/events`, the health responses, and matchmaking responses:

| Event | UTC schedule |
|-------|--------------|
| Americas community queue | Saturday 21:00–22:00 UTC |
| Eurasia community queue | Sunday 18:00–19:00 UTC |

The schedule is a suggestion to concentrate otherwise sparse activity, not a
tracked session. Clients should convert `starts_at_utc` and `ends_at_utc` to
local time and may show a reminder or countdown. UTC keeps the advertised
instant stable when local daylight-saving rules change.

### Matchmaking Matched Response
```json
{
  "status": "matched",
  "ticket": "ticket-id",
  "endpoints": [
    {"ip": "198.51.100.10", "port": 45860}
  ],
  "token": "ticket-owner-token",
  "match": {
    "id": "0.9.5|default|TicketA|TicketB",
    "role": "host",
    "matched_at_ms": 1783692000123,
    "peer": {
      "endpoints": [
        {"ip": "198.51.100.20", "port": 45861}
      ],
      "character": "Silicon"
    },
    "self": {
      "endpoints": [
        {"ip": "198.51.100.10", "port": 45860}
      ],
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

For automatic TLS, publish both ACME/HTTP and HTTPS and persist the certificate cache:
```bash
docker run -p 80:80 -p 443:443 -v antistatic-certs:/certs antistatic-server -autocert example.com -autocert-cache /certs
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
