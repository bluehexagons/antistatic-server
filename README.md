# antistatic-server

Low-overhead lobby, matchmaking, peer-introduction, and bounded reporting
server with a bundled profile for
[Antistatic](https://antistaticgame.com/), the uncompromising platform fighter
by bluehexagons.

The coordination core is designed to be straightforward to adapt in a source
fork. It uses ordinary typed Go configuration and a few conspicuous
game-specific files rather than plugins, runtime dispatch, or a matchmaking
rule language. See [FORKING.md](FORKING.md) for the complete adaptation
checklist.

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
- Privacy-bounded crash, feedback, gameplay, performance, and netplay report storage
- TLS-only authenticated report administration

## Basic use

By default, running `antistatic-server` will run on port 80 without enabling HTTPS.
This is suitable only for public health/events and local development. Report
endpoints require HTTPS (except loopback development requests), and deployments
with admin credentials must use `-nohttp` or a trusted TLS-terminating proxy.

Run with `antistatic-server -help` to view all command line options.

### Game profile

The server starts with the built-in Antistatic profile. Pass
`-config path/to/game.json` to overlay a JSON game profile; the checked-in
[`config/antistatic.json`](config/antistatic.json) file is a complete,
prefilled starting point for forks:

```sh
antistatic-server -config config/antistatic.json -port 8080
```

The profile controls the service name shown by the health and admin pages, the
exact `compatibility_id` admitted by the API, recurring queue events,
report/metric route switches, and lobby and matchmaking lifetimes. Property
names use `lower_snake_case`. Durations use Go
duration strings such as `30s`, `2m`, and `1h`. An omitted property keeps the
Antistatic default; an explicit empty `events` array removes every event.

The file is bounded to 64 KiB and parsed and validated once at startup.
Unknown properties, invalid weekdays/durations, duplicate event IDs, and
values above compiled safety ceilings fail startup with a field-specific
error. Request handlers consume the already parsed typed configuration and do
not interpret the profile again.

The hard ceilings currently allow at most 32 recurring events, a 10-minute
lobby member lifetime, a 5-minute waiting-ticket lifetime, a 15-minute active
match lifetime, and 24-hour report-retention and match-code lease lifetimes.
The existing capacity, request-size, identifier, and storage bounds below are
not configurable.

#### Adapting the source for another game

The runtime profile intentionally stops short of being a rule language or
plugin system. Forks normally make their game-facing changes in three
conspicuous files:

- `antistatic_profile.go` owns the bundled service identity, compatibility
  partition, feature defaults, timeouts, and recurring events.
- `antistatic_matchmaking.go` owns typed ticket metadata, its validation,
  compatible-ticket policy, and host/client role assignment.
- `antistatic_reports.go` owns the game-specific gameplay metric schema,
  validation, and ingestion handler.
- `antistatic_admin.go` owns the corresponding retained-record presentation.

The generic lobby lifecycle, peer endpoint disclosure, ticket ownership,
storage mechanics, and HTTP plumbing remain in their ordinary source files.
Changing a wire shape also requires updating `protocol/openapi.json`, its
shared fixtures, and regenerating `protocol/index.d.ts` with `npm run
generate`. The step-by-step source, client, test, and release workflow is in
[FORKING.md](FORKING.md).

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
| `-config` | built-in Antistatic profile | Optional JSON game profile to overlay on the defaults |

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
| Report collection file | 64 MiB |
| Retained records per collection | 10,000 |

When a capacity limit is reached, new state-creating requests return `503 Service Unavailable`; existing tickets and lobby members can continue to refresh until they expire or are deleted.

### Environment variables

| Variable | Description |
|----------|-------------|
| `ANTISTATIC_DATA_DIR` | Directory for the append-only report JSON Lines files. When unset, collection endpoints return 503. |
| `ANTISTATIC_ADMIN_USERNAME` | HTTP Basic username for `/admin/`. Must be set with the password. |
| `ANTISTATIC_ADMIN_PASSWORD` | HTTP Basic password for `/admin/`. Must be set with the username. |

If exactly one admin credential is set, startup fails. If both are unset, admin
routes are not registered and return 404. Admin pages can still be enabled when
the data directory is unset; they show storage as unavailable.

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

`-trust-proxy` is still opt-in. When enabled, forwarded headers are only honored if the immediate TCP peer is in `-trusted-proxy-cidrs`. Requests from a trusted proxy must contain one unambiguous, valid `X-Forwarded-For` or `X-Real-IP` client address; missing or multi-hop values are rejected rather than grouped under the proxy address.

For example, when nginx runs on the same host and proxies to the Go server over loopback, use `-trust-proxy -trusted-proxy-cidrs 127.0.0.1/32`.
If nginx connects over IPv6 loopback, include `::1/128` as well.
Configure the proxy to overwrite one client identity header with its resolved
client address rather than appending a chain. For nginx:

```nginx
proxy_set_header X-Forwarded-For $remote_addr;
proxy_set_header X-Real-IP "";
```

Do not use `$proxy_add_x_forwarded_for` for this service. Health probes sent
through a trusted proxy must follow the same rule; otherwise the fail-closed
identity check returns `400 Bad Request`.

Quick command to generate a self-signed certificate:
```bash
openssl req -newkey rsa:2048 -nodes -keyout cert.key -x509 -days 36525 -out cert.crt
```

## Endpoints

[`protocol/openapi.json`](protocol/openapi.json) is the OpenAPI 3.1 source of
truth for the JSON API. `npm run generate` updates the published TypeScript
declarations, while `npm run check` validates local references and shared
fixtures and fails if the generated file is stale. The package intentionally
adds no runtime validation or handler generation to the server.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | JSON health check with live counts, split HTTP/game error logs, and aggregate activity |
| `GET` | `/health.html` | Human-readable HTML health view with the same data |
| `GET` | `/events` | Upcoming recurring community queue events |
| `PUT` | `/api/v1/lobbies/{key}` | Register/update a lobby member |
| `DELETE` | `/api/v1/lobbies/{key}` | Remove a lobby member |
| `PUT` | `/api/v1/matchmaking/{ticket}` | Register, refresh, or long-poll a matchmaking ticket |
| `DELETE` | `/api/v1/matchmaking/{ticket}` | Cancel a matchmaking ticket |
| `POST` | `/api/v1/matchmaking/{ticket}/outcome` | Submit an authenticated coarse game outcome |
| `POST` | `/api/v1/reports/crash` | Submit a crash report |
| `POST` | `/api/v1/reports/feedback` | Submit feedback |
| `POST` | `/api/v1/metrics/gameplay` | Submit a coarse gameplay sample |
| `POST` | `/api/v1/metrics/performance` | Submit a coarse performance sample |

### Report collection API

Report collection requires `Content-Type: application/json`, rejects unknown
fields and trailing JSON, and uses the global 10 KiB request limit. In the
current v1 schema, every body carries `client_version` for diagnostics and
`compatibility_id` for exact admission. A mismatch returns `426` with the
server's expected identifier. All records store `client_version`.
Report endpoints require HTTPS for non-loopback clients and allow at most 20
requests initially, refilling at 10 requests per minute per client IP and
report path. They do not enable CORS; desktop clients send requests directly.
Every `event_id` must be a fresh random 16-80 character identifier containing
only letters, digits, `_`, and `-`; it exists only to make retries idempotent.
The raw client value is never persisted. While the process is running, the
store uses a process-keyed opaque digest for in-memory retry handling; retries
after a server restart may create a second record.

Crash reports accept:

```json
{
  "client_version": "0.10.8",
  "compatibility_id": "antistatic-v1",
  "event_id": "random-retry-id-0001",
  "platform": "linux",
  "arch": "amd64",
  "reason_code": "segfault",
  "symbols": ["game::update", "engine_main"]
}
```

`client_version` uses the bounded application-version format. `platform` is
`windows`, `linux`, `macos`,
`steamdeck`, or `unknown`. `arch` and `reason_code` are bounded identifiers. `symbols` is required, has at most
48 entries of at most 120 identifier-like characters each, and rejects paths.
The response is `201` with `{"report_id":"cr-..."}`. A duplicate event ID
returns the original ID.

Feedback accepts:

```json
{
  "client_version": "0.10.8",
  "compatibility_id": "antistatic-v1",
  "event_id": "random-retry-id-0002",
  "category": "bug",
  "subject": "Short summary",
  "body": "Description without logs, paths, addresses, or credentials.",
  "related_report_id": "cr-0123456789abcdef"
}
```

`category` is `bug`, `feedback`, or `other`; subject and body are limited to
120 and 6,000 characters. `related_report_id` is optional and accepts a server
crash, feedback, or netplay report ID. Feedback returns the same 201 response
and retry behavior as crash reports.

Gameplay metrics accept:

```json
{
  "event_id": "random-retry-id-0003",
  "mode": "versus",
  "stage": "arena",
  "character": "carbon",
  "opponent_character": "silicon",
  "online": true,
  "completed": true,
  "duration_frames": 3600,
  "local_players": 1,
  "cpu_players": 0,
  "result": "win"
}
```

The four names are bounded identifiers. Player counts are 0-8, duration is
0-10,000,000 frames, and result is `win`, `loss`, `draw`, `unknown`, or `quit`.

Performance metrics accept:

```json
{
  "event_id": "random-retry-id-0004",
  "platform": "linux",
  "arch": "amd64",
  "renderer_family": "vulkan",
  "gpu_vendor": "amd",
  "memory_gib_bucket": "16-31",
  "cpu_cores_bucket": "9-16",
  "resolution_bucket": "1440p",
  "sample_frames": 600,
  "frame_ms_avg": 8.4,
  "frame_ms_p95": 12.1
}
```

Renderer families are `opengl`, `vulkan`, `metal`, `direct3d11`,
`direct3d12`, `webgl`, `other`, or `unknown`. GPU vendors are `amd`, `intel`,
`nvidia`, `apple`, `qualcomm`, `arm`, `imagination`, `other`, or `unknown`.
Memory buckets are `under-4`, `4-7`, `8-15`, `16-31`, `32-63`, `64-plus`, or
`unknown`; CPU buckets are `1-2`, `3-4`, `5-8`, `9-16`, `17-plus`, or
`unknown`; resolution buckets are `720p-or-less`, `1080p`, `1440p`,
`2160p-or-more`, `other`, or `unknown`. Frame values must be finite, positive,
at most 1,000 ms, and p95 must not be below the average. Both metric endpoints
return 204 and deduplicate event IDs within retention.

### Report storage and administration

`ANTISTATIC_DATA_DIR` contains a separate append-only JSON Lines file for each
enabled report feature: `crash.jsonl`, `feedback.jsonl`, `gameplay.jsonl`,
`performance.jsonl`, or `netplay.jsonl`. Disabled collections are not opened,
created, compacted, or shown in the admin UI. Existing files are left in place
when a feature is disabled so re-enabling it is reversible.
The directory is mode 0700 and files are mode 0600. Writes are serialized and
synced. Startup and daily maintenance compaction safely drop malformed records
and expired data; readers also tolerate an incomplete final line. Crash and coarse netplay
reports are retained 90 days, feedback 365 days, and metrics 30 days.

Records contain only a random server report ID, a server time rounded to 15
minutes, app version, and the documented typed fields. The store never adds
client IPs, user agents, authorization headers, tokens, network endpoints,
request paths or payloads, client timestamps, install/session identifiers,
raw logs, or arbitrary metadata. Clients must not put those values into
feedback text. Authenticated netplay reports persist only report ID, version,
coarse event code, and rounded server time; a persistence failure does not
change matchmaking or its response. Netplay persistence uses a bounded
64-record best-effort queue so disk I/O never delays the report response. Each
queued record gets at most two write attempts with a short fixed backoff. A
duplicate received while an ID is pending shares that bounded retry; after a
final failure, a later duplicate can enqueue the same stable report ID again.
A full queue drops that persistence attempt, and already-persisted IDs are not
written twice. Ordinary shutdown drains records that were accepted into the
queue before closing the store.

The dependency-free admin UI is under `/admin/` and includes only the views
enabled by the game profile. Every admin resource, including
`/admin/style.css`, requires HTTP Basic authentication over
TLS. `X-Forwarded-Proto: https` is accepted only when `-trust-proxy` is enabled
and the immediate peer is in `-trusted-proxy-cidrs`. Do not expose Basic auth
over plain HTTP. A reverse proxy must overwrite, not append or pass through,
`X-Forwarded-Proto` from the client and set it from the proxy's own TLS state.
When the server exposes its TLS listener directly and admin auth is enabled,
also pass `-nohttp`; the application cannot prevent a browser from
preemptively sending cached Basic credentials to a separately published plain
HTTP listener.

For backups, snapshot or copy the data volume. A copy made during a write may
end with a partial JSON line, which restore reads safely ignore; a stopped
server or filesystem/volume snapshot gives a fully consistent set. Preserve
0700 directory and 0600 file permissions after restore. Backups contain user
feedback and should be encrypted and access-controlled.

Lobby and matchmaking ownership uses `Authorization: Bearer <token>`. The
first successful `PUT` returns a `token`; clients send it when refreshing,
deleting, or reporting on the same member or ticket. Tokens should not be
logged or shared.

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

`successful_matches` counts pairs formed by the server. The connection success
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
  "client_version": "0.10.8",
  "compatibility_id": "antistatic-v1",
  "port": 45860,
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
    ]
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
  "client_version": "0.10.8",
  "compatibility_id": "antistatic-v1",
  "queue": "default",
  "port": 45860,
  "metadata": {
    "character": "Carbon"
  },
  "local_ips": ["192.168.1.20", "10.0.0.20"],
  "local_endpoints": [
    {"ip": "192.168.1.20", "port": 45860}
  ]
}
```

`metadata` is the small game-specific part of the matchmaking contract. The
bundled Antistatic profile currently requires one bounded `character` string;
the server otherwise treats matching, endpoint exchange, and ticket lifecycle
as generic coordination. The local fields follow the same sanitization and
visibility rules as lobby check-ins. They let same-NAT or same-host clients try
LAN/loopback tunnel candidates without exposing local addresses to unrelated
WAN peers.

For Match by Code, set `queue` to `code` and include a JSON claim:

```json
"match_code": {
  "self_tag": "ALPHA1",
  "peer_tag": "BRAVO2",
  "self_tag_token": "optional-returned-lease-token"
}
```

The server leases the self tag to one owner token for 1 hour, returns that
token as `tag_token`, and only matches reciprocal claims (`A -> B` with
`B -> A`). Matching is case-insensitive. A single client IP can hold at most
8 active match-code leases.

### Matchmaking Queue Measurements
Waiting, matched, and canceled matchmaking responses include aggregate `queue`
measurements for the same compatibility identity and queue:

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

`POST /api/v1/matchmaking/{ticket}/outcome`

The request must include the ticket bearer token, client identity, queue (and
match-code claim when applicable), and one strict JSON event code. The server
keeps only the fixed event code in its bounded recent log and aggregate counter:

```json
{"client_version":"0.10.8","compatibility_id":"antistatic-v1","queue":"default","event":"match_connect_failed"}
```

Supported event codes are `match_connected`, `match_connect_failed`,
`match_handshake_failed`, `match_runtime_error`, `match_sim_desync`,
`match_rollback_refused`, and `match_peer_timeout`. Reports are authenticated
and idempotent per event and ticket. A successful response is
`{"report_id":"nr-..."}`; clients can show that anonymous ID to the player.

The peer-introduction match and its addresses are discarded after two minutes.
The server retains a scrubbed ticket containing only routing/lifecycle fields,
its bearer credential, and report deduplication state for 20 minutes so
failures later in a normal match remain reportable. The health log stores only
the fixed event, report ID, and coarse time; it does not store request paths,
addresses, queue, character, free-form text, or stack traces.
Polling a canceled or expired retained ticket returns `status:"canceled"` with
an empty `endpoints` array; its bearer token remains valid only for the
authenticated report endpoint until the 20-minute retention deadline.
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
      "metadata": {"character": "Silicon"}
    },
    "self": {
      "endpoints": [
        {"ip": "198.51.100.10", "port": 45860}
      ],
      "metadata": {"character": "Carbon"}
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
{"time":"2026-08-13T09:17:56.123Z","level":"ERROR","msg":"Request rejected: invalid remote address","requestID":"abc123"}
```

Request logs deliberately omit addresses, tokens, lobby/ticket identifiers,
request bodies, and report contents.

## Docker
Build and run with Docker:
```bash
docker build -t antistatic-server .
docker run -p 80:80 -p 443:443 antistatic-server
```

Mount a customized game profile read-only and select it explicitly:

```bash
docker run -p 80:80 -p 443:443 \
  -v /host/config/my-game.json:/config/my-game.json:ro \
  antistatic-server -config /config/my-game.json
```

Persist reports and enable the TLS-only admin UI with a mounted data volume:

```bash
docker run -p 443:443 \
  -v antistatic-data:/data \
  -e ANTISTATIC_DATA_DIR=/data \
  -e ANTISTATIC_ADMIN_USERNAME=operator \
  -e ANTISTATIC_ADMIN_PASSWORD='replace-with-a-long-secret' \
  -v /host/tls:/tls:ro \
  antistatic-server -tls -nohttp -cert /tls/server.crt -key /tls/server.key
```

For automatic TLS, publish both ACME/HTTP and HTTPS and persist the certificate cache:
```bash
docker run -p 80:80 -p 443:443 -v antistatic-certs:/certs antistatic-server -autocert example.com -autocert-cache /certs
```

## Building
Requires Go 1.26.5 or later.

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
