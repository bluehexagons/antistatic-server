import type {
  CrashReportRequest,
  GameplayMetricRequest,
  LobbyResponse,
  MatchmakingRequest,
  MatchmakingResponse,
  PerformanceMetricRequest,
} from './index.js';

const matchmaking_request = {
  metadata: { character: 'Carbon' },
  local_ips: ['192.168.1.2'],
  local_endpoints: [{ ip: '192.168.1.2', port: 45860 }],
} satisfies MatchmakingRequest;

const matchmaking_response = {
  status: 'matched',
  ticket: 'ticket-a',
  endpoints: [{ ip: '198.51.100.10', port: 45860 }],
  token: 'owner-token',
  match: {
    id: 'match-a',
    role: 'host',
    matched_at_ms: 1_786_657_200_000,
    self: {
      endpoints: [{ ip: '198.51.100.10', port: 45860 }],
      metadata: { character: 'Carbon' },
    },
    peer: {
      endpoints: [{ ip: '198.51.100.20', port: 45861 }],
      metadata: { character: 'Silicon' },
    },
  },
} satisfies MatchmakingResponse;

const lobby_response = {
  lobby: {
    key: 'ABC123',
    members: [{ endpoints: [{ ip: '198.51.100.10', port: 45860 }] }],
    version: '0.11.0',
  },
  endpoint: { ip: '198.51.100.10', port: 45860 },
  token: 'owner-token',
} satisfies LobbyResponse;

const crash_report = {
  event_id: 'random-event-id-0001',
  app_version: '0.11.0',
  platform: 'linux',
  arch: 'x64',
  reason_code: 'segfault',
  symbols: [],
} satisfies CrashReportRequest;

const gameplay_metric = {
  event_id: 'random-event-id-0002',
  mode: 'versus',
  stage: 'arena',
  character: 'carbon',
  opponent_character: 'silicon',
  online: true,
  completed: true,
  duration_frames: 3600,
  local_players: 1,
  cpu_players: 0,
  result: 'win',
} satisfies GameplayMetricRequest;

const performance_metric = {
  event_id: 'random-event-id-0003',
  platform: 'linux',
  arch: 'x64',
  renderer_family: 'opengl',
  gpu_vendor: 'amd',
  memory_gib_bucket: '16-31',
  cpu_cores_bucket: '9-16',
  resolution_bucket: '1440p',
  sample_frames: 600,
  frame_ms_avg: 8.4,
  frame_ms_p95: 12.1,
} satisfies PerformanceMetricRequest;

if (matchmaking_response.status === 'matched') {
  matchmaking_response.match.peer.metadata.character.toLowerCase();
}

void matchmaking_request;
void lobby_response;
void crash_report;
void gameplay_metric;
void performance_metric;
