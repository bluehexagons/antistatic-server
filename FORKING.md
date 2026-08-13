# Adapting antistatic-server for another game

This repository intentionally provides source-level seams, not a plugin or
extension system. A fork produces one game-specific server binary, and its
current client ships with the matching server version. Keeping that assumption
usually makes the result faster and easier to understand than adding dynamic
dispatch or compatibility adapters.

## Decide what can stay in configuration

Start from `config/antistatic.json`. A profile can change these without a Go
edit:

- service name and exact `compatibility_id`;
- enabled event and report features;
- recurring queue event definitions; and
- selected lobby, ticket, match, report-retention, and tag-lease timeouts.

Configuration is parsed and validated once at startup. Compiled limits still
bound request sizes, identifiers, state, storage, and maximum durations. If
your game only needs different identity, events, timeouts, or a subset of the
existing features, a profile may be the only customization required.

## Change the compiled game policy

The main source-level customization points are:

| File | Change here |
| --- | --- |
| `antistatic_profile.go` | Bundled defaults, feature selection, compatibility identity, timeouts, and events |
| `antistatic_matchmaking.go` | Typed ticket metadata, metadata validation, compatible-ticket selection, and participant role assignment |
| `antistatic_reports.go` | Game-specific gameplay metric request, validation, and ingestion |
| `antistatic_admin.go` | Game-specific retained gameplay record presentation |
| `admin.go` | Generic report navigation plus crash, feedback, performance, and outcome presentation |
| `store.go` | Persisted record shapes, retention, deduplication, and collection registration |

Keep generic lifecycle and transport behavior in `lobby.go`, `matchmaking.go`,
`member.go`, `server.go`, and `application.go` unless the game truly needs a
different coordination model. The repository remains one Go package on
purpose: the visible policy files provide ownership boundaries without adding
interfaces, indirect calls, or allocation pressure to request paths.

## Update the wire contract

Public JSON properties use `lower_snake_case`. If a source change affects a
request or response:

1. Update `protocol/openapi.json`, including bounds, enums, required fields,
   authentication, and response variants.
2. Update or add shared valid and invalid fixtures under
   `protocol/fixtures/`.
3. Run `npm run generate` and commit the resulting `protocol/index.d.ts`.
4. Update Go contract tests and the client boundary parsers to consume the
   same fixtures.
5. Bump the protocol package version in `package.json`.

The immutable tag archives the repository root, so npm also sees root files
selected by its package rules, including `README.md`. Treat any package-visible
root change as a package change: make it before the version bump and tag, then
verify `npm pack --dry-run` from the exact tagged archive. Never add such a
change after publishing a protocol tag without issuing a new version.

The generated TypeScript package is type-only. Client code should use `import
type` and should still validate untrusted responses once at the network
boundary. Prefer exact concrete aliases for the shipped game; generic metadata
foundations are useful for forks but do not need to weaken the active client.

There is no legacy route or payload compatibility layer. Change
`compatibility_id` whenever an old client must be rejected, then release the
new client and server together.

## Adapt client and deployment names

Search the client for the server URL, `compatibility_id`, matchmaking metadata,
outcome vocabulary, and telemetry calls. Keep the shared wire objects in
underscore form rather than maintaining a second camel-case representation.

The `ANTISTATIC_*` environment variables, binary/module name, container image,
admin copy, logs, and repository documentation may also be renamed in a
long-lived fork. Those are deployment/source names rather than protocol
aliases, so rename them directly instead of accepting both forms indefinitely.

## Verify and release together

Run the server checks:

```sh
go test ./...
go test -race ./...
go vet ./...
npm run check
npm pack --dry-run
```

Run the client unit, type, and integration/netplay checks appropriate to the
changed contract. For a coordinated protocol change:

1. Commit and push the server source first.
2. Create a new immutable server protocol or release tag; never move a tag.
3. Verify the tag target and the public package archive contents.
4. Pin that immutable archive in the client lockfile.
5. Validate, commit, and push the client.

If the wire contract did not change, do not mint a replacement protocol tag.
