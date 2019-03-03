# antistatic-server
Lobby coordination server for Antistatic, the uncompromising platform fighter by bluehexagons.

Based on gomoose (https://github.com/bluehexagons/gomoose)

## Basic use
Running with `-ssl -nohttp` flags will disable the HTTP component.

By default, HTTPS support looks for `cert.key` and `cert.crt` in the working directory. Use `-cert path` and `-key path` to specify custom locations.

Run with `antistatic-server -help` to view all command line options.

Examples:
* `antistatic-server -ssl` will enable serving over HTTPS.
* `antistatic-server -dir "/path/to/dir` specifies what directory to serve (defaults to working directory).
* `antistatic-server -port 8080` specifies port to listen on.

Quick command to generate a certificate using OpenSSL:
`openssl req -newkey rsa:2048 -nodes -keyout cert.key -x509 -days 36525 -out cert.crt`

## Client setup
Antistatic checks `config.server` for URL to query.

Set this using the `config` command, or by manually editing the `asconfig` JSON file and adding the `server` property there.

## Building
A simple `go build` will build the project, as it pulls in no external dependencies. Built on `go1.12`.
