# antistatic-server
Lobby coordination server for Antistatic, the uncompromising platform fighter by bluehexagons.

Based on gomoose (https://github.com/bluehexagons/gomoose)

## Features

- **Secure**: Input validation, rate limiting, CORS support, and security headers
- **Performant**: Efficient rate limiting with token bucket algorithm
- **Maintainable**: Clean code structure with middleware pattern and comprehensive tests
- **Production-ready**: Graceful shutdown, health checks, and configurable timeouts

## Basic use
By default, running `antistatic-server` will run on port 80 without enabling HTTPS.

Run with `antistatic-server -help` to view all command line options.

By default, HTTPS support looks for `cert.key` and `cert.crt` in the working directory.
Use `-cert path` and `-key path` to specify custom locations.
Specifying a port using -tlsport will implicitly enable TLS.

Examples:
* `antistatic-server -tls -cert /etc/tls/server.crt -key /etc/tls/server.key` will specify custom crt/key locations.
* `antistatic-server -tls -nohttp` will disable HTTP, only providing HTTPS.
* `antistatic-server -port 8080` specifies port to listen on.

Quick command to generate a certificate using OpenSSL:
`openssl req -newkey rsa:2048 -nodes -keyout cert.key -x509 -days 36525 -out cert.crt`

## Endpoints

- `GET /health` - Health check endpoint (returns `{"status":"ok"}`)
- `PUT /{version}/lobby/{key}/{port}` - Register/update a lobby member
- `DELETE /{version}/lobby/{key}/{port}` - Remove a lobby member
- `GET /lobby/{key}/{port}` - Legacy endpoint for old clients

## Client setup
Antistatic checks `config.server` for URL to query.

Set this using the `config` command; e.g. `config server \"http://example.com:8080\"` (quotes must be escaped until strings are better supported).

Can also modify the value by editing the `asconfig` JSON file (e.g. `nano ~/asconfig` from the in-game terminal, or sifting through the `fs.json` save game file) and adding/changing the `server` property there.

## Building
A simple `go build` will build the project. Built and tested with Go 1.24.

## Testing
Run tests with:
```bash
go test -v ./...
```

## Security Features

- **Input Validation**: Lobby keys are validated to prevent injection attacks
- **Rate Limiting**: 60 requests per minute per IP with burst of 120
- **CORS Headers**: Configured for cross-origin requests
- **Security Headers**: X-Content-Type-Options, X-Frame-Options, CSP
- **Request Size Limits**: 10KB maximum request body size
- **Request Timeouts**: 30 second timeout per request
- **Graceful Shutdown**: Clean shutdown with 30 second timeout

## Development

The codebase includes:
- Comprehensive input validation for lobby keys and ports
- Rate limiting middleware with token bucket algorithm  
- Security headers middleware
- Health check endpoint for monitoring
- Graceful shutdown handling with signal catching
- Structured logging throughout
- Unit tests for critical functionality
