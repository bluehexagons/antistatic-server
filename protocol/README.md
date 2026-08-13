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

Matchmaking declarations are generic over a bounded string metadata object and
default to the bundled Antistatic shape:

```ts
type RacingMetadata = Record<string, string> & {
  vehicle: string;
  ruleset: string;
};

type RacingResponse = MatchmakingResponse<RacingMetadata>;
```

Stable package versions follow server releases. During coordinated development,
Antistatic may pin an immutable `protocol-v*-dev.*` tag that does not trigger a
binary release. The final client pins the matching immutable server release;
mixed client and server versions are unsupported.
