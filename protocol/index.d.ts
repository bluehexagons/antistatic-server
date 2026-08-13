/** Exact JSON wire types for antistatic-server v0.11. */

export type Endpoint = {
  ip: string;
  port: number;
};

export type LobbyCheckInRequest = {
  local_ips?: string[];
  local_endpoints?: Endpoint[];
};

export type LobbyMember = {
  endpoints: Endpoint[];
  local_ips?: string[];
  local_endpoints?: Endpoint[];
};

export type Lobby = {
  key: string;
  members: LobbyMember[];
  version: string;
};

export type LobbyResponse = {
  lobby: Lobby;
  endpoint: Endpoint;
  token?: string;
};

export type CommunityQueueEvent = {
  id: string;
  name: string;
  region: string;
  weekday: string;
  start_hour_utc: number;
  start_minute_utc: number;
  duration_minutes: number;
  active: boolean;
  starts_at_utc: string;
  ends_at_utc: string;
};

export type EventsResponse = {
  events: CommunityQueueEvent[];
};

export type MatchmakingMetadata = Record<string, string>;

export type AntistaticMatchmakingMetadata = MatchmakingMetadata & {
  character: string;
};

export type MatchmakingRequest<Metadata extends MatchmakingMetadata = AntistaticMatchmakingMetadata> =
  LobbyCheckInRequest & {
    metadata: Metadata;
  };

export type MatchmakingPeer<Metadata extends MatchmakingMetadata = AntistaticMatchmakingMetadata> = LobbyMember & {
  metadata: Metadata;
};

export type MatchmakingRole = 'host' | 'client';

export type MatchmakingMatch<Metadata extends MatchmakingMetadata = AntistaticMatchmakingMetadata> = {
  id: string;
  role: MatchmakingRole;
  peer: MatchmakingPeer<Metadata>;
  self: MatchmakingPeer<Metadata>;
  matched_at_ms?: number;
};

export type MatchmakingQueue = {
  players_waiting: number;
  own_wait_ms?: number;
  oldest_wait_ms?: number;
  queue_attempt_count?: number;
  match_count?: number;
  average_match_wait_ms?: number;
  match_connection_success_count?: number;
  match_connection_failure_count?: number;
  queue_cancellation_count?: number;
  queue_expiration_count?: number;
};

type MatchmakingResponseBase = {
  ticket: string;
  endpoints: Endpoint[];
  token?: string;
  tag_token?: string;
  queue?: MatchmakingQueue;
  events?: CommunityQueueEvent[];
};

export type MatchmakingWaitingResponse = MatchmakingResponseBase & {
  status: 'waiting';
  match?: never;
};

export type MatchmakingMatchedResponse<
  Metadata extends MatchmakingMetadata = AntistaticMatchmakingMetadata,
> = MatchmakingResponseBase & {
  status: 'matched';
  match: MatchmakingMatch<Metadata>;
};

export type MatchmakingCanceledResponse = MatchmakingResponseBase & {
  status: 'canceled';
  match?: never;
};

export type MatchmakingResponse<Metadata extends MatchmakingMetadata = AntistaticMatchmakingMetadata> =
  | MatchmakingWaitingResponse
  | MatchmakingMatchedResponse<Metadata>
  | MatchmakingCanceledResponse;

export type MatchmakingOutcome =
  | 'match_connected'
  | 'match_connect_failed'
  | 'match_handshake_failed'
  | 'match_runtime_error'
  | 'match_sim_desync'
  | 'match_rollback_refused'
  | 'match_peer_timeout';

export type MatchmakingOutcomeRequest = {
  event: MatchmakingOutcome;
};

export type ReportResponse = {
  report_id: string;
};

export type Platform = 'windows' | 'linux' | 'macos' | 'steamdeck' | 'unknown';

export type CrashReportRequest = {
  event_id: string;
  app_version: string;
  platform: Platform;
  arch: string;
  reason_code: string;
  symbols: string[];
};

export type FeedbackCategory = 'bug' | 'feedback' | 'other';

export type FeedbackRequest = {
  event_id: string;
  category: FeedbackCategory;
  subject: string;
  body: string;
  related_report_id?: string;
};

export type GameplayResult = 'win' | 'loss' | 'draw' | 'unknown' | 'quit';

export type GameplayMetricRequest = {
  event_id: string;
  mode: string;
  stage: string;
  character: string;
  opponent_character: string;
  online: boolean;
  completed: boolean;
  duration_frames: number;
  local_players: number;
  cpu_players: number;
  result: GameplayResult;
};

export type RendererFamily =
  | 'opengl'
  | 'vulkan'
  | 'metal'
  | 'direct3d11'
  | 'direct3d12'
  | 'webgl'
  | 'other'
  | 'unknown';

export type GPUVendor =
  | 'amd'
  | 'intel'
  | 'nvidia'
  | 'apple'
  | 'qualcomm'
  | 'arm'
  | 'imagination'
  | 'other'
  | 'unknown';

export type MemoryGiBBucket = 'under-4' | '4-7' | '8-15' | '16-31' | '32-63' | '64-plus' | 'unknown';
export type CPUCoresBucket = '1-2' | '3-4' | '5-8' | '9-16' | '17-plus' | 'unknown';
export type ResolutionBucket = '720p-or-less' | '1080p' | '1440p' | '2160p-or-more' | 'other' | 'unknown';

export type PerformanceMetricRequest = {
  event_id: string;
  platform: Platform;
  arch: string;
  renderer_family: RendererFamily;
  gpu_vendor: GPUVendor;
  memory_gib_bucket: MemoryGiBBucket;
  cpu_cores_bucket: CPUCoresBucket;
  resolution_bucket: ResolutionBucket;
  sample_frames: number;
  frame_ms_avg: number;
  frame_ms_p95: number;
};

export type RecentHTTPError = {
  time: string;
  code: string;
  status: number;
};

export type RecentGameError = {
  time: string;
  code: string;
};

export type ActivityHour = {
  hour_utc: number;
  attempts?: number;
  matches?: number;
  match_successes?: number;
  match_failures?: number;
  queue_cancellations?: number;
  queue_expirations?: number;
  average_match_wait_ms?: number;
  suppressed?: boolean;
};

export type ActivitySummary = {
  window_days: number;
  timezone: 'UTC';
  hours: ActivityHour[];
};

export type HealthResponse = {
  status: 'ok';
  start_time: string;
  lobby_count: number;
  ticket_count: number;
  match_count: number;
  tag_lease_count: number;
  lobbies_created: number;
  successful_matches: number;
  match_created_count: number;
  queue_attempt_count: number;
  match_connection_success_count: number;
  match_connection_failure_count: number;
  queue_cancellation_count: number;
  queue_expiration_count: number;
  http_error_count: number;
  game_error_count: number;
  client_error_count: number;
  server_error_count: number;
  http_errors?: RecentHTTPError[];
  game_errors?: RecentGameError[];
  activity: ActivitySummary;
  events: CommunityQueueEvent[];
  version: string;
};
