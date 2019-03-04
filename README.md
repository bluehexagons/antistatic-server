# antistatic-server
Lobby coordination server for Antistatic, the uncompromising platform fighter by bluehexagons.

Based on gomoose (https://github.com/bluehexagons/gomoose)

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

## Client setup
Antistatic checks `config.server` for URL to query.

Set this using the `config` command; e.g. `config server \"http://example.com:8080\"` (quotes must be escaped until strings are better supported).

Can also modify the value by editing the `asconfig` JSON file (e.g. `nano ~/asconfig` from the in-game terminal, or sifting through the `fs.json` save game file) and adding/changing the `server` property there.

## Building
A simple `go build` will build the project, as it includes no external dependencies. Built on `go1.12`.
