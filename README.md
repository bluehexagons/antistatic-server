# antistatic-server
Lobby coordination server for [Antistatic](https://antistaticgame.com/), the uncompromising platform fighter by bluehexagons.

Built on [bluehexagons/gomoose](https://github.com/bluehexagons/gomoose)

## Features
- IPv4 and IPv6 support
- Structured JSON logging with `log/slog`
- Configurable HTTP timeouts
- Automatic TLS with Let's Encrypt or custom certificates
- Rate limiting to prevent abuse
- Docker support
- Health endpoint with lobby statistics

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

### Examples
* `antistatic-server -tls -cert /etc/tls/server.crt -key /etc/tls/server.key` - Custom cert/key locations
* `antistatic-server -tls -nohttp` - HTTPS only, no HTTP
* `antistatic-server -port 8080` - Custom HTTP port
* `antistatic-server -autocert example.com` - Automatic TLS with Let's Encrypt
* `antistatic-server -autocert example.com -autocert-cache /var/cache/certs` - Custom cache directory
* `antistatic-server -read-timeout 30s -write-timeout 30s` - Custom timeouts
* `antistatic-server -trust-proxy` - Trust proxy headers (use with reverse proxy)

Quick command to generate a self-signed certificate:
```bash
openssl req -newkey rsa:2048 -nodes -keyout cert.key -x509 -days 36525 -out cert.crt
```

## Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Health check (returns status, lobby count, version) |
| `PUT` | `/{version}/lobby/{key}/{port}` | Register/update a lobby member |
| `DELETE` | `/{version}/lobby/{key}/{port}` | Remove a lobby member |
| `GET` | `/lobby/{key}/{port}` | Legacy endpoint (no version) |

### Health Endpoint Response
```json
{
  "status": "ok",
  "lobby_count": 3,
  "version": "1.0.0"
}
```

## Client setup
Antistatic checks `config.server` for URL to query.

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
