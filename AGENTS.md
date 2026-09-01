# Repository Guidelines

## Project structure

`antistatic-server` is a Go lobby, matchmaking, reporting, and STUN service.
The protocol package is generated from `protocol/openapi.json`; its JavaScript
tooling lives under `scripts/`. Keep generic coordination behavior separate
from the Antistatic-specific files prefixed with `antistatic_`.

## Environment and commands

The standard Linux host is an infra-tools-managed agent VM with Go, Node, a C
compiler for race tests, and related repositories below `~/repos`. This
repository's protocol tooling requires Node 26.4+; run `nvm use` from the
checkout before npm commands so `.nvmrc` selects the newest installed release.

- `npm ci`: install protocol-tooling dependencies.
- `./scripts/check.sh`: run the complete local/release verification gate.
- `go test ./...`: run the narrow Go test loop.
- `npm run check`: validate the generated protocol package only.
- `npm run generate`: regenerate declarations after an intentional OpenAPI
  contract change.

Use `infra-tools agent doctor --capability development --json` only when the
managed Go or Node layer is suspect. Keep local data, logs, reports, and other
task evidence under ignored `local-artifacts/`; never commit credentials or
real report contents.

## Testing and releases

Run `./scripts/check.sh` before pushing. Protocol contract changes also require
the consumer coordination described by Antistatic's
`sister-repository-maintenance` skill and `FORKING.md`. Do not move published
protocol or release tags. AI-assisted commits use an imperative subject under
70 characters and append `w/llm`.
