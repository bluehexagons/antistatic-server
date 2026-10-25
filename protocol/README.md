# antistatic-server protocol types

This type-only package describes the exact JSON exchanged with the current
`antistatic-server` API. Public properties retain their `lower_snake_case` wire
names. Consumers should use `import type`, which adds no runtime dependency:

```ts
import type {
  MatchmakingRequest,
  MatchmakingResponse,
} from '@bluehexagons/antistatic-server-protocol';
```

These declarations provide compile-time checking but do not validate untrusted
network responses. Validate a response once at the network boundary before
treating it as one of these types.

The checked-in declarations are generated from `protocol/openapi.json` by
`npm run generate`. `npm run check` fails when generated output is stale.
The schema uses concrete Antistatic metadata today while keeping metadata in a
separate component that a source-level adaptation can replace:

```ts
type RacingMetadata = Record<string, string> & {
  vehicle: string;
  ruleset: string;
};

// Replace AntistaticMatchmakingMetadata in a fork's OpenAPI profile,
// then regenerate the declarations.
```

Stable package versions follow server releases. During coordinated development,
Antistatic may pin an immutable `protocol-v*-dev.*` tag that does not trigger a
binary release. The final client pins the matching immutable server release;
mixed client and server versions are unsupported.

The HTTP surface is fixed at `/api/v1`. Every request body includes
`client_version` for diagnostics and `compatibility_id` for exact protocol
admission. Ownership tokens use `Authorization: Bearer …`; no custom ownership
or match-code headers are part of the API.

For Match by Code, persist a random 256-bit `match_code.self_tag_token` before
the initial PUT. An unleased tag adopts that secret. Retrying the same ticket
and tag pair with the secret recovers the original response if it was lost;
DELETE can use the same proof when the ticket bearer token was never received.
The proof must still own the lease's current ticket. A new ticket with that
proof replaces its previous waiting search. Omitting the proof requests a
server-generated tag token, which must be retained from the response.

For ordinary queues, generate a separate random 256-bit `registration_token`
before the first PUT. Reuse it with the same service, compatibility ID, queue,
and ticket for retries and DELETE. It recovers the original bearer after a lost
response, including after matching or peer cancellation. A wrong supplied bearer
never falls back to either ownership proof. Outcome reports remain bearer-only.
The field is additive and optional; clients omitting it cannot recover a lost
ordinary registration response. The compatibility ID therefore stays unchanged.

Every proof-bearing registration reserves bounded replay-protection capacity.
DELETE and waiting-search replacement retire that generation before detaching
its lease. Delayed PUTs return 409 without recreating it; intentional searches
use a new ticket ID. DELETE arriving before PUT also fences that generation.
Ordinary protection lasts the configured report-retention duration after
retirement. Code protection follows the particular lease incarnation through
renewals until expiry or replacement. An absent code DELETE without a current
matching lease is protected for one tag-lease duration. Repeated DELETE does not
extend protection. At capacity new admissions return 503 without evicting
protected generations; cancellation of admitted tickets still succeeds.
This history is in memory: protection ends after its window or server restart.

Report and metric routes use status `426` for two early admission failures.
An incompatible client receives the structured `APIErrorResponse` JSON body;
an insecure non-loopback request receives a plaintext HTTPS-required error.
Consumers should select the representation from `Content-Type` rather than
assuming one body shape from the status alone.
